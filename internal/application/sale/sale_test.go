package sale

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shinderuman/kindle-automation/internal/application/execution"
	"github.com/shinderuman/kindle-automation/internal/domain/book"
	domainsale "github.com/shinderuman/kindle-automation/internal/domain/sale"
	"github.com/shinderuman/kindle-automation/internal/job"
)

var fixedClock = func() time.Time { return time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC) }

func baseThresholds() domainsale.Thresholds {
	return domainsale.Thresholds{SaleThreshold: 151, PointPercent: 20, PriceChangeAmount: 100}
}

func saleCheckJob(asin string) job.Job {
	return job.Job{
		Version:   job.Version,
		JobID:     "sale_check:c:B0",
		Kind:      job.KindSaleCheck,
		CheckType: job.CheckSale,
		CycleID:   "sale:c",
		Target:    job.Target{ASIN: asin},
	}
}

type fakeFetcher struct {
	result FetchResult
	err    error
	calls  int
}

func (f *fakeFetcher) FetchProduct(_ context.Context, _ string) (FetchResult, error) {
	f.calls++
	return f.result, f.err
}

type fakeStore struct {
	oldBook book.KindleBook
	applied bool
	err     error
	updated book.KindleBook
}

func (s *fakeStore) UpdateOneBook(_ context.Context, _, _ string, update func(book.KindleBook) book.KindleBook) (bool, error) {
	s.updated = update(s.oldBook)
	return s.applied, s.err
}

type fakeNotifier struct {
	called  bool
	message string
	err     error
}

func (n *fakeNotifier) Notify(_ context.Context, message string) error {
	n.called = true
	n.message = message
	return n.err
}

type fakeEnqueuer struct {
	jobs []job.Job
}

func (e *fakeEnqueuer) Enqueue(_ context.Context, j job.Job) error {
	e.jobs = append(e.jobs, j)
	return nil
}

func okInfo(asin string, price float64) ProductInfo {
	return ProductInfo{
		ASIN: asin, Title: "タイトル",
		HasKindleSwatch: true, CurrentPrice: book.NewPrice(price),
	}
}

func existingBook(asin string, price float64) book.KindleBook {
	return book.KindleBook{
		ASIN: asin, Title: "タイトル", URL: "https://u",
		CurrentPrice: book.NewPrice(price), MaxPrice: book.NewPrice(price),
	}
}

// deps は標準的な sale.Dependencies を組み立てる。
func deps(fetcher *fakeFetcher, store *fakeStore, notifier *fakeNotifier) Dependencies {
	return Dependencies{
		Fetcher: fetcher, Store: store, Notifier: notifier,
		Config: Config{Thresholds: baseThresholds()}, Clock: fixedClock,
	}
}

func TestHandleSaleCheck_OK_FetchesAmazonOnce(t *testing.T) {
	fetcher := &fakeFetcher{result: FetchResult{Category: CategoryOK, Info: okInfo("B0FX3X569X", 759)}}
	store := &fakeStore{oldBook: existingBook("B0FX3X569X", 759), applied: true}
	d := deps(fetcher, store, &fakeNotifier{})

	oc, err := HandleSaleCheck(context.Background(), d, saleCheckJob("B0FX3X569X"))
	if err != nil {
		t.Fatalf("HandleSaleCheck: %v", err)
	}
	if fetcher.calls != 1 {
		t.Errorf("Amazon fetch calls = %d, want 1", fetcher.calls)
	}
	if oc.Result != execution.ResultCompleted || oc.ErrorType != "" {
		t.Errorf("outcome = %+v, want completed/empty error_type", oc)
	}
}

func TestHandleSaleCheck_RetryableCategoryReturnsError(t *testing.T) {
	fetcher := &fakeFetcher{result: FetchResult{Category: CategoryRetryable}}
	d := deps(fetcher, &fakeStore{}, &fakeNotifier{})

	oc, err := HandleSaleCheck(context.Background(), d, saleCheckJob("B0FX3X569X"))
	if err == nil {
		t.Fatal("retryable category must return error")
	}
	var re *ErrRetryableFetch
	if !errors.As(err, &re) {
		t.Errorf("error must be ErrRetryableFetch, got %T", err)
	}
	if oc.Result != execution.ResultError || oc.ErrorType != errorTypeFetchRetryable {
		t.Errorf("outcome = %+v, want result=error error_type=%s", oc, errorTypeFetchRetryable)
	}
}

func TestHandleSaleCheck_FetchErrorReturnsError(t *testing.T) {
	fetcher := &fakeFetcher{err: errors.New("network down")}
	d := deps(fetcher, &fakeStore{}, &fakeNotifier{})

	oc, err := HandleSaleCheck(context.Background(), d, saleCheckJob("B0FX3X569X"))
	if err == nil {
		t.Fatal("fetch error must return error")
	}
	if oc.Result != execution.ResultError || oc.ErrorType != errorTypeFetchError {
		t.Errorf("outcome = %+v, want result=error error_type=%s", oc, errorTypeFetchError)
	}
}

