// Package papertokindle は check-worker における紙書籍・Kindle版チェック2ジョブ種別
// （paper_to_kindle_check / paper_to_kindle_detail）のユースケースを実装する。
//
// 本 package は Amazon adapter（HTTP/goquery/selector）と storage adapter（S3 BookRecord）の都合へ依存しない。
// Fetcher が返す PaperPageInfo/KindlePageInfo と Store interface は紙→Kindle ユースケースが必要とする最小の型・操作で、
// Amazon DTO から cmd/worker 層で変換して注入し、S3 adapter は internal/storage へ置く前提とする。
package papertokindle

import (
	"context"
	"fmt"
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
	errorTypeNotFound         = "not_found"
	errorTypePermanentClient  = "permanent_client_error"
	errorTypeUnknownCategory  = "unknown_category"
	errorTypeAsinMismatch     = "asin_mismatch"
	errorTypeSameAsin         = "same_asin"
	errorTypeNotKindle        = "not_kindle"
	errorTypeTitleUnavailable = "title_unavailable"
	errorTypePriceUnavailable = "price_unavailable"
	errorTypeDateUnavailable  = "release_date_unavailable"
	errorTypeStoreFailed      = "store_failed"
	errorTypeEditionMismatch  = "edition_mismatch"
	errorTypeNotPaperBook     = "not_paper_book"
	errorTypeKindleNA         = "kindle_not_available"
	errorTypeStoreUpdate      = "store_update"
	errorTypeTargetRemoved    = "target_removed"
	errorTypeEnqueueFailed    = "enqueue_failed"
	errorTypeKnownState       = "known_state"
	errorTypeManualDelete     = "manual_delete"
	errorTypeNotifiedUpsert   = "notified_upsert"
	errorTypeUpcomingUpsert   = "upcoming_upsert"
	errorTypePaperDelete      = "paper_delete"
)

// Category は商品ページ1回の取得結果の分類（SPECIFICATION.md 11.3 相当）。
// HTTP/goquery の都合を含まず、業務分類のみを表す。
type Category int

const (
	// CategoryOK は商品ページの正常取得を表す。
	CategoryOK Category = iota
	// CategoryNotFound は 404 または商品不存在。terminal。
	CategoryNotFound
	// CategoryPermanentClientError は恒久的 4xx。terminal。
	CategoryPermanentClientError
	// CategoryRetryable は 403/429/5xx/CAPTCHA/構造欠落/解析失敗。再試行する。
	CategoryRetryable
)

// PaperPageInfo は紙書籍ページ1回から取得した情報を表す。
// 紙書籍ページ（check job）から取得する。
type PaperPageInfo struct {
	ASIN             string
	Title            string
	PaperPrice       book.Price
	HasPaperSwatch   bool
	HasKindleSwatch  bool
	KindleSwatchASIN string
}

// PaperCheckResult は紙書籍ページ1回の取得結果。
// HTTPStatus/ResponseBytes は構造化ログ（SPECIFICATION.md 18.1）へ伝播するHTTP計測値。未送信時は0。
type PaperCheckResult struct {
	Category      Category
	Info          PaperPageInfo
	HTTPStatus    int
	ResponseBytes int
}

// KindlePageInfo はKindle商品ページ1回から取得した情報を表す。
// Kindle商品ページ（detail job）から取得する。
type KindlePageInfo struct {
	ASIN            string
	Title           string
	CurrentPrice    book.Price
	ReleaseDate     time.Time
	HasReleaseDate  bool
	HasKindleSwatch bool
}

// KindleDetailResult はKindle商品ページ1回の取得結果。
// HTTPStatus/ResponseBytes は構造化ログ（SPECIFICATION.md 18.1）へ伝播するHTTP計測値。未送信時は0。
type KindleDetailResult struct {
	Category      Category
	Info          KindlePageInfo
	HTTPStatus    int
	ResponseBytes int
}

// DetectedBook は紙→Kindle 判定で保存・通知する候補1件を表す。
type DetectedBook struct {
	ASIN        string
	Title       string
	KindlePrice book.Price
	ReleaseDate time.Time
	PaperPrice  book.Price
	PaperURL    string
}

// KnownState は処理開始時の候補/対象の既知状態（SPECIFICATION.md 14.4 step1）。
type KnownState struct {
	NotifiedExists    bool
	UpcomingExists    bool
	UnprocessedExists bool
	PaperBookExists   bool
}

