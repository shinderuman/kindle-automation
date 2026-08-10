// Package newrelease は check-worker における新刊チェック3ジョブ種別
// （new_release_search / new_release_result / new_release_detail）のユースケースを実装する。
//
// 本 package は Amazon adapter（HTTP/goquery/selector）と storage adapter（S3 BookRecord）の都合へ依存しない。
// Fetcher が返す SearchHit/ProductInfo と Store interface は新刊ユースケースが必要とする最小の型・操作で、
// Amazon DTO から cmd/worker 層で変換して注入し、S3 adapter は internal/storage へ置く前提とする。
package newrelease

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/shinderuman/kindle-automation/internal/application/execution"
	"github.com/shinderuman/kindle-automation/internal/domain/book"
	"github.com/shinderuman/kindle-automation/internal/domain/scheduling"
	"github.com/shinderuman/kindle-automation/internal/job"
)

// job結果の error_type（SPECIFICATION.md 18.1）。正常時は空。
const (
	errorTypeFetchError       = "fetch_error"
	errorTypeFetchRetryable   = "fetch_retryable"
	errorTypeSearchEmpty      = "search_empty"
	errorTypeUnknownCategory  = "unknown_category"
	errorTypeAsinMismatch     = "asin_mismatch"
	errorTypeNotKindle        = "not_kindle"
	errorTypeTitleUnavailable = "title_unavailable"
	errorTypePriceUnavailable = "price_unavailable"
	errorTypeDateUnavailable  = "release_date_unavailable"
	errorTypeAuthorMismatch   = "author_mismatch"
	errorTypeExcluded         = "excluded"
	errorTypeMissingProduct   = "missing_product"
	errorTypeRetention        = "retention"
	errorTypeAuthorStore      = "author_store"
	errorTypeEnqueueFailed    = "enqueue_failed"
	errorTypeNotifiedUpsert   = "notified_upsert"
	errorTypeUpcomingUpsert   = "upcoming_upsert"
)

// SearchCategory は検索ページ1回の取得結果の分類（SPECIFICATION.md 11.3 相当）。
// HTTP/goquery の都合を含まず、業務分類のみを表す。
type SearchCategory int

const (
	// SearchOK は検索結果あり。
	SearchOK SearchCategory = iota
	// SearchEmpty は検索ページは検証できたが結果0件。既存Go実装と同じく再試行可能。
	SearchEmpty
	// SearchRetryable は 403/429/5xx/CAPTCHA/構造欠落。再試行する。
	SearchRetryable
)

// SearchHit は検索結果1件から抽出した候補。
type SearchHit struct {
	ASIN           string
	Title          string
	URL            string
	KindlePrice    book.Price
	ReleaseDate    time.Time
	HasReleaseDate bool
	AuthorLabel    string
	IsKindle       bool
}

// SearchResult は検索ページ1回の取得結果。
// HTTPStatus/ResponseBytes は構造化ログ（SPECIFICATION.md 18.1）へ伝播するHTTP計測値。未送信時は0。
type SearchResult struct {
	Category      SearchCategory
	Hits          []SearchHit
	HTTPStatus    int
	ResponseBytes int
}

// ProductCategory は商品ページ1回の取得結果の分類。
type ProductCategory int

const (
	// ProductOK は必須構造あり。
	ProductOK ProductCategory = iota
	// ProductNotFound は 404 または商品不存在。terminal。
	ProductNotFound
	// ProductPermanentClientError は恒久的 4xx。terminal。
	ProductPermanentClientError
	// ProductRetryable は 403/429/5xx/CAPTCHA/構造欠落/解析失敗。再試行する。
	ProductRetryable
)

// ProductInfo は商品ページ（詳細job）から必要な取得結果。
type ProductInfo struct {
	ASIN            string
	Title           string
	URL             string
	CurrentPrice    book.Price
	ReleaseDate     time.Time
	HasReleaseDate  bool
	HasKindleSwatch bool
	AuthorLabel     string
}