func TestHandleSaleCheck_NotFoundIsTerminalNoNotify(t *testing.T) {
	fetcher := &fakeFetcher{result: FetchResult{Category: CategoryNotFound}}
	store := &fakeStore{applied: true}
	notifier := &fakeNotifier{}
	d := deps(fetcher, store, notifier)

	oc, err := HandleSaleCheck(context.Background(), d, saleCheckJob("B0FX3X569X"))
	if err != nil {
		t.Fatalf("terminal must not return error: %v", err)
	}
	if notifier.called {
		t.Errorf("terminal result must not notify")
	}
	if oc.Result != execution.ResultTerminal || oc.ErrorType != errorTypeNotFound {
		t.Errorf("outcome = %+v, want result=terminal error_type=%s", oc, errorTypeNotFound)
	}
}

func TestHandleSaleCheck_AsinMismatchIsTerminal(t *testing.T) {
	info := okInfo("B0DIFFERNT1", 759)
	fetcher := &fakeFetcher{result: FetchResult{Category: CategoryOK, Info: info}}
	d := deps(fetcher, &fakeStore{applied: true}, &fakeNotifier{})

	oc, err := HandleSaleCheck(context.Background(), d, saleCheckJob("B0FX3X569X"))
	if err != nil {
		t.Fatalf("asin_mismatch must be terminal: %v", err)
	}
	if oc.Result != execution.ResultTerminal || oc.ErrorType != errorTypeAsinMismatch {
		t.Errorf("outcome = %+v, want result=terminal error_type=%s", oc, errorTypeAsinMismatch)
	}
}

func TestHandleSaleCheck_NotKindleIsTerminal(t *testing.T) {
	info := okInfo("B0FX3X569X", 759)
	info.HasKindleSwatch = false
	fetcher := &fakeFetcher{result: FetchResult{Category: CategoryOK, Info: info}}
	d := deps(fetcher, &fakeStore{applied: true}, &fakeNotifier{})

	oc, err := HandleSaleCheck(context.Background(), d, saleCheckJob("B0FX3X569X"))
	if err != nil {
		t.Fatalf("not_kindle must be terminal: %v", err)
	}
	if oc.Result != execution.ResultTerminal || oc.ErrorType != errorTypeNotKindle {
		t.Errorf("outcome = %+v, want result=terminal error_type=%s", oc, errorTypeNotKindle)
	}
}

func TestHandleSaleCheck_PriceUnavailableIsRetryable(t *testing.T) {
	info := okInfo("B0FX3X569X", 0) // CurrentPrice Invalid
	fetcher := &fakeFetcher{result: FetchResult{Category: CategoryOK, Info: info}}
	d := deps(fetcher, &fakeStore{applied: true}, &fakeNotifier{})

	oc, err := HandleSaleCheck(context.Background(), d, saleCheckJob("B0FX3X569X"))
	if err == nil {
		t.Fatal("price unavailable must be retryable error")
	}
	if oc.Result != execution.ResultError || oc.ErrorType != errorTypePriceUnavailable {
		t.Errorf("outcome = %+v, want result=error error_type=%s", oc, errorTypePriceUnavailable)
	}
}

func TestHandleSaleCheck_SaleConditionNotifies(t *testing.T) {
	info := okInfo("B0FX3X569X", 600)
	info.Points = 200
	fetcher := &fakeFetcher{result: FetchResult{Category: CategoryOK, Info: info}}
	store := &fakeStore{oldBook: existingBook("B0FX3X569X", 900), applied: true}
	notifier := &fakeNotifier{}
	d := deps(fetcher, store, notifier)

	oc, err := HandleSaleCheck(context.Background(), d, saleCheckJob("B0FX3X569X"))
	if err != nil {
		t.Fatalf("HandleSaleCheck: %v", err)
	}
	if !notifier.called {
		t.Fatal("sale condition must notify")
	}
	if !strings.Contains(notifier.message, "セール情報") {
		t.Errorf("not sale message: %s", notifier.message)
	}
	if strings.Contains(notifier.message, "値上がり") {
		t.Errorf("price change must be suppressed when sale condition met: %s", notifier.message)
	}
	if oc.Result != execution.ResultCompleted {
		t.Errorf("outcome result = %q, want completed", oc.Result)
	}
}

func TestHandleSaleCheck_PriceChangeNotifiesWhenNoSale(t *testing.T) {
	info := okInfo("B0FX3X569X", 800)
	fetcher := &fakeFetcher{result: FetchResult{Category: CategoryOK, Info: info}}
	store := &fakeStore{oldBook: existingBook("B0FX3X569X", 600), applied: true}
	notifier := &fakeNotifier{}
	d := deps(fetcher, store, notifier)

	if _, err := HandleSaleCheck(context.Background(), d, saleCheckJob("B0FX3X569X")); err != nil {
		t.Fatalf("HandleSaleCheck: %v", err)
	}
	if !notifier.called {
		t.Fatal("price change must notify")
	}
	if !strings.Contains(notifier.message, "値上がり") {
		t.Errorf("not price-up message: %s", notifier.message)
	}
}

