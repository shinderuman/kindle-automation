// Package sale は check-worker における sale_check と sale_finalize のユースケースを実装する。
// sale_check は Amazon 商品ページへ最大1回アクセスし、価格履歴更新と best-effort 通知を行う。
// sale_finalize は Sale 用 gist_update を1件投入する（SPECIFICATION.md 12, 17.1, AGENTS.md 4/9）。
//
// 本 package は Amazon adapter（HTTP/goquery/selector）の都合へ依存しない。
// ProductFetcher が返す ProductInfo と Category は sale ユースケースが必要とする最小の型で、
// Amazon adapter 側の DTO から cmd/worker 層で変換して注入する。
package sale

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/shinderuman/kindle-automation/internal/application/execution"
	"github.com/shinderuman/kindle-automation/internal/domain/book"
	domainsale "github.com/shinderuman/kindle-automation/internal/domain/sale"
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
	errorTypeNotKindle        = "not_kindle"
	errorTypePriceUnavailable = "price_unavailable"
	errorTypeStoreUpdate      = "store_update"
	errorTypeTargetRemoved    = "target_removed"
	errorTypeEnqueueFailed    = "enqueue_failed"
)

// Category は Amazon 商品ページ1回の取得結果の分類（SPECIFICATION.md 11.3）。
// HTTP/goquery の都合を含まず、retryable/terminal の業務分類のみを表す。
type Category int

const (
	// CategoryOK は必須構造あり。ページ内 ASIN/Kindle 種別/価格の検証は呼び出し側で行う。
	CategoryOK Category = iota
	// CategoryNotFound は 404 または明示的な商品不存在。terminal。
	CategoryNotFound
	// CategoryPermanentClientError は 400 等の恒久的 4xx。terminal。
	CategoryPermanentClientError
	// CategoryRetryable は 403/429/5xx/CAPTCHA/構造欠落/解析失敗。再試行する。
	CategoryRetryable
)

// ProductInfo は sale ユースケースが Amazon 商品ページから必要とする取得結果。
// adapter 側の DTO（goquery 抽出や HTTP status 由来）を含まず、業務に必要な最小値だけ持つ。
type ProductInfo struct {
	ASIN            string
	Title           string
	CurrentPrice    book.Price
	Points          int
	Coupon          bool
	CouponText      string
	HasKindleSwatch bool
}

// FetchResult は Amazon 商品ページ1回の取得結果。
// HTTPStatus/ResponseBytes は構造化ログ（SPECIFICATION.md 18.1）へ伝播するHTTP計測値。未送信時は0。
type FetchResult struct {
	Category      Category
	Info          ProductInfo
	HTTPStatus    int
	ResponseBytes int
}

// ProductFetcher は Amazon 商品ページを1回取得し sale 用の結果へ変換して返す。1起動で最大1回しか呼ばない。
type ProductFetcher interface {
	FetchProduct(ctx context.Context, asin string) (FetchResult, error)
}

// BookStore は対象レコードの条件付き更新。手動削除時は applied=false。
type BookStore interface {
	UpdateOneBook(ctx context.Context, key, asin string, update func(book.KindleBook) book.KindleBook) (bool, error)
}

// Notifier は商品通知を送る。best-effort で、失敗しても保存済み状態は巻き戻さない。
// 通知失敗は Notifier adapter が notification_error（SPECIFICATION.md 18.3）で記録するため、
// 本ユースケースでは通知結果をジョブ結果へ反映しない。
type Notifier interface {
	Notify(ctx context.Context, message string) error
}

// Enqueuer は後続 job を投入する。
type Enqueuer interface {
	Enqueue(ctx context.Context, job job.Job) error
}

// Config は sale ユースケースの設定。
type Config struct {
	UnprocessedKey string
	Thresholds     domainsale.Thresholds
}

// Dependencies は sale ユースケースの外部依存。
type Dependencies struct {
	Fetcher  ProductFetcher
	Store    BookStore
	Notifier Notifier
	Enqueuer Enqueuer
	Config   Config
	Clock    func() time.Time
}

// ErrRetryableFetch は Amazon 取得の再試行可能エラー。Lambda error として SQS へ再配信させる。
type ErrRetryableFetch struct{ ASIN string }

func (e *ErrRetryableFetch) Error() string { return "retryable fetch for " + e.ASIN }