// ProductResult は商品ページ1回の取得結果。
// HTTPStatus/ResponseBytes は構造化ログ（SPECIFICATION.md 18.1）へ伝播するHTTP計測値。未送信時は0。
type ProductResult struct {
	Category      ProductCategory
	Info          ProductInfo
	HTTPStatus    int
	ResponseBytes int
}

// Candidate は result/detail で共通に使う新刊候補の判定・保存値。
type Candidate struct {
	ASIN        string
	Title       string
	URL         string
	ReleaseDate time.Time
	AuthorLabel string
	KindlePrice book.Price
}

// SearchFetcher は検索ページを1回取得し新刊用の結果へ変換して返す。1起動で最大1回。
type SearchFetcher interface {
	FetchSearch(ctx context.Context, author string) (SearchResult, error)
}

// ProductFetcher は商品ページを1回取得し新刊用の結果へ変換して返す。1起動で最大1回。
type ProductFetcher interface {
	FetchProduct(ctx context.Context, asin string) (ProductResult, error)
}

// NotifiedStore は notified_asins の保存期間適用・存在判定・冪等upsertを担う。
// 保存期間（将来発売分のみ残す）の適用は result/detail で行い、検索jobは存在判定だけ使う。
type NotifiedStore interface {
	// Exists はASINが notified にあるかを保存期間適用なしで返す（検索jobの事前除外用）。
	Exists(ctx context.Context, asin string) (bool, error)
	// ApplyRetentionAndExists は notified の保存期間を適用し、対象ASINが処理開始時に通知済みかを返す。
	ApplyRetentionAndExists(ctx context.Context, asin string, now time.Time) (alreadyNotified bool, err error)
	// Upsert は対象 ASIN を冪等 upsert する。既存 ASIN は値を merge 更新し、不存在なら追加する。重複実行で件数は増えない。
	Upsert(ctx context.Context, b book.KindleBook) error
}

// UpcomingStore は upcoming_asins の冪等upsertを担う。
type UpcomingStore interface {
	Upsert(ctx context.Context, b book.KindleBook) error
}

// AuthorStore は authors.json の最新作更新を担う。
type AuthorStore interface {
	// UpdateLatestRelease は候補の発売日が既存より後なら Author の最新作を更新する。変更があれば true。
	UpdateLatestRelease(ctx context.Context, authorName string, releaseDate time.Time, title, url string) (changed bool, err error)
}

// Enqueuer は後続 job を投入する。
type Enqueuer interface {
	Enqueue(ctx context.Context, job job.Job) error
}

// Notifier は商品通知を送る。best-effort で、失敗しても保存済み状態は巻き戻さない。
// 通知失敗は Notifier adapter が notification_error（SPECIFICATION.md 18.3）で記録する。
type Notifier interface {
	Notify(ctx context.Context, message string) error
}

// Config は新刊ユースケースの設定。
type Config struct {
	ExcludedKeywords []string
}

// Dependencies は新刊ユースケースの外部依存。
type Dependencies struct {
	SearchFetcher  SearchFetcher
	ProductFetcher ProductFetcher
	NotifiedStore  NotifiedStore
	UpcomingStore  UpcomingStore
	AuthorStore    AuthorStore
	Enqueuer       Enqueuer
	Notifier       Notifier
	Config         Config
	Clock          func() time.Time
}

// ErrRetryableFetch は Amazon 取得の再試行可能エラー。Lambda error として SQS へ再配信させる。
type ErrRetryableFetch struct{ ASIN string }

func (e *ErrRetryableFetch) Error() string { return "retryable fetch for " + e.ASIN }

var (
	isbnRe       = regexp.MustCompile(`^\d{10,13}$`)
	yearMonthRe  = regexp.MustCompile(`\d{4}年\d{1,2}月`)
	gistNewRelID = "new_release"
	// roleParenRe は contributor 表記の役割括弧（著）や（イラスト）など半角/全角を取り除く。
	roleParenRe = regexp.MustCompile(`[（(][^)）]*[)）]`)
)

// maxSearchCandidates は検索ページ1回から候補として処理する最大件数（SPECIFICATION.md 13.2）。
const maxSearchCandidates = 10

