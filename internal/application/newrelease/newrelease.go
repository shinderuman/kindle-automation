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
	errorTypeMinPriceExcluded = "min_price_excluded"
	errorTypeMissingProduct   = "missing_product"
	errorTypeRetention        = "retention"
	errorTypeAuthorStore      = "author_store"
	errorTypePaperStore       = "paper_store"
	errorTypeEnqueueFailed    = "enqueue_failed"
	errorTypeNotifiedUpsert   = "notified_upsert"
	errorTypeUpcomingUpsert   = "upcoming_upsert"
)

// SearchCategory は検索ページ1回の取得結果の分類（SPECIFICATION.md 11.3 相当）。
// HTTP/goquery の都合を含まず、業務分類のみを表す。
type SearchCategory int

const (
	// SearchOK は検索ページ1回の正常取得を表す。
	SearchOK SearchCategory = iota
	// SearchEmpty は検索ページ検証後に結果0件。既存Go実装と同じく再試行可能。
	SearchEmpty
	// SearchRetryable は 403/429/5xx/CAPTCHA/構造欠落などの再試行可能な取得失敗。
	SearchRetryable
)

// SearchHit は検索ページ1回から得た1候補の情報を表す。
type SearchHit struct {
	ASIN           string
	Title          string
	URL            string
	KindlePrice    book.Price
	ReleaseDate    time.Time
	HasReleaseDate bool
	Contributors   []string
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

// ProductCategory は商品ページ1回の取得結果の分類（SPECIFICATION.md 11.3 相当）。
// HTTP/goquery の都合を含まず、業務分類のみを表す。
type ProductCategory int

const (
	// ProductOK は商品ページの正常取得を表す。
	ProductOK ProductCategory = iota
	// ProductNotFound は 404 または商品不存在。terminal。
	ProductNotFound
	// ProductPermanentClientError は恒久的 4xx。terminal。
	ProductPermanentClientError
	// ProductRetryable は 403/429/5xx/CAPTCHA/構造欠落/解析失敗などの再試行可能な取得失敗。
	ProductRetryable
)

// ProductInfo は商品ページ1回から取得したKindle商品の情報を表す。
type ProductInfo struct {
	ASIN            string
	Title           string
	URL             string
	CurrentPrice    book.Price
	ReleaseDate     time.Time
	HasReleaseDate  bool
	HasKindleSwatch bool
	Contributors    []string
}

// ProductResult は商品ページ1回の取得結果。
// HTTPStatus/ResponseBytes は構造化ログ（SPECIFICATION.md 18.1）へ伝播するHTTP計測値。未送信時は0。
type ProductResult struct {
	Category      ProductCategory
	Info          ProductInfo
	HTTPStatus    int
	ResponseBytes int
}

// PaperPageInfo はISBN候補の紙書籍ページ1回から取得した情報（SPECIFICATION.md 13.4）。
// PaperPrice が未取得のときは Invalid となり、CurrentPrice/MaxPrice を 0 のまま paper_books へ保存する。
type PaperPageInfo struct {
	ASIN           string
	Title          string
	URL            string
	PaperPrice     book.Price
	ReleaseDate    time.Time
	HasReleaseDate bool
	Contributors   []string
}

// PaperPageResult はISBN候補の紙書籍ページ1回の取得結果。
type PaperPageResult struct {
	Category      ProductCategory
	Info          PaperPageInfo
	HTTPStatus    int
	ResponseBytes int
}

// Candidate は notified/upcoming へ保存・通知する候補1件を表す。
type Candidate struct {
	ASIN        string
	Title       string
	URL         string
	ReleaseDate time.Time
	AuthorLabel string
	KindlePrice book.Price
}

// SearchFetcher は検索ページ取得の最小依存インターフェース。
// 1起動で最大1回。
type SearchFetcher interface {
	FetchSearch(ctx context.Context, author string) (SearchResult, error)
}

// ProductFetcher は商品ページ取得の最小依存インターフェース。
// 1起動で最大1回。
type ProductFetcher interface {
	FetchProduct(ctx context.Context, asin string) (ProductResult, error)
}

// PaperPageFetcher はISBN候補の紙書籍ページ取得の最小依存インターフェース（SPECIFICATION.md 13.4）。
// 1起動で最大1回。
type PaperPageFetcher interface {
	FetchPaperPage(ctx context.Context, asin string) (PaperPageResult, error)
}

// PaperCandidateStore は paper_books_asins.json へのASIN単位冪等upsertと存在判定を担う（SPECIFICATION.md 13.3/13.4/15）。
// Amazon 由来 field の追加・変更があった場合だけ changed=true を返す。
type PaperCandidateStore interface {
	UpsertChanged(ctx context.Context, b book.KindleBook) (bool, error)
	// Exists はASINが paper_books_asins.json に存在するかを返す（ISBN候補の事前除外用）。
	Exists(ctx context.Context, asin string) (bool, error)
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

// UpcomingStore は upcoming_asins への冪等upsertを担う最小依存インターフェース。
type UpcomingStore interface {
	Upsert(ctx context.Context, b book.KindleBook) error
}

// AuthorStore は Author の最新作情報更新を担う最小依存インターフェース。
type AuthorStore interface {
	// 候補の発売日が既存より後なら更新。変更があれば true。
	UpdateLatestRelease(ctx context.Context, authorName string, releaseDate time.Time, title, url string) (changed bool, err error)
}

// Enqueuer は後続jobの投入を担う最小依存インターフェース。
type Enqueuer interface {
	Enqueue(ctx context.Context, job job.Job) error
}

// Notifier は商品通知を送る。best-effort で、失敗しても保存済み状態は巻き戻さない。
// 通知失敗は Notifier adapter が notification_error（SPECIFICATION.md 18.3）で記録する。
type Notifier interface {
	Notify(ctx context.Context, message string) error
}

// Config は新刊ジョブの実行設定を表す。
type Config struct {
	ExcludedKeywords []string
	// MinPrice は新刊候補の最低価格（円）。取得できたKindle/紙価格が MinPrice 以下の候補は除外する（SPECIFICATION.md 13.4）。
	MinPrice int
}

// Dependencies は新刊ジョブが依存する adapter・設定・時刻源をまとめる。
type Dependencies struct {
	SearchFetcher       SearchFetcher
	ProductFetcher      ProductFetcher
	PaperPageFetcher    PaperPageFetcher
	NotifiedStore       NotifiedStore
	UpcomingStore       UpcomingStore
	AuthorStore         AuthorStore
	PaperCandidateStore PaperCandidateStore
	Enqueuer            Enqueuer
	Notifier            Notifier
	Config              Config
	Clock               func() time.Time
}

// ErrRetryableFetch は Amazon 取得の再試行可能エラー。Lambda error として SQS へ再配信させる。
type ErrRetryableFetch struct{ ASIN string }

// Error は Amazon 取得の再試行可能エラーである旨のメッセージを返す。
func (e *ErrRetryableFetch) Error() string { return "retryable fetch for " + e.ASIN }

var (
	isbnRe       = regexp.MustCompile(`^\d{10,13}$`)
	yearMonthRe  = regexp.MustCompile(`\d{4}年\d{1,2}月`)
	gistNewRelID = "new_release"
	// gistPaperID は新刊ISBN候補が paper_books へ追加・変更されたときに投入する Paper Gist の gist_type（SPECIFICATION.md 13.4/15）。
	gistPaperID = "paper_to_kindle"
	// roleParenRe は contributor 表記の役割括弧（著）や（イラスト）など半角/全角を取り除く。
	roleParenRe = regexp.MustCompile(`[（(][^)）]*[)）]`)
)

// gistStageNewReleasePaper は新刊ISBN候補による paper_books 追加・変更を示す Paper Gist job の決定的 stage。
// paper_to_kindle checker/detail の stage と区別し、同一 cycle・同一 paperASIN の再試行を同一 job_id で冪等にする。
const gistStageNewReleasePaper = "nr_paper"

// maxSearchCandidates は検索ページ1回から候補として処理する最大件数（SPECIFICATION.md 13.2）。
const maxSearchCandidates = 10

// HandleNewReleaseSearch は新刊検索jobのユースケース entry point。
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

// HandleNewReleaseResult は新刊結果jobのユースケース entry point。
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

// HandleNewReleaseDetail は新刊詳細jobのユースケース entry point。
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
		return execution.Terminal(errorTypeAsinMismatch, result.HTTPStatus, result.ResponseBytes), nil
	}
	if info.Title == "" {
		return execution.Errored(errorTypeTitleUnavailable, result.HTTPStatus, result.ResponseBytes),
			fmt.Errorf("title not available for %s", asin) // SPECIFICATION.md 13.4 必須構造欠落は retryable
	}
	if !info.HasKindleSwatch {
		return execution.Terminal(errorTypeNotKindle, result.HTTPStatus, result.ResponseBytes), nil
	}
	if !info.CurrentPrice.Valid() {
		return execution.Errored(errorTypePriceUnavailable, result.HTTPStatus, result.ResponseBytes),
			fmt.Errorf("kindle price not available for %s", asin)
	}
	if !info.HasReleaseDate {
		return execution.Errored(errorTypeDateUnavailable, result.HTTPStatus, result.ResponseBytes),
			fmt.Errorf("release date not available for %s", asin)
	}
	if !AuthorMatches(j.Target.AuthorName, info.Contributors) {
		return execution.Terminal(errorTypeAuthorMismatch, result.HTTPStatus, result.ResponseBytes), nil
	}
	if ExcludedByKeyword(info.Title, deps.Config.ExcludedKeywords) || ExcludedByYearMonth(info.Title) {
		return execution.Terminal(errorTypeExcluded, result.HTTPStatus, result.ResponseBytes), nil
	}
	oc, err := applyCandidate(ctx, deps, j, Candidate{
		ASIN:        asin,
		Title:       info.Title,
		URL:         info.URL,
		ReleaseDate: info.ReleaseDate,
		AuthorLabel: strings.Join(info.Contributors, " "),
		KindlePrice: info.CurrentPrice,
	})
	// applyCandidate は Amazon 未アクセスのため計測値を持たない。詳細jobの取得計測値を反映する。
	oc.HTTPStatus = result.HTTPStatus
	oc.ResponseBytes = result.ResponseBytes
	return oc, err
}