// HandleSaleCheck は sale_check ジョブを処理する（SPECIFICATION.md 12.6）。
// 処理結果とHTTP計測値を Outcome で返し、retryable は原因 error も返す。
// composition root が Outcome へ共通ログfieldを合成して job_completed/job_terminal/job_error を出す。
func HandleSaleCheck(ctx context.Context, deps Dependencies, j job.Job) (execution.Outcome, error) {
	asin := j.Target.ASIN
	result, err := deps.Fetcher.FetchProduct(ctx, asin)
	if err != nil {
		return execution.Errored(errorTypeFetchError, 0, 0), fmt.Errorf("fetch product %s: %w", asin, err)
	}
	switch result.Category {
	case CategoryRetryable:
		return execution.Errored(errorTypeFetchRetryable, result.HTTPStatus, result.ResponseBytes), &ErrRetryableFetch{ASIN: asin}
	case CategoryNotFound:
		// terminal。S3 を更新せず対象はリストへ残す。
		return execution.Terminal(errorTypeNotFound, result.HTTPStatus, result.ResponseBytes), nil
	case CategoryPermanentClientError:
		return execution.Terminal(errorTypePermanentClient, result.HTTPStatus, result.ResponseBytes), nil
	case CategoryOK:
	default:
		return execution.Errored(errorTypeUnknownCategory, result.HTTPStatus, result.ResponseBytes),
			fmt.Errorf("unknown fetch category %v for %s", result.Category, asin)
	}

	info := result.Info
	if info.ASIN != "" && info.ASIN != asin {
		// asin_mismatch terminal。対象はリストへ残す。
		return execution.Terminal(errorTypeAsinMismatch, result.HTTPStatus, result.ResponseBytes), nil
	}
	if !info.HasKindleSwatch {
		// not_kindle terminal。対象はリストへ残す。
		return execution.Terminal(errorTypeNotKindle, result.HTTPStatus, result.ResponseBytes), nil
	}
	if !info.CurrentPrice.Valid() {
		// 価格解析失敗は retryable。
		return execution.Errored(errorTypePriceUnavailable, result.HTTPStatus, result.ResponseBytes),
			fmt.Errorf("kindle price not available for %s", asin)
	}

	var message string
	var shouldNotify bool
	applied, err := deps.Store.UpdateOneBook(ctx, deps.Config.UnprocessedKey, asin, func(old book.KindleBook) book.KindleBook {
		newBook := book.UpdatePriceHistory(old, info.CurrentPrice, deps.Clock())
		conditions := domainsale.Evaluate(toSaleInput(newBook, info), deps.Config.Thresholds)
		if conditions.Any() {
			// セール成立時は価格変動通知を重ねない（SPECIFICATION.md 12.5）。
			message = formatSaleMessage(newBook, conditions, info)
			shouldNotify = true
			return newBook
		}
		kind, diff := domainsale.EvaluatePriceChange(old.CurrentPrice.Yen(), info.CurrentPrice.Yen(), deps.Config.Thresholds.PriceChangeAmount)
		if kind != domainsale.NoChange {
			message = formatPriceChangeMessage(newBook, kind, diff, old.CurrentPrice.Yen())
			shouldNotify = true
		}
		return newBook
	})
	if err != nil {
		return execution.Errored(errorTypeStoreUpdate, result.HTTPStatus, result.ResponseBytes),
			fmt.Errorf("update book %s: %w", asin, err)
	}
	if !applied {
		// target_removed terminal。再追加しない。
		return execution.Terminal(errorTypeTargetRemoved, result.HTTPStatus, result.ResponseBytes), nil
	}

	// S3 保存成功後のみ通知する。通知は best-effort で保存済み価格を巻き戻さない（SPECIFICATION.md 17.1）。
	// 通知失敗は Notifier adapter が notification_error（SPECIFICATION.md 18.3）で記録するため
	// ジョブ結果は completed のままにする。
	if shouldNotify {
		_ = deps.Notifier.Notify(ctx, message)
	}
	return execution.Completed(result.HTTPStatus, result.ResponseBytes), nil
}

// HandleSaleFinalize は Sale 用 gist_update を1件投入する。1周につき1回呼ばれる。
func HandleSaleFinalize(ctx context.Context, deps Dependencies, j job.Job) (execution.Outcome, error) {
	if err := deps.Enqueuer.Enqueue(ctx, buildGistJob(j, "sale")); err != nil {
		return execution.Errored(errorTypeEnqueueFailed, 0, 0), fmt.Errorf("enqueue sale gist: %w", err)
	}
	return execution.Completed(0, 0), nil
}

// buildGistJob は Sale 用 gist_update ジョブを生成する。job_id は決定的。
func buildGistJob(j job.Job, gistType string) job.Job {
	return job.Job{
		Version:     job.Version,
		JobID:       scheduling.JobID(string(job.KindGistUpdate), j.CycleID, gistType),
		Kind:        job.KindGistUpdate,
		CheckType:   j.CheckType,
		CycleID:     j.CycleID,
		ScheduledAt: j.ScheduledAt,
		Target:      job.Target{GistType: gistType},
	}
}

func toSaleInput(b book.KindleBook, info ProductInfo) domainsale.Input {
	return domainsale.Input{
		CurrentPrice: b.CurrentPrice.Yen(),
		MaxPrice:     b.MaxPrice.Yen(),
		Points:       info.Points,
		Coupon:       info.Coupon,
	}
}

func formatSaleMessage(b book.KindleBook, conditions domainsale.Conditions, info ProductInfo) string {
	lines := domainsale.NotificationLines(toSaleInput(b, info), conditions, info.CouponText)
	return fmt.Sprintf("📚 セール情報: %s\n条件達成: %s\n%s", b.Title, strings.Join(lines, " "), b.URL)
}

func formatPriceChangeMessage(b book.KindleBook, kind domainsale.PriceChangeKind, diff int, oldYen float64) string {
	prefix := "📈 プチ値上がり情報:"
	if kind == domainsale.PriceDown {
		prefix = "📉 プチ値下がり情報:"
	}
	return fmt.Sprintf("%s %s\n価格変動: %.0f円 → %.0f円 (%d円)\n%s", prefix, b.Title, oldYen, b.CurrentPrice.Yen(), diff, b.URL)
}