// HandleNewReleaseSearch は new_release_search ジョブを処理する。
// 検索ページへ1回アクセスし、候補ごとに new_release_result または new_release_detail を投入する。
// 検索job自身は S3 保存も商品通知も行わない（SPECIFICATION.md 13.4, AGENTS.md 4）。
func HandleNewReleaseSearch(ctx context.Context, deps Dependencies, j job.Job) (execution.Outcome, error) {
	author := j.Target.AuthorName
	result, err := deps.SearchFetcher.FetchSearch(ctx, author)
	if err != nil {
		return execution.Errored(errorTypeFetchError, 0, 0), fmt.Errorf("search %s: %w", author, err)
	}
	switch result.Category {
	case SearchRetryable:
		return execution.Errored(errorTypeFetchRetryable, result.HTTPStatus, result.ResponseBytes), &ErrRetryableFetch{ASIN: author}
	case SearchEmpty:
		return execution.Errored(errorTypeSearchEmpty, result.HTTPStatus, result.ResponseBytes),
			fmt.Errorf("search empty for %s", author)
	case SearchOK:
	default:
		return execution.Errored(errorTypeUnknownCategory, result.HTTPStatus, result.ResponseBytes),
			fmt.Errorf("unknown search category %v for %s", result.Category, author)
	}
	// SPECIFICATION.md 13.2: 検索結果の先頭10件だけを候補として処理する。
	hits := result.Hits
	if len(hits) > maxSearchCandidates {
		hits = hits[:maxSearchCandidates]
	}
	for _, hit := range hits {
		if err := enqueueCandidate(ctx, deps, j, hit); err != nil {
			return execution.Errored(errorTypeEnqueueFailed, result.HTTPStatus, result.ResponseBytes), err
		}
	}
	return execution.Completed(result.HTTPStatus, result.ResponseBytes), nil
}

// HandleNewReleaseResult は new_release_result ジョブを処理する。
// 検索結果の product を使って判定・保存・通知を行う（商品ページへの追加アクセスはしない）。
func HandleNewReleaseResult(ctx context.Context, deps Dependencies, j job.Job) (execution.Outcome, error) {
	p := j.Target.Product
	if p == nil {
		return execution.Errored(errorTypeMissingProduct, 0, 0),
			fmt.Errorf("missing product in new_release_result %s", j.JobID)
	}
	return applyCandidate(ctx, deps, j, Candidate{
		ASIN:        p.ASIN,
		Title:       p.Title,
		URL:         p.URL,
		ReleaseDate: p.ReleaseDate,
		AuthorLabel: p.AuthorLabel,
		KindlePrice: book.NewPrice(p.KindlePrice),
	})
}