// PaperPageFetcher は紙書籍ページ取得の最小依存インターフェース。
// 1起動で最大1回。
type PaperPageFetcher interface {
	FetchPaperPage(ctx context.Context, paperASIN string) (PaperCheckResult, error)
}

// KindlePageFetcher はKindle商品ページ取得の最小依存インターフェース。
// 1起動で最大1回。
type KindlePageFetcher interface {
	FetchKindlePage(ctx context.Context, kindleASIN string) (KindleDetailResult, error)
}

// PaperBooksStore は paper_books の取得・更新・削除を担う最小依存インターフェース。
type PaperBooksStore interface {
	// 手動削除時 applied=false。
	UpdateOneBook(ctx context.Context, paperASIN string, update func(book.KindleBook) book.KindleBook) (bool, error)
	// 冪等（既になければ何もしない）。
	Delete(ctx context.Context, paperASIN string) error
	// 存在しない場合は ok=false。
	PaperBook(ctx context.Context, paperASIN string) (book.KindleBook, bool, error)
}

// NotifiedStore は notified_asins への冪等upsertを担う最小依存インターフェース。
type NotifiedStore interface {
	Upsert(ctx context.Context, b book.KindleBook) error
}

// UpcomingStore は upcoming_asins への冪等upsertを担う最小依存インターフェース。
type UpcomingStore interface {
	Upsert(ctx context.Context, b book.KindleBook) error
}