func notFoundType(c ProductCategory) string {
	if c == ProductPermanentClientError {
		return "permanent_client_error"
	}
	return "not_found"
}

// enqueueCandidate は検索候補の事前除外を行い、ISBN候補は紙detail、それ以外は必須項目が揃えば result・不足なら detail を投入する。
func enqueueCandidate(ctx context.Context, deps Dependencies, j job.Job, hit SearchHit) error {
	if hit.ASIN == "" || hit.Title == "" || hit.URL == "" {
		return nil
	}
	if ExcludedByKeyword(hit.Title, deps.Config.ExcludedKeywords) {
		return nil
	}
	if ExcludedByYearMonth(hit.Title) {
		return nil
	}
	if !AuthorMatches(j.Target.AuthorName, hit.Contributors) {
		return nil
	}
	// SPECIFICATION.md 13.3/13.4: ISBN候補は紙経路（new_release_paper_detail）へ振り分け、捨てない。
	// 紙候補は notified/upcoming に入れないため notified 存在判定は使わず、紙detailで paper_books へ冪等upsertする。
	// 既存paper ASINを通常周期のdetail/Gistから事前除外するため、paper_booksの既存判定を挟む。
	if IsISBNASIN(hit.ASIN) {
		exists, err := deps.PaperCandidateStore.Exists(ctx, hit.ASIN)
		if err != nil {
			return fmt.Errorf("check paper_books %s: %w", hit.ASIN, err)
		}
		if exists {
			return nil
		}
		return deps.Enqueuer.Enqueue(ctx, buildPaperDetailJob(j, hit))
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

	// 価格未取得候補はここへ来ないため Valid な価格だけ比較し、MinPrice 以下なら更新せず終了する（SPECIFICATION.md 13.4）。
	if c.KindlePrice.Valid() && c.KindlePrice.Yen() <= float64(deps.Config.MinPrice) {
		return execution.Terminal(errorTypeMinPriceExcluded, 0, 0), nil
	}

	// 13.6 step1-3: notified 読直し・保存期間（将来分のみ残す）適用・処理開始時の通知済み記録。
	alreadyNotified, err := deps.NotifiedStore.ApplyRetentionAndExists(ctx, c.ASIN, now)
	if err != nil {
		return execution.Errored(errorTypeRetention, 0, 0), fmt.Errorf("apply retention for %s: %w", c.ASIN, err)
	}

	// 13.5: 候補の発売日が LatestReleaseDate より後なら Author の最新作を更新する（過去/将来を問わない）。
	// SPECIFICATION.md 9.3: LatestReleaseURL から query/fragment を除去する。
	// notified/upcoming 用（toBook）は affiliate tag 付きのまま保持する（SPECIFICATION.md 9.2）。
	if _, err := deps.AuthorStore.UpdateLatestRelease(ctx, j.Target.AuthorName, c.ReleaseDate, c.Title, book.CleanURL(c.URL)); err != nil {
		return execution.Errored(errorTypeAuthorStore, 0, 0), fmt.Errorf("update author latest for %s: %w", c.ASIN, err)
	}

	// 13.6 step4: authors.json を確定した後、Author 用 gist_update を authorChanged に関わらず常に投入する。
	// 初回の enqueue が失敗した再配信では UpdateLatestRelease が false を返すが、この経路が同じ決定的 job_id を再投入し、
	// Gist は authors.json 全体から再生成されるため欠落した job を reconcile する（SPECIFICATION.md 7.5, AGENTS.md 非transaction契約）。
	// FIFO 5分 dedup だけを正しさの根拠にせず、enqueue 自体の再試行で再投入されることを優先する。
	// 投入失敗時は error とし notified/upcoming へ進まない。過去発売分でも投入する。
	if err := deps.Enqueuer.Enqueue(ctx, buildAuthorGistJob(j, c.ASIN)); err != nil {
		return execution.Errored(errorTypeEnqueueFailed, 0, 0), fmt.Errorf("enqueue author gist: %w", err)
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
		AuthorLabel: strings.Join(hit.Contributors, " "),
		ItemType:    job.ItemTypeKindle,
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

// buildPaperDetailJob はISBN候補の紙書籍詳細jobを生成する（SPECIFICATION.md 13.4）。
// job_id はKindle detail と同じ cycle+ASIN でも kind が異なるため別 id になり、FIFO dedup で消えない。
func buildPaperDetailJob(j job.Job, hit SearchHit) job.Job {
	return job.Job{
		Version:     job.Version,
		JobID:       scheduling.JobID(string(job.KindNewReleasePaperDetail), j.CycleID, hit.ASIN),
		Kind:        job.KindNewReleasePaperDetail,
		CheckType:   j.CheckType,
		CycleID:     j.CycleID,
		ScheduledAt: j.ScheduledAt,
		Target:      job.Target{ASIN: hit.ASIN, AuthorName: j.Target.AuthorName},
	}
}

// buildAuthorGistJob は Author 用 gist_update ジョブを生成する。
// job_id は gist_type + 作者名 + 候補ASIN で決定的。Gist updater は authors.json 全体を再生成するため
// Target.GistType は new_release のまま変えない（job schema 互換、SPECIFICATION.md 7.2/15）。
// 候補ASIN（状態変更元）を discriminator へ入れることで同一 cycle・同一作者の複数候補（A/B）の
// Author 変更がそれぞれ別 job_id となり SQS FIFO 5分 dedup で消えず、
// 同一候補の再試行は同一 job_id で冪等になる。異なる作者も当然別 job_id になる。
// candidateASIN は result/detail job の必須 ASIN（requireASIN 済み）を渡すため空にはならない。
func buildAuthorGistJob(j job.Job, candidateASIN string) job.Job {
	return job.Job{
		Version:     job.Version,
		JobID:       scheduling.JobID(string(job.KindGistUpdate), j.CycleID, gistNewRelID+":"+j.Target.AuthorName+":"+candidateASIN),
		Kind:        job.KindGistUpdate,
		CheckType:   j.CheckType,
		CycleID:     j.CycleID,
		ScheduledAt: j.ScheduledAt,
		Target:      job.Target{GistType: gistNewRelID},
	}
}

// buildNewReleasePaperGistJob はISBN候補による paper_books 追加・変更後に投入する Paper Gist job を生成する（SPECIFICATION.md 13.4/15）。
// Gist updater は paper_books 全体を再生成するため gist_type は paper_to_kindle を使う。
// stage+paperASIN で決定的 id にし、paper_to_kindle checker/detail の gist job と区別しつつ再試行は冪等にする。
func buildNewReleasePaperGistJob(j job.Job, paperASIN string) job.Job {
	return job.Job{
		Version:     job.Version,
		JobID:       scheduling.JobID(string(job.KindGistUpdate), j.CycleID, gistPaperID+":"+gistStageNewReleasePaper+":"+paperASIN),
		Kind:        job.KindGistUpdate,
		CheckType:   j.CheckType,
		CycleID:     j.CycleID,
		ScheduledAt: j.ScheduledAt,
		Target:      job.Target{GistType: gistPaperID},
	}
}

// HandleNewReleasePaperDetail はISBN紙書籍候補の詳細job（SPECIFICATION.md 13.4）。
// 商品ページへ最大1回アクセスし、ASIN/作者/タイトル/発売日を検証して paper_books_asins.json へ upsert する。
// 紙価格は取得できれば CurrentPrice=MaxPrice へ保存し、未取得なら 0/0 で保存して価格初期化は Paper-to-Kindle（§14）へ委ねる。
// notified/upcoming/unprocessed へは入れず、authors.LatestRelease も更新しない。
func HandleNewReleasePaperDetail(ctx context.Context, deps Dependencies, j job.Job) (execution.Outcome, error) {
	asin := j.Target.ASIN
	result, err := deps.PaperPageFetcher.FetchPaperPage(ctx, asin)
	if err != nil {
		return execution.Errored(errorTypeFetchError, 0, 0), fmt.Errorf("fetch paper page %s: %w", asin, err)
	}
	switch result.Category {
	case ProductRetryable:
		return execution.Errored(errorTypeFetchRetryable, result.HTTPStatus, result.ResponseBytes), &ErrRetryableFetch{ASIN: asin}
	case ProductNotFound, ProductPermanentClientError:
		return execution.Terminal(notFoundType(result.Category), result.HTTPStatus, result.ResponseBytes), nil
	case ProductOK:
	default:
		return execution.Errored(errorTypeUnknownCategory, result.HTTPStatus, result.ResponseBytes),
			fmt.Errorf("unknown fetch category %v for %s", result.Category, asin)
	}
	info := result.Info
	if info.ASIN != "" && info.ASIN != asin {
		return execution.Terminal(errorTypeAsinMismatch, result.HTTPStatus, result.ResponseBytes), nil
	}
	if info.Title == "" {
		return execution.Errored(errorTypeTitleUnavailable, result.HTTPStatus, result.ResponseBytes),
			fmt.Errorf("title not available for paper %s", asin)
	}
	if !info.HasReleaseDate {
		return execution.Errored(errorTypeDateUnavailable, result.HTTPStatus, result.ResponseBytes),
			fmt.Errorf("release date not available for paper %s", asin)
	}
	if !AuthorMatches(j.Target.AuthorName, info.Contributors) {
		return execution.Terminal(errorTypeAuthorMismatch, result.HTTPStatus, result.ResponseBytes), nil
	}
	if ExcludedByKeyword(info.Title, deps.Config.ExcludedKeywords) || ExcludedByYearMonth(info.Title) {
		return execution.Terminal(errorTypeExcluded, result.HTTPStatus, result.ResponseBytes), nil
	}
	if info.PaperPrice.Valid() && info.PaperPrice.Yen() <= float64(deps.Config.MinPrice) {
		return execution.Terminal(errorTypeMinPriceExcluded, result.HTTPStatus, result.ResponseBytes), nil
	}
	price := info.PaperPrice
	b := book.KindleBook{
		ASIN:         asin,
		Title:        info.Title,
		URL:          info.URL,
		ReleaseDate:  info.ReleaseDate,
		CurrentPrice: price,
		MaxPrice:     price,
		CreatedAt:    deps.Clock(),
	}
	if _, err := deps.PaperCandidateStore.UpsertChanged(ctx, b); err != nil {
		return execution.Errored(errorTypePaperStore, result.HTTPStatus, result.ResponseBytes),
			fmt.Errorf("upsert paper book %s: %w", asin, err)
	}
	// upsert後のenqueue失敗を再配信で補完するため、changed=falseとなる再配信でもPaper Gistを投入する。
	if err := deps.Enqueuer.Enqueue(ctx, buildNewReleasePaperGistJob(j, asin)); err != nil {
		return execution.Errored(errorTypeEnqueueFailed, result.HTTPStatus, result.ResponseBytes),
			fmt.Errorf("enqueue new_release paper gist: %w", err)
	}
	return execution.Completed(result.HTTPStatus, result.ResponseBytes), nil
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

// IsISBNASIN は ASIN が 10〜13 桁の数字のみで構成される紙書籍 ISBN かを返す（SPECIFICATION.md 13.3）。
func IsISBNASIN(asin string) bool {
	return isbnRe.MatchString(asin)
}

// ExcludedByKeyword は title が除外 keywords のいずれかを含むかを返す。
func ExcludedByKeyword(title string, keywords []string) bool {
	for _, keyword := range keywords {
		if keyword != "" && strings.Contains(title, keyword) {
			return true
		}
	}
	return false
}

// ExcludedByYearMonth は title に「YYYY年M月」形式の年月表記が含まれるかを返す。
func ExcludedByYearMonth(title string) bool {
	return yearMonthRe.MatchString(title)
}

// IsFutureRelease は発売日が now より未来か（SPECIFICATION.md 13.6）。
func IsFutureRelease(releaseDate, now time.Time) bool {
	return releaseDate.After(now)
}

// AuthorMatches は対象作者名が contributor 表記のいずれかと完全一致するかを返す（SPECIFICATION.md 13.3）。
// 各 contributor ごとに役割表記（(著)等）を除去し、NormalizeAuthorName で正規化した完全名同士を比較する。
// contributor 境界は HTML parser が要素単位で保持した []string であり、ここで空白トークンへ分解しない。
// したがって「山田 太郎」を姓と名に分けて対象「山田次郎」へ部分一致させる誤検出は起きない。
func AuthorMatches(authorName string, contributors []string) bool {
	author := NormalizeAuthorName(authorName)
	if author == "" {
		return false
	}
	for _, c := range contributors {
		stripped := roleParenRe.ReplaceAllString(c, " ")
		if NormalizeAuthorName(stripped) == author {
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