// HandleNewReleaseDetail は new_release_detail ジョブを処理する。
// 商品ページへ最大1回アクセスし、候補を判定して保存・通知する。
func HandleNewReleaseDetail(ctx context.Context, deps Dependencies, j job.Job) (execution.Outcome, error) {
	asin := j.Target.ASIN
	result, err := deps.ProductFetcher.FetchProduct(ctx, asin)
	if err != nil {
		return execution.Errored(errorTypeFetchError, 0, 0), fmt.Errorf("fetch product %s: %w", asin, err)
	}
	switch result.Category {
	case ProductRetryable:
		return execution.Errored(errorTypeFetchRetryable, result.HTTPStatus, result.ResponseBytes), &ErrRetryableFetch{ASIN: asin}
	case ProductNotFound, ProductPermanentClientError:
		// terminal。対象はリストへ残す。
		return execution.Terminal(notFoundType(result.Category), result.HTTPStatus, result.ResponseBytes), nil
	case ProductOK:
	default:
		return execution.Errored(errorTypeUnknownCategory, result.HTTPStatus, result.ResponseBytes),
			fmt.Errorf("unknown fetch category %v for %s", result.Category, asin)
	}
	info := result.Info
	if info.ASIN != "" && info.ASIN != asin {
		return execution.Terminal(errorTypeAsinMismatch, result.HTTPStatus, result.ResponseBytes), nil // asin_mismatch terminal
	}
	if info.Title == "" {
		return execution.Errored(errorTypeTitleUnavailable, result.HTTPStatus, result.ResponseBytes),
			fmt.Errorf("title not available for %s", asin) // 必須構造欠落 retryable（SPECIFICATION.md 13.4）
	}
	if !info.HasKindleSwatch {
		return execution.Terminal(errorTypeNotKindle, result.HTTPStatus, result.ResponseBytes), nil // not_kindle terminal
	}
	if !info.CurrentPrice.Valid() {
		return execution.Errored(errorTypePriceUnavailable, result.HTTPStatus, result.ResponseBytes),
			fmt.Errorf("kindle price not available for %s", asin) // 解析失敗 retryable
	}
	if !info.HasReleaseDate {
		return execution.Errored(errorTypeDateUnavailable, result.HTTPStatus, result.ResponseBytes),
			fmt.Errorf("release date not available for %s", asin) // 解析失敗 retryable
	}
	if !AuthorMatches(j.Target.AuthorName, info.AuthorLabel) {
		return execution.Terminal(errorTypeAuthorMismatch, result.HTTPStatus, result.ResponseBytes), nil // 作者不一致 terminal
	}
	if ExcludedByKeyword(info.Title, deps.Config.ExcludedKeywords) || ExcludedByYearMonth(info.Title) {
		return execution.Terminal(errorTypeExcluded, result.HTTPStatus, result.ResponseBytes), nil // 除外条件 terminal
	}
	oc, err := applyCandidate(ctx, deps, j, Candidate{
		ASIN:        asin,
		Title:       info.Title,
		URL:         info.URL,
		ReleaseDate: info.ReleaseDate,
		AuthorLabel: info.AuthorLabel,
		KindlePrice: info.CurrentPrice,
	})
	// applyCandidate は Amazon 未アクセスのため計測値を持たない。詳細jobの取得計測値を反映する。
	oc.HTTPStatus = result.HTTPStatus
	oc.ResponseBytes = result.ResponseBytes
	return oc, err
}

// notFoundType は商品ページ取得分類を not_found/permanent_client_error の error_type へ写像する。
func notFoundType(c ProductCategory) string {
	if c == ProductPermanentClientError {
		return "permanent_client_error"
	}
	return "not_found"
}

// enqueueCandidate は検索候補の事前除外を行い、必須項目が揃えば result、不足なら detail を投入する。
func enqueueCandidate(ctx context.Context, deps Dependencies, j job.Job, hit SearchHit) error {
	if hit.ASIN == "" || hit.Title == "" || hit.URL == "" {
		return nil
	}
	if IsISBNASIN(hit.ASIN) {
		return nil
	}
	if ExcludedByKeyword(hit.Title, deps.Config.ExcludedKeywords) {
		return nil
	}
	if ExcludedByYearMonth(hit.Title) {
		return nil
	}
	if !AuthorMatches(j.Target.AuthorName, hit.AuthorLabel) {
		return nil
	}
	exists, err := deps.NotifiedStore.Exists(ctx, hit.ASIN)
	if err != nil {
		return fmt.Errorf("check notified %s: %w", hit.ASIN, err)
	}
	if exists {
		return nil
	}
	if hit.IsKindle && hit.KindlePrice.Valid() && hit.HasReleaseDate {
		return deps.Enqueuer.Enqueue(ctx, buildResultJob(j, hit))
	}
	return deps.Enqueuer.Enqueue(ctx, buildDetailJob(j, hit))
}