// KnownStateQuerier は処理開始時の候補/対象の既知状態を返す（SPECIFICATION.md 14.4 step1, 7.5 reconcile）。
type KnownStateQuerier interface {
	KnownState(ctx context.Context, kindleASIN, paperASIN string) (KnownState, error)
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

// Config は紙→Kindle ジョブの実行設定を表す。
type Config struct {
	// 保存用 Kindle URL へ付ける Amazon Affiliate Tag（SPECIFICATION.md 9.2）。
	PartnerTag string
}

// Dependencies は紙→Kindle 2ジョブが依存する adapter・設定・時刻源をまとめる。
type Dependencies struct {
	PaperPageFetcher  PaperPageFetcher
	KindlePageFetcher KindlePageFetcher
	PaperBooksStore   PaperBooksStore
	NotifiedStore     NotifiedStore
	UpcomingStore     UpcomingStore
	KnownStateQuerier KnownStateQuerier
	Enqueuer          Enqueuer
	Notifier          Notifier
	Config            Config
	Clock             func() time.Time
}

// ErrRetryableFetch は Amazon 取得の再試行可能エラー。Lambda error として SQS へ再配信させる。
type ErrRetryableFetch struct{ ASIN string }

// Error は Amazon 取得の再試行可能エラーである旨のメッセージを返す。
func (e *ErrRetryableFetch) Error() string { return "retryable fetch for " + e.ASIN }

var (
	jst          = time.FixedZone("JST", 9*60*60)
	gistPaperID  = "paper_to_kindle"
	paperURLBase = "https://www.amazon.co.jp/dp/"
)

// paper_to_kindle gist_update の状態変更 stage。
// 同一 cycle で「価格初期化」と「紙書籍削除」を別の gist job として区別する（SPECIFICATION.md 7.2）。
// 異なる状態変更は別 job_id、同一状態変更の再試行は同一 job_id になる。
const (
	gistStagePaperPriceInit = "price_init"
	gistStagePaperDelete    = "delete"
)

// HandlePaperToKindleCheck は紙→Kindle check jobのユースケース entry point。
// Kindle候補がスウォッチから得られた場合は paper_to_kindle_detail を投入し、同じ起動で詳細へはアクセスしない。
func HandlePaperToKindleCheck(ctx context.Context, deps Dependencies, j job.Job) (execution.Outcome, error) {
	paperASIN := j.Target.ASIN
	result, err := deps.PaperPageFetcher.FetchPaperPage(ctx, paperASIN)
	if err != nil {
		return execution.Errored(errorTypeFetchError, 0, 0), fmt.Errorf("fetch paper page %s: %w", paperASIN, err)
	}
	switch result.Category {
	case CategoryRetryable:
		return execution.Errored(errorTypeFetchRetryable, result.HTTPStatus, result.ResponseBytes), &ErrRetryableFetch{ASIN: paperASIN}
	case CategoryNotFound, CategoryPermanentClientError:
		// terminal。対象は paper_books へ残す。
		return execution.Terminal(notFoundType(result.Category), result.HTTPStatus, result.ResponseBytes), nil
	case CategoryOK:
	default:
		return execution.Errored(errorTypeUnknownCategory, result.HTTPStatus, result.ResponseBytes),
			fmt.Errorf("unknown fetch category %v for %s", result.Category, paperASIN)
	}

	info := result.Info
	if info.ASIN != "" && info.ASIN != paperASIN {
		return execution.Terminal(errorTypeAsinMismatch, result.HTTPStatus, result.ResponseBytes), nil
	}

	if info.PaperPrice.Valid() {
		applied, err := deps.PaperBooksStore.UpdateOneBook(ctx, paperASIN, func(old book.KindleBook) book.KindleBook {
			if !old.CurrentPrice.Valid() {
				old.CurrentPrice = info.PaperPrice
				old.MaxPrice = info.PaperPrice
			}
			return old
		})
		if err != nil {
			return execution.Errored(errorTypeStoreUpdate, result.HTTPStatus, result.ResponseBytes),
				fmt.Errorf("initialize paper price %s: %w", paperASIN, err)
		}
		if !applied {
			return execution.Terminal(errorTypeTargetRemoved, result.HTTPStatus, result.ResponseBytes), nil
		}
		// 価格が既に設定済みでも常に投入する。Gist は paper_books 全体から毎回再生成する（15）ため
		// 冗長な再投入は安全であり、初回 enqueue 失敗後の再配信で永続状態に依存せず欠落を reconcile する（7.5）。
		if err := deps.Enqueuer.Enqueue(ctx, buildPaperToKindleGistJob(j, gistStagePaperPriceInit, j.Target.ASIN)); err != nil {
			return execution.Errored(errorTypeEnqueueFailed, result.HTTPStatus, result.ResponseBytes),
				fmt.Errorf("enqueue paper-to-kindle gist after price init: %w", err)
		}
	}

	if !info.HasPaperSwatch {
		return execution.Terminal(errorTypeNotPaperBook, result.HTTPStatus, result.ResponseBytes), nil
	}
	if !info.HasKindleSwatch || info.KindleSwatchASIN == "" {
		return execution.Terminal(errorTypeKindleNA, result.HTTPStatus, result.ResponseBytes), nil
	}

	// Kindle候補は同一商品ページの形式スウォッチから得たものだけ（SPECIFICATION.md 14.2）。詳細は後続jobへ。
	if err := deps.Enqueuer.Enqueue(ctx, buildDetailJob(j, info.KindleSwatchASIN)); err != nil {
		return execution.Errored(errorTypeEnqueueFailed, result.HTTPStatus, result.ResponseBytes),
			fmt.Errorf("enqueue paper-to-kindle detail: %w", err)
	}
	return execution.Completed(result.HTTPStatus, result.ResponseBytes), nil
}

// HandlePaperToKindleDetail は紙→Kindle detail jobのユースケース entry point。
// Kindle商品ページへ最大1回アクセスし、SPEC 14.3 の対応判定後、notified/upcoming/paper_books を更新する。
func HandlePaperToKindleDetail(ctx context.Context, deps Dependencies, j job.Job) (execution.Outcome, error) {
	kindleASIN := j.Target.ASIN
	paperASIN := j.Target.SourceASIN
	result, err := deps.KindlePageFetcher.FetchKindlePage(ctx, kindleASIN)
	if err != nil {
		return execution.Errored(errorTypeFetchError, 0, 0), fmt.Errorf("fetch kindle page %s: %w", kindleASIN, err)
	}
	switch result.Category {
	case CategoryRetryable:
		return execution.Errored(errorTypeFetchRetryable, result.HTTPStatus, result.ResponseBytes), &ErrRetryableFetch{ASIN: kindleASIN}
	case CategoryNotFound, CategoryPermanentClientError:
		return execution.Terminal(notFoundType(result.Category), result.HTTPStatus, result.ResponseBytes), nil
	case CategoryOK:
	default:
		return execution.Errored(errorTypeUnknownCategory, result.HTTPStatus, result.ResponseBytes),
			fmt.Errorf("unknown fetch category %v for %s", result.Category, kindleASIN)
	}

	info := result.Info
	if info.ASIN != "" && info.ASIN != kindleASIN {
		return execution.Terminal(errorTypeAsinMismatch, result.HTTPStatus, result.ResponseBytes), nil
	}
	// SPEC 14.3 必須。条件を満たさない場合は edition_mismatch として対象を削除せず残す。
	if kindleASIN == paperASIN {
		return execution.Terminal(errorTypeSameAsin, result.HTTPStatus, result.ResponseBytes), nil
	}
	if !info.HasKindleSwatch {
		return execution.Terminal(errorTypeNotKindle, result.HTTPStatus, result.ResponseBytes), nil
	}
	if info.Title == "" {
		return execution.Errored(errorTypeTitleUnavailable, result.HTTPStatus, result.ResponseBytes),
			fmt.Errorf("title not available for %s", kindleASIN)
	}
	if !info.CurrentPrice.Valid() {
		return execution.Errored(errorTypePriceUnavailable, result.HTTPStatus, result.ResponseBytes),
			fmt.Errorf("kindle price not available for %s", kindleASIN)
	}
	if !info.HasReleaseDate {
		return execution.Errored(errorTypeDateUnavailable, result.HTTPStatus, result.ResponseBytes),
			fmt.Errorf("release date not available for %s", kindleASIN)
	}
	paperBook, paperExists, err := deps.PaperBooksStore.PaperBook(ctx, paperASIN)
	if err != nil {
		return execution.Errored(errorTypeStoreFailed, result.HTTPStatus, result.ResponseBytes),
			fmt.Errorf("load paper book %s: %w", paperASIN, err)
	}
	if paperExists && !IsSameReleaseDayJST(paperBook.ReleaseDate, info.ReleaseDate) {
		return execution.Terminal(errorTypeEditionMismatch, result.HTTPStatus, result.ResponseBytes), nil
	}

	oc, err := applyDetectedBook(ctx, deps, j, DetectedBook{
		ASIN:        kindleASIN,
		Title:       info.Title,
		KindlePrice: info.CurrentPrice,
		ReleaseDate: info.ReleaseDate,
		PaperPrice:  paperBook.CurrentPrice,
		PaperURL:    paperBook.URL,
	})
	// applyDetectedBook は Amazon 未アクセスのため計測値を持たない。詳細jobの取得計測値を反映する。
	oc.HTTPStatus = result.HTTPStatus
	oc.ResponseBytes = result.ResponseBytes
	return oc, err
}

func notFoundType(c Category) string {
	if c == CategoryPermanentClientError {
		return "permanent_client_error"
	}
	return "not_found"
}

// applyDetectedBook は SPECIFICATION.md 14.4/7.5 の順序と reconcile で候補を保存し paper_books を削除する。
func applyDetectedBook(ctx context.Context, deps Dependencies, j job.Job, d DetectedBook) (execution.Outcome, error) {
	now := deps.Clock()
	known, err := deps.KnownStateQuerier.KnownState(ctx, d.ASIN, j.Target.SourceASIN)
	if err != nil {
		return execution.Errored(errorTypeKnownState, 0, 0), fmt.Errorf("load known state %s: %w", d.ASIN, err)
	}

	// SPEC 7.5: 紙書籍targetが削除済みでKindle候補も保存されていない場合は手動削除として新規保存しない。
	if !known.PaperBookExists && !known.NotifiedExists && !known.UpcomingExists && !known.UnprocessedExists {
		return execution.Terminal(errorTypeManualDelete, 0, 0), nil
	}

	b := toBook(d, now, deps.Config.PartnerTag)
	// SPEC 14.4 step2: unprocessed に候補がない場合は notified/upcoming の不足側へ冪等upsert。
	// 通知済み/既知であっても補完を省略しない（7.5）。
	if !known.UnprocessedExists {
		if err := deps.NotifiedStore.Upsert(ctx, b); err != nil {
			return execution.Errored(errorTypeNotifiedUpsert, 0, 0), fmt.Errorf("upsert notified for %s: %w", d.ASIN, err)
		}
		if err := deps.UpcomingStore.Upsert(ctx, b); err != nil {
			return execution.Errored(errorTypeUpcomingUpsert, 0, 0), fmt.Errorf("upsert upcoming for %s: %w", d.ASIN, err)
		}
	}

	// SPEC 14.4 step4: paper_books から紙書籍ASINを削除する（存在する場合のみ）。
	if known.PaperBookExists {
		if err := deps.PaperBooksStore.Delete(ctx, j.Target.SourceASIN); err != nil {
			return execution.Errored(errorTypePaperDelete, 0, 0), fmt.Errorf("delete paper book %s: %w", j.Target.SourceASIN, err)
		}
	}

	// SPEC 14.4 step5: Paper-to-Kindle 用 gist_update を決定的 job_id で投入する。
	// Gist は paper_books 全体から再生成するため paper_books 削除後に投入し、
	// upsert 失敗の再実行でも決定的 job_id で欠損・重複しない（7.5）。
	if err := deps.Enqueuer.Enqueue(ctx, buildPaperToKindleGistJob(j, gistStagePaperDelete, j.Target.SourceASIN)); err != nil {
		return execution.Errored(errorTypeEnqueueFailed, 0, 0), fmt.Errorf("enqueue paper-to-kindle gist: %w", err)
	}

	// SPEC 14.4 step6: 処理開始時に3リストすべてで未知だった場合だけ best-effort 通知する。
	// 通知失敗は Notifier adapter が notification_error（SPECIFICATION.md 18.3）で記録する。
	if !known.NotifiedExists && !known.UpcomingExists && !known.UnprocessedExists {
		_ = deps.Notifier.Notify(ctx, formatPaperToKindleMessage(d, deps.Config.PartnerTag))
	}
	return execution.Completed(0, 0), nil
}

func buildDetailJob(j job.Job, kindleASIN string) job.Job {
	return job.Job{
		Version:     job.Version,
		JobID:       scheduling.JobID(string(job.KindPaperToKindleDetail), j.CycleID, kindleASIN),
		Kind:        job.KindPaperToKindleDetail,
		CheckType:   j.CheckType,
		CycleID:     j.CycleID,
		ScheduledAt: j.ScheduledAt,
		Target:      job.Target{ASIN: kindleASIN, SourceASIN: j.Target.ASIN},
	}
}

// buildPaperToKindleGistJob は Paper-to-Kindle 用 gist_update ジョブを生成する。
// job_id は gist_type + stage + paperASIN で決定的。Gist updater は S3 全体を再生成するため
// Target.GistType は paper_to_kindle のまま変えない（job schema 互換、SPECIFICATION.md 7.2/15）。
// stage+paperASIN により同一 cycle の異なる S3 状態変更が SQS FIFO 5分 dedup で消えず、
// 同一状態変更の再試行は同一 job_id で冪等になる。
func buildPaperToKindleGistJob(j job.Job, stage, paperASIN string) job.Job {
	return job.Job{
		Version:     job.Version,
		JobID:       scheduling.JobID(string(job.KindGistUpdate), j.CycleID, gistPaperID+":"+stage+":"+paperASIN),
		Kind:        job.KindGistUpdate,
		CheckType:   j.CheckType,
		CycleID:     j.CycleID,
		ScheduledAt: j.ScheduledAt,
		Target:      job.Target{GistType: gistPaperID},
	}
}

func toBook(d DetectedBook, now time.Time, partnerTag string) book.KindleBook {
	return book.KindleBook{
		ASIN:         d.ASIN,
		Title:        d.Title,
		URL:          kindleURL(d.ASIN, partnerTag),
		ReleaseDate:  d.ReleaseDate,
		CurrentPrice: d.KindlePrice,
		MaxPrice:     d.KindlePrice,
		CreatedAt:    now,
	}
}

func formatPaperToKindleMessage(d DetectedBook, partnerTag string) string {
	paperLine := "📕 紙書籍(価格取得不可): " + d.PaperURL
	if d.PaperPrice.Valid() {
		paperLine = fmt.Sprintf("📕 紙書籍(%d円): %s", int(d.PaperPrice.Yen()), d.PaperURL)
	}
	return fmt.Sprintf("📚 新刊予定があります: %s\n%s\n📱 電子書籍(%d円): %s",
		d.Title, paperLine, int(d.KindlePrice.Yen()), kindleURL(d.ASIN, partnerTag))
}

// partnerTag が空でなければ Affiliate Tag を付ける（SPECIFICATION.md 9.2）。
func kindleURL(asin, partnerTag string) string {
	u := paperURLBase + asin
	if partnerTag != "" {
		u += "?tag=" + partnerTag
	}
	return u
}

// IsSameReleaseDayJST は2つの時刻が同じJST暦日かを返す。
// 2つの時刻が同じJST暦日か（SPECIFICATION.md 14.3）。
func IsSameReleaseDayJST(a, b time.Time) bool {
	return a.In(jst).Format("2006-01-02") == b.In(jst).Format("2006-01-02")
}