func TestHandleSaleCheck_TargetRemovedIsTerminal(t *testing.T) {
	fetcher := &fakeFetcher{result: FetchResult{Category: CategoryOK, Info: okInfo("B0FX3X569X", 759)}}
	store := &fakeStore{applied: false} // 手動削除
	notifier := &fakeNotifier{}
	d := deps(fetcher, store, notifier)

	oc, err := HandleSaleCheck(context.Background(), d, saleCheckJob("B0FX3X569X"))
	if err != nil {
		t.Fatalf("target_removed must be terminal: %v", err)
	}
	if notifier.called {
		t.Errorf("target_removed must not notify")
	}
	if oc.Result != execution.ResultTerminal || oc.ErrorType != errorTypeTargetRemoved {
		t.Errorf("outcome = %+v, want result=terminal error_type=%s", oc, errorTypeTargetRemoved)
	}
}

func TestHandleSaleCheck_StoreErrorSkipsNotify(t *testing.T) {
	fetcher := &fakeFetcher{result: FetchResult{Category: CategoryOK, Info: okInfo("B0FX3X569X", 759)}}
	store := &fakeStore{oldBook: existingBook("B0FX3X569X", 600), applied: true, err: errors.New("s3 conflict over retries")}
	notifier := &fakeNotifier{}
	d := deps(fetcher, store, notifier)

	oc, err := HandleSaleCheck(context.Background(), d, saleCheckJob("B0FX3X569X"))
	if err == nil {
		t.Fatal("store error must propagate")
	}
	if notifier.called {
		t.Errorf("must not notify when S3 save failed")
	}
	if oc.Result != execution.ResultError || oc.ErrorType != errorTypeStoreUpdate {
		t.Errorf("outcome = %+v, want result=error error_type=%s", oc, errorTypeStoreUpdate)
	}
}

// 通知失敗は Notifier adapter が notification_error（SPECIFICATION.md 18.3）で記録するため、
// sale ユースケースでは job を completed のままにし、保存済み状態を巻き戻さない（SPEC 17.1）。
func TestHandleSaleCheck_NotifyFailureDoesNotRollback(t *testing.T) {
	info := okInfo("B0FX3X569X", 600)
	info.Points = 200
	fetcher := &fakeFetcher{result: FetchResult{Category: CategoryOK, Info: info}}
	store := &fakeStore{oldBook: existingBook("B0FX3X569X", 900), applied: true}
	notifier := &fakeNotifier{err: errors.New("slack down")}
	d := deps(fetcher, store, notifier)

	oc, err := HandleSaleCheck(context.Background(), d, saleCheckJob("B0FX3X569X"))
	if err != nil {
		t.Fatalf("notify failure must not fail the job: %v", err)
	}
	if oc.Result != execution.ResultCompleted {
		t.Errorf("outcome result = %q, want completed", oc.Result)
	}
}

func TestHandleSaleCheck_UpdatesPriceHistory(t *testing.T) {
	fetcher := &fakeFetcher{result: FetchResult{Category: CategoryOK, Info: okInfo("B0FX3X569X", 600)}}
	store := &fakeStore{oldBook: existingBook("B0FX3X569X", 900), applied: true}
	d := deps(fetcher, store, &fakeNotifier{})

	if _, err := HandleSaleCheck(context.Background(), d, saleCheckJob("B0FX3X569X")); err != nil {
		t.Fatalf("HandleSaleCheck: %v", err)
	}
	if !store.updated.CurrentPrice.Valid() || store.updated.CurrentPrice.Yen() != 600 {
		t.Errorf("CurrentPrice not updated: %+v", store.updated.CurrentPrice)
	}
	if !store.updated.MaxPrice.Valid() || store.updated.MaxPrice.Yen() != 900 {
		t.Errorf("MaxPrice must keep 900: %+v", store.updated.MaxPrice)
	}
}

func TestHandleSaleFinalize_EnqueuesSaleGistUpdate(t *testing.T) {
	enq := &fakeEnqueuer{}
	d := Dependencies{Enqueuer: enq}
	j := job.Job{Version: job.Version, JobID: "f", Kind: job.KindSaleFinalize, CheckType: job.CheckSale, CycleID: "sale:2026-07-23T00:00:00Z", Target: job.Target{}}

	if _, err := HandleSaleFinalize(context.Background(), d, j); err != nil {
		t.Fatalf("HandleSaleFinalize: %v", err)
	}
	if len(enq.jobs) != 1 {
		t.Fatalf("enqueued jobs = %d, want 1", len(enq.jobs))
	}
	gist := enq.jobs[0]
	if gist.Kind != job.KindGistUpdate {
		t.Errorf("kind = %v, want gist_update", gist.Kind)
	}
	if gist.Target.GistType != "sale" {
		t.Errorf("gist_type = %q, want sale", gist.Target.GistType)
	}
	if got := job.MessageGroup(gist.Kind); got != "external-updates" {
		t.Errorf("MessageGroup = %q, want external-updates", got)
	}
	if job.AmazonRequests(gist.Kind) != 0 {
		t.Errorf("gist_update must not access Amazon")
	}
}