// applyCandidate は候補の Author 最新作更新・notified/upcoming 冪等upsert・保存後 best-effort 通知を行う。
// SPECIFICATION.md 13.5/13.6/7.5 の順序と reconcile に従う。
// 結果とHTTP計測値を Outcome で返すが、本関数は Amazon 未アクセスのため計測値は0（呼び出し側が上書き）。
func applyCandidate(ctx context.Context, deps Dependencies, j job.Job, c Candidate) (execution.Outcome, error) {
	now := deps.Clock()

	// 13.6 step1-3: notified 読直し・保存期間（将来分のみ残す）適用・処理開始時の通知済み記録。
	alreadyNotified, err := deps.NotifiedStore.ApplyRetentionAndExists(ctx, c.ASIN, now)
	if err != nil {
		return execution.Errored(errorTypeRetention, 0, 0), fmt.Errorf("apply retention for %s: %w", c.ASIN, err)
	}

	// 13.5: 候補の発売日が LatestReleaseDate より後なら Author の最新作を更新する（過去/将来を問わない）。
	// SPECIFICATION.md 9.3: LatestReleaseURL から query/fragment を除去する。
	// notified/upcoming 用（toBook）は affiliate tag 付きのまま保持する（SPECIFICATION.md 9.2）。
	authorChanged, err := deps.AuthorStore.UpdateLatestRelease(ctx, j.Target.AuthorName, c.ReleaseDate, c.Title, book.CleanURL(c.URL))
	if err != nil {
		return execution.Errored(errorTypeAuthorStore, 0, 0), fmt.Errorf("update author latest for %s: %w", c.ASIN, err)
	}

	// 13.6 step4: Author 変更時は Author 用 gist_update を決定的 job_id で投入する。
	// Gist は authors.json 全体から再生成するため upsert 前に投入しても生存し、
	// upsert 失敗の再実行で authorChanged=false になっても決定的 job_id で欠損・重複しない（7.5）。
	// 投入失敗時は error とし notified/upcoming へ進まない。過去発売分で Author 変更があっても投入する。
	if authorChanged {
		if err := deps.Enqueuer.Enqueue(ctx, buildAuthorGistJob(j)); err != nil {
			return execution.Errored(errorTypeEnqueueFailed, 0, 0), fmt.Errorf("enqueue author gist: %w", err)
		}
	}

	// 13.6 step5-6: 新刊予定（将来発売分）のみ notified と upcoming へ冪等upsertする。
	futureRelease := IsFutureRelease(c.ReleaseDate, now)
	if futureRelease {
		b := toBook(c, now)
		if err := deps.NotifiedStore.Upsert(ctx, b); err != nil {
			return execution.Errored(errorTypeNotifiedUpsert, 0, 0), fmt.Errorf("upsert notified for %s: %w", c.ASIN, err)
		}
		if err := deps.UpcomingStore.Upsert(ctx, b); err != nil {
			return execution.Errored(errorTypeUpcomingUpsert, 0, 0), fmt.Errorf("upsert upcoming for %s: %w", c.ASIN, err)
		}
	}

	// 13.6 step7: 将来発売かつ処理開始時に未通知だった場合のみ best-effort 通知する。
	// 通知失敗は Notifier adapter が notification_error（SPECIFICATION.md 18.3）で記録する。
	if futureRelease && !alreadyNotified {
		_ = deps.Notifier.Notify(ctx, formatNewReleaseMessage(j.Target.AuthorName, c))
	}
	return execution.Completed(0, 0), nil
}

func buildResultJob(j job.Job, hit SearchHit) job.Job {
	product := &job.SearchProduct{
		ASIN:        hit.ASIN,
		Title:       hit.Title,
		URL:         hit.URL,
		KindlePrice: hit.KindlePrice.Yen(),
		ReleaseDate: hit.ReleaseDate,
		AuthorLabel: hit.AuthorLabel,
	}
	return job.Job{
		Version:     job.Version,
		JobID:       scheduling.JobID(string(job.KindNewReleaseResult), j.CycleID, hit.ASIN),
		Kind:        job.KindNewReleaseResult,
		CheckType:   j.CheckType,
		CycleID:     j.CycleID,
		ScheduledAt: j.ScheduledAt,
		Target:      job.Target{ASIN: hit.ASIN, AuthorName: j.Target.AuthorName, Product: product},
	}
}

func buildDetailJob(j job.Job, hit SearchHit) job.Job {
	return job.Job{
		Version:     job.Version,
		JobID:       scheduling.JobID(string(job.KindNewReleaseDetail), j.CycleID, hit.ASIN),
		Kind:        job.KindNewReleaseDetail,
		CheckType:   j.CheckType,
		CycleID:     j.CycleID,
		ScheduledAt: j.ScheduledAt,
		Target:      job.Target{ASIN: hit.ASIN, AuthorName: j.Target.AuthorName},
	}
}

