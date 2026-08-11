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
	"github.com/shinderuman/kindle-automation/internal/domain/scheduling"
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
	calls   int
}

func (s *fakeStore) UpdateOneBook(_ context.Context, _, _ string, update func(book.KindleBook) book.KindleBook) (bool, error) {
	s.calls++
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
	store := &fakeStore{applied: true}
	d := deps(fetcher, store, &fakeNotifier{})

	oc, err := HandleSaleCheck(context.Background(), d, saleCheckJob("B0FX3X569X"))
	if err == nil {
		t.Fatal("price unavailable must be retryable error")
	}
	if oc.Result != execution.ResultError || oc.ErrorType != errorTypePriceUnavailable {
		t.Errorf("outcome = %+v, want result=error error_type=%s", oc, errorTypePriceUnavailable)
	}
	// 価格不明は保存前に弾き、0円を保存しない（SPEC 11.2/12.6）。
	if store.calls != 0 {
		t.Errorf("store calls = %d, want 0 (must not save when price unavailable)", store.calls)
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

// セール不成立かつ価格変動も閾値未満のとき、通知せずとも価格履歴を正しく更新する（SPEC 12.3/12.5/12.6）。
// CurrentPrice は今回価格へ更新し、MaxPrice は過去最高を維持する。
func TestHandleSaleCheck_SubThresholdChangeUpdatesStateWithoutNotify(t *testing.T) {
	info := okInfo("B0FX3X569X", 870) // 900→870 は差30で PriceChangeAmount(100) 未満
	fetcher := &fakeFetcher{result: FetchResult{Category: CategoryOK, Info: info}}
	store := &fakeStore{oldBook: existingBook("B0FX3X569X", 900), applied: true}
	notifier := &fakeNotifier{}
	d := deps(fetcher, store, notifier)

	oc, err := HandleSaleCheck(context.Background(), d, saleCheckJob("B0FX3X569X"))
	if err != nil {
		t.Fatalf("HandleSaleCheck: %v", err)
	}
	if notifier.called {
		t.Errorf("must not notify when no sale and sub-threshold change")
	}
	if !store.updated.CurrentPrice.Valid() || store.updated.CurrentPrice.Yen() != 870 {
		t.Errorf("CurrentPrice = %+v, want 870 (updated even without notify)", store.updated.CurrentPrice)
	}
	if !store.updated.MaxPrice.Valid() || store.updated.MaxPrice.Yen() != 900 {
		t.Errorf("MaxPrice = %+v, want 900 (kept on sub-threshold drop)", store.updated.MaxPrice)
	}
	if oc.Result != execution.ResultCompleted {
		t.Errorf("outcome result = %q, want completed", oc.Result)
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

// TestBuildGistJob_DiscriminatorIsDeterministic は Sale 用 gist job_id の決定性を検証する
// （SPECIFICATION.md 7.2/15）。sale_finalize は1周1回で Gist は S3 全体から再生成するため
// discriminator は cycleID+gist_type で十分（最終状態が常に勝つ）。同一 cycle の再試行は同一 job_id で
// 冪等、異なる cycle は別 job_id となる。SQS FIFO の MessageDeduplicationId は job_id の SHA-256。
func TestBuildGistJob_DiscriminatorIsDeterministic(t *testing.T) {
	j := job.Job{Version: job.Version, JobID: "f", Kind: job.KindSaleFinalize, CheckType: job.CheckSale, CycleID: "sale:2026-07-23T00:00:00Z"}
	first := buildGistJob(j, "sale")
	// 同一 cycle の再試行（SQS 再配信）は同一 job_id で冪等。
	if buildGistJob(j, "sale").JobID != first.JobID {
		t.Errorf("sale gist job_id is not deterministic")
	}
	if scheduling.DedupID(buildGistJob(j, "sale").JobID) != scheduling.DedupID(first.JobID) {
		t.Errorf("sale gist dedup id is not deterministic")
	}
	// 異なる cycle は別 job_id（5分 dedup で前周に吸われない）。
	other := job.Job{Version: job.Version, JobID: "f2", Kind: job.KindSaleFinalize, CheckType: job.CheckSale, CycleID: "sale:2026-07-24T00:00:00Z"}
	if buildGistJob(other, "sale").JobID == first.JobID {
		t.Errorf("different cycle must differ: %s", first.JobID)
	}
}

type failingEnqueuer struct{ err error }

func (e *failingEnqueuer) Enqueue(_ context.Context, _ job.Job) error { return e.err }

// statefulStore は更新結果を自身へ反映し、重複配信時の状態遷移を検証する。
type statefulStore struct {
	book    book.KindleBook
	applied bool
	calls   int
}

func (s *statefulStore) UpdateOneBook(_ context.Context, _, _ string, update func(book.KindleBook) book.KindleBook) (bool, error) {
	s.calls++
	s.book = update(s.book)
	return s.applied, nil
}

func TestHandleSaleFinalize_EnqueueFailureReturnsError(t *testing.T) {
	d := Dependencies{Enqueuer: &failingEnqueuer{err: errors.New("sqs throttled")}}
	j := job.Job{Version: job.Version, JobID: "f", Kind: job.KindSaleFinalize, CheckType: job.CheckSale, CycleID: "sale:c"}

	oc, err := HandleSaleFinalize(context.Background(), d, j)
	if err == nil {
		t.Fatal("gist enqueue failure must return error")
	}
	if oc.Result != execution.ResultError || oc.ErrorType != errorTypeEnqueueFailed {
		t.Errorf("outcome = %+v, want result=error error_type=%s", oc, errorTypeEnqueueFailed)
	}
}

func TestHandleSaleCheck_PriceDownNotifiesWhenNoSale(t *testing.T) {
	info := okInfo("B0FX3X569X", 700)
	fetcher := &fakeFetcher{result: FetchResult{Category: CategoryOK, Info: info}}
	// old Max=800/Current=800, current=700: 価格差100<151で非セール、変動-100で値下がり通知。
	store := &fakeStore{oldBook: existingBook("B0FX3X569X", 800), applied: true}
	notifier := &fakeNotifier{}
	d := deps(fetcher, store, notifier)

	if _, err := HandleSaleCheck(context.Background(), d, saleCheckJob("B0FX3X569X")); err != nil {
		t.Fatalf("HandleSaleCheck: %v", err)
	}
	if !notifier.called {
		t.Fatal("price down must notify")
	}
	if !strings.Contains(notifier.message, "値下がり") {
		t.Errorf("not price-down message: %s", notifier.message)
	}
	if store.updated.MaxPrice.Yen() != 800 {
		t.Errorf("MaxPrice = %v, want 800 (kept on price down)", store.updated.MaxPrice)
	}
}

func TestHandleSaleCheck_PriceUpUpdatesMaxPriceWhenNoSale(t *testing.T) {
	info := okInfo("B0FX3X569X", 800)
	fetcher := &fakeFetcher{result: FetchResult{Category: CategoryOK, Info: info}}
	// old Max=600/Current=600, current=800: 価格差負で非セール、変動+200で値上がり、Maxは800へ更新。
	store := &fakeStore{oldBook: existingBook("B0FX3X569X", 600), applied: true}
	d := deps(fetcher, store, &fakeNotifier{})

	if _, err := HandleSaleCheck(context.Background(), d, saleCheckJob("B0FX3X569X")); err != nil {
		t.Fatalf("HandleSaleCheck: %v", err)
	}
	if store.updated.CurrentPrice.Yen() != 800 {
		t.Errorf("CurrentPrice = %v, want 800", store.updated.CurrentPrice)
	}
	if store.updated.MaxPrice.Yen() != 800 {
		t.Errorf("MaxPrice = %v, want 800 (price up updates max)", store.updated.MaxPrice)
	}
}

func TestHandleSaleCheck_FirstFetchNoPriceDropButPointsCouponNotify(t *testing.T) {
	info := okInfo("B0FX3X569X", 600)
	info.Points = 200
	info.Coupon = true
	fetcher := &fakeFetcher{result: FetchResult{Category: CategoryOK, Info: info}}
	// 未取得レコード(価格ゼロ)の初回取得: 価格差セールは成立せず、ポイントとクーポンは成立する（SPEC 12.3）。
	store := &fakeStore{
		oldBook: book.KindleBook{ASIN: "B0FX3X569X", Title: "タイトル", URL: "https://u"},
		applied: true,
	}
	notifier := &fakeNotifier{}
	d := deps(fetcher, store, notifier)

	if _, err := HandleSaleCheck(context.Background(), d, saleCheckJob("B0FX3X569X")); err != nil {
		t.Fatalf("HandleSaleCheck: %v", err)
	}
	if !notifier.called {
		t.Fatal("first fetch with points/coupon must notify sale")
	}
	if !strings.Contains(notifier.message, "セール情報") {
		t.Errorf("not sale message: %s", notifier.message)
	}
	if strings.Contains(notifier.message, "最高額との価格差") {
		t.Errorf("price drop must not be in first-fetch sale message: %s", notifier.message)
	}
	if store.updated.MaxPrice.Yen() != 600 {
		t.Errorf("MaxPrice = %v, want 600 (first fetch basis)", store.updated.MaxPrice)
	}
	if !store.updated.CreatedAt.Equal(fixedClock()) {
		t.Errorf("CreatedAt = %v, want fixedClock (set on first fetch)", store.updated.CreatedAt)
	}
}

func TestHandleSaleCheck_DuplicateDeliveryIsIdempotent(t *testing.T) {
	info := okInfo("B0FX3X569X", 600)
	fetcher := &fakeFetcher{result: FetchResult{Category: CategoryOK, Info: info}}
	store := &statefulStore{book: existingBook("B0FX3X569X", 900), applied: true}
	d := Dependencies{
		Fetcher: fetcher, Store: store, Notifier: &fakeNotifier{},
		Config: Config{Thresholds: baseThresholds()}, Clock: fixedClock,
	}

	// SQSの少なくとも1回配信を前提に同じjobを2回処理しても、価格履歴は冪等に収束する（SPEC 7.4）。
	for i := 0; i < 2; i++ {
		if _, err := HandleSaleCheck(context.Background(), d, saleCheckJob("B0FX3X569X")); err != nil {
			t.Fatalf("call %d: HandleSaleCheck: %v", i, err)
		}
	}
	if store.book.CurrentPrice.Yen() != 600 {
		t.Errorf("CurrentPrice = %v, want 600 after re-delivery", store.book.CurrentPrice)
	}
	if store.book.MaxPrice.Yen() != 900 {
		t.Errorf("MaxPrice = %v, want 900 (stable, not inflated) after re-delivery", store.book.MaxPrice)
	}
	if store.calls != 2 {
		t.Errorf("store calls = %d, want 2", store.calls)
	}
}

// orderStore/orderNotifier は保存→通知の副作用順序を共有スライスへ記録する。
type orderStore struct {
	old   book.KindleBook
	order *[]string
}

func (s *orderStore) UpdateOneBook(_ context.Context, _, _ string, update func(book.KindleBook) book.KindleBook) (bool, error) {
	_ = update(s.old)
	*s.order = append(*s.order, "save")
	return true, nil
}

type orderNotifier struct{ order *[]string }

func (n *orderNotifier) Notify(_ context.Context, _ string) error {
	*n.order = append(*n.order, "notify")
	return nil
}

func TestHandleSaleCheck_SavesBeforeNotify(t *testing.T) {
	info := okInfo("B0FX3X569X", 600)
	info.Points = 200
	fetcher := &fakeFetcher{result: FetchResult{Category: CategoryOK, Info: info}}
	var order []string
	store := &orderStore{old: existingBook("B0FX3X569X", 900), order: &order}
	notifier := &orderNotifier{order: &order}
	d := Dependencies{
		Fetcher: fetcher, Store: store, Notifier: notifier,
		Config: Config{Thresholds: baseThresholds()}, Clock: fixedClock,
	}

	if _, err := HandleSaleCheck(context.Background(), d, saleCheckJob("B0FX3X569X")); err != nil {
		t.Fatalf("HandleSaleCheck: %v", err)
	}
	if len(order) != 2 || order[0] != "save" || order[1] != "notify" {
		t.Fatalf("side-effect order = %v, want [save notify]", order)
	}
}