// buildAuthorGistJob は Author 用 gist_update ジョブを生成する。
// job_id は gist_type + 作者名で決定的。Gist updater は authors.json 全体を再生成するため
// Target.GistType は new_release のまま変えない（job schema 互換、SPECIFICATION.md 7.2/15）。
// 作者名を discriminator へ入れることで同一 cycle の異なる作者の Author 変更が
// SQS FIFO 5分 dedup で消えず、同一作者の再試行は同一 job_id で冪等になる。
func buildAuthorGistJob(j job.Job) job.Job {
	return job.Job{
		Version:     job.Version,
		JobID:       scheduling.JobID(string(job.KindGistUpdate), j.CycleID, gistNewRelID+":"+j.Target.AuthorName),
		Kind:        job.KindGistUpdate,
		CheckType:   j.CheckType,
		CycleID:     j.CycleID,
		ScheduledAt: j.ScheduledAt,
		Target:      job.Target{GistType: gistNewRelID},
	}
}

func toBook(c Candidate, now time.Time) book.KindleBook {
	return book.KindleBook{
		ASIN:         c.ASIN,
		Title:        c.Title,
		URL:          c.URL,
		ReleaseDate:  c.ReleaseDate,
		CurrentPrice: c.KindlePrice,
		MaxPrice:     c.KindlePrice,
		CreatedAt:    now,
	}
}

func formatNewReleaseMessage(author string, c Candidate) string {
	return fmt.Sprintf("📚 新刊予定があります: %s\n作者: %s\n発売日: %s\nASIN: %s\n%s",
		c.Title, author, c.ReleaseDate.Format("2006-01-02"), c.ASIN, c.URL)
}

// IsISBNASIN は10〜13桁の数字だけで構成される紙書籍ISBNかを返す（SPECIFICATION.md 13.3）。
func IsISBNASIN(asin string) bool {
	return isbnRe.MatchString(asin)
}

// ExcludedByKeyword は title が除外キーワードのいずれかを含むかを返す。
func ExcludedByKeyword(title string, keywords []string) bool {
	for _, keyword := range keywords {
		if keyword != "" && strings.Contains(title, keyword) {
			return true
		}
	}
	return false
}

// ExcludedByYearMonth は title が「YYYY年M月」パターンを含むかを返す。
func ExcludedByYearMonth(title string) bool {
	return yearMonthRe.MatchString(title)
}

// IsFutureRelease は発売日が処理時刻より後（将来）かを返す（SPECIFICATION.md 13.6）。
func IsFutureRelease(releaseDate, now time.Time) bool {
	return releaseDate.After(now)
}

// AuthorMatches は対象作者名が contributor 表記のいずれかを含むかを返す（SPECIFICATION.md 13.3）。
// 既存Go実装(isNameMatched)と同じく、正規化した対象作者名へ正規化したcontributor名が
// 部分文字列として含まれる場合を一致とする。contributor 表記は複数人を空白区切りで含み得る
// （例: "上原誠 やきいもほくほく"）。役割の括弧（例: "海李 (著)"）を除去した上で空白ごとに分割し、
// いずれかの contributor が対象作者名へ含まれれば一致とする。
func AuthorMatches(authorName, contributorLabel string) bool {
	author := NormalizeAuthorName(authorName)
	if author == "" || contributorLabel == "" {
		return false
	}
	stripped := roleParenRe.ReplaceAllString(contributorLabel, " ")
	for _, c := range strings.Fields(stripped) {
		if strings.Contains(author, NormalizeAuthorName(c)) {
			return true
		}
	}
	return false
}

// NormalizeAuthorName は既存Go実装の normalizeName と同じ正規化を行う。
// 全角ASCII（U+FF01〜U+FF5E）を半角へ、全角スペースを半角へ変換し、全スペースを除去して TrimSpace する。
func NormalizeAuthorName(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r >= '！' && r <= '～' {
			r -= 0xFEE0
		}
		if r == '　' {
			r = ' '
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(strings.ReplaceAll(b.String(), " ", ""))
}
