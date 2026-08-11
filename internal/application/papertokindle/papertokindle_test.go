package papertokindle

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shinderuman/kindle-automation/internal/application/execution"
	"github.com/shinderuman/kindle-automation/internal/domain/book"
	"github.com/shinderuman/kindle-automation/internal/domain/scheduling"
	"github.com/shinderuman/kindle-automation/internal/job"
)

var fixedClock = func() time.Time { return time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC) }
var releaseDay = time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

func checkJob(paperASIN string) job.Job {
	return job.Job{Version: job.Version, JobID: "c", Kind: job.KindPaperToKindleCheck,
		CheckType: job.CheckPaperToKindle, CycleID: "p:c", Target: job.Target{ASIN: paperASIN}}
}

func detailJob(kindleASIN, paperASIN string) job.Job {
	return job.Job{Version: job.Version, JobID: "d", Kind: job.KindPaperToKindleDetail,
		CheckType: job.CheckPaperToKindle, CycleID: "p:c",
		Target: job.Target{ASIN: kindleASIN, SourceASIN: paperASIN}}
}

type fakePaperPageFetcher struct {
	result PaperCheckResult
	err    error
	calls  int
}

func (f *fakePaperPageFetcher) FetchPaperPage(_ context.Context, _ string) (PaperCheckResult, error) {
	f.calls++
	return f.result, f.err
}

type fakeKindlePageFetcher struct {
	result KindleDetailResult
	err    error
	calls  int
}

func (f *fakeKindlePageFetcher) FetchKindlePage(_ context.Context, _ string) (KindleDetailResult, error) {
	f.calls++
	return f.result, f.err
}

type fakePaperBooksStore struct {
	updateCalled  bool
	updateApplied bool
	updateErr     error
	updateOldBook book.KindleBook
	lastUpdate    book.KindleBook
	deleted       []string
	deleteErr     error
	paperBook     book.KindleBook
	paperExists   bool
	paperErr      error
}

func (s *fakePaperBooksStore) UpdateOneBook(_ context.Context, _ string, update func(book.KindleBook) book.KindleBook) (bool, error) {
	s.updateCalled = true
	s.lastUpdate = update(s.updateOldBook)
	return s.updateApplied, s.updateErr
}

func (s *fakePaperBooksStore) Delete(_ context.Context, paperASIN string) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	s.deleted = append(s.deleted, paperASIN)
	return nil
}

func (s *fakePaperBooksStore) PaperBook(_ context.Context, _ string) (book.KindleBook, bool, error) {
	return s.paperBook, s.paperExists, s.paperErr
}

type fakeNotifiedStore struct {
	upserts   []book.KindleBook
	upsertErr error
}

func (s *fakeNotifiedStore) Upsert(_ context.Context, b book.KindleBook) error {
	if s.upsertErr != nil {
		return s.upsertErr
	}
	s.upserts = append(s.upserts, b)
	return nil
}

type fakeUpcomingStore struct {
	upserts   []book.KindleBook
	upsertErr error
}

func (s *fakeUpcomingStore) Upsert(_ context.Context, b book.KindleBook) error {
	if s.upsertErr != nil {
		return s.upsertErr
	}
	s.upserts = append(s.upserts, b)
	return nil
}

type fakeKnownStateQuerier struct {
	state KnownState
	err   error
}

func (q *fakeKnownStateQuerier) KnownState(_ context.Context, _, _ string) (KnownState, error) {
	return q.state, q.err
}

type fakeEnqueuer struct {
	jobs []job.Job
	err  error
}

func (e *fakeEnqueuer) Enqueue(_ context.Context, j job.Job) error {
	if e.err != nil {
		return e.err
	}
	e.jobs = append(e.jobs, j)
	return nil
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

type statefulPaperBooksStore struct {
	book   book.KindleBook
	exists bool
}

func (s *statefulPaperBooksStore) UpdateOneBook(_ context.Context, _ string, update func(book.KindleBook) book.KindleBook) (bool, error) {
	s.book = update(s.book)
	return s.exists, nil
}

func (s *statefulPaperBooksStore) Delete(_ context.Context, _ string) error { return nil }

func (s *statefulPaperBooksStore) PaperBook(_ context.Context, _ string) (book.KindleBook, bool, error) {
	return s.book, s.exists, nil
}

type failOnceEnqueuer struct {
	succeeded []job.Job
	failErr   error
	failed    bool
}

func (e *failOnceEnqueuer) Enqueue(_ context.Context, j job.Job) error {
	if !e.failed && e.failErr != nil {
		e.failed = true
		return e.failErr
	}
	e.succeeded = append(e.succeeded, j)
	return nil
}

func baseDeps() Dependencies {
	return Dependencies{
		PaperPageFetcher:  &fakePaperPageFetcher{},
		KindlePageFetcher: &fakeKindlePageFetcher{result: KindleDetailResult{Category: CategoryOK, Info: kindleInfo("B0KINDLE01")}},
		PaperBooksStore: &fakePaperBooksStore{
			updateApplied: true,
			paperExists:   true,
			paperBook:     book.KindleBook{ReleaseDate: releaseDay, CurrentPrice: book.NewPrice(792), URL: "https://www.amazon.co.jp/dp/B0PAPER001"},
		},
		NotifiedStore:     &fakeNotifiedStore{},
		UpcomingStore:     &fakeUpcomingStore{},
		KnownStateQuerier: &fakeKnownStateQuerier{state: KnownState{PaperBookExists: true}},
		Enqueuer:          &fakeEnqueuer{},
		Notifier:          &fakeNotifier{},
		Clock:             fixedClock,
	}
}

func paperInfoWithSwatch(paperASIN, kindleASIN string) PaperPageInfo {
	return PaperPageInfo{
		ASIN: paperASIN, Title: "紙タイトル", PaperPrice: book.NewPrice(792),
		HasPaperSwatch: true, HasKindleSwatch: true, KindleSwatchASIN: kindleASIN,
	}
}

func kindleInfo(asin string) KindlePageInfo {
	return KindlePageInfo{
		ASIN: asin, Title: "Kindleタイトル", CurrentPrice: book.NewPrice(759),
		ReleaseDate: releaseDay, HasReleaseDate: true, HasKindleSwatch: true,
	}
}

func TestIsSameReleaseDayJST(t *testing.T) {
	utcMidnight := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC) // JST 7/1 09:00
	other := time.Date(2026, 7, 1, 20, 0, 0, 0, time.UTC)      // JST 7/2 05:00
	if !IsSameReleaseDayJST(utcMidnight, releaseDay) {
		t.Errorf("同じJST暦日はtrue")
	}
	if IsSameReleaseDayJST(other, releaseDay) {
		t.Errorf("異なるJST暦日はfalse")
	}
}

func TestCheck_FetchesOnceAndEnqueuesDetail(t *testing.T) {
	fetcher := &fakePaperPageFetcher{result: PaperCheckResult{Category: CategoryOK, Info: paperInfoWithSwatch("B0PAPER001", "B0KINDLE01")}}
	deps := baseDeps()
	deps.PaperPageFetcher = fetcher

	if _, err := HandlePaperToKindleCheck(context.Background(), deps, checkJob("B0PAPER001")); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if fetcher.calls != 1 {
		t.Errorf("paper page fetch calls = %d, want 1", fetcher.calls)
	}
	enq := deps.Enqueuer.(*fakeEnqueuer)
	var detailJob, gistJob *job.Job
	for i := range enq.jobs {
		switch enq.jobs[i].Kind {
		case job.KindPaperToKindleDetail:
			detailJob = &enq.jobs[i]
		case job.KindGistUpdate:
			gistJob = &enq.jobs[i]
		}
	}
	if detailJob == nil || detailJob.Target.ASIN != "B0KINDLE01" || detailJob.Target.SourceASIN != "B0PAPER001" {
		t.Errorf("want 1 detail job to B0KINDLE01/B0PAPER001, got %+v", enq.jobs)
	}
	if gistJob == nil || gistJob.Target.GistType != gistPaperID {
		t.Errorf("want 1 paper gist job after price init, got %+v", enq.jobs)
	}
	if len(deps.NotifiedStore.(*fakeNotifiedStore).upserts) != 0 {
		t.Errorf("check must not save notified（詳細は後続job）")
	}
}

func TestCheck_InitializesPaperPriceWhenZero(t *testing.T) {
	info := paperInfoWithSwatch("B0PAPER001", "B0KINDLE01")
	fetcher := &fakePaperPageFetcher{result: PaperCheckResult{Category: CategoryOK, Info: info}}
	store := &fakePaperBooksStore{updateApplied: true, paperExists: true, paperBook: book.KindleBook{ReleaseDate: releaseDay}}
	deps := baseDeps()
	deps.PaperPageFetcher = fetcher
	deps.PaperBooksStore = store

	if _, err := HandlePaperToKindleCheck(context.Background(), deps, checkJob("B0PAPER001")); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !store.updateCalled {
		t.Errorf("paper price initialization must be called when PaperPrice is available")
	}
	if !store.lastUpdate.CurrentPrice.Valid() || store.lastUpdate.CurrentPrice.Yen() != 792 {
		t.Errorf("lastUpdate CurrentPrice = %+v, want 792", store.lastUpdate.CurrentPrice)
	}
	if !store.lastUpdate.MaxPrice.Valid() || store.lastUpdate.MaxPrice.Yen() != 792 {
		t.Errorf("lastUpdate MaxPrice = %+v, want 792", store.lastUpdate.MaxPrice)
	}
	enq := deps.Enqueuer.(*fakeEnqueuer)
	found := false
	for i := range enq.jobs {
		if enq.jobs[i].Kind == job.KindGistUpdate && enq.jobs[i].Target.GistType == gistPaperID {
			found = true
		}
	}
	if !found {
		t.Errorf("paper gist must be enqueued after price initialization: %+v", enq.jobs)
	}
}

func TestCheck_PriceInitFailureEnqueuesNoGist(t *testing.T) {
	info := paperInfoWithSwatch("B0PAPER001", "B0KINDLE01")
	deps := baseDeps()
	deps.PaperPageFetcher.(*fakePaperPageFetcher).result = PaperCheckResult{Category: CategoryOK, Info: info}
	deps.PaperBooksStore.(*fakePaperBooksStore).updateErr = errors.New("s3 down")

	oc, err := HandlePaperToKindleCheck(context.Background(), deps, checkJob("B0PAPER001"))
	if err == nil {
		t.Fatal("want error on store update failure")
	}
	if oc.ErrorType != errorTypeStoreUpdate {
		t.Errorf("error_type = %q, want store_update", oc.ErrorType)
	}
	if got := len(deps.Enqueuer.(*fakeEnqueuer).jobs); got != 0 {
		t.Errorf("no gist/detail must be enqueued on price init failure, got %d", got)
	}
}

func TestCheck_PriceAlreadySetStillEnqueuesGist(t *testing.T) {
	info := paperInfoWithSwatch("B0PAPER001", "B0KINDLE01")
	deps := baseDeps()
	deps.PaperPageFetcher.(*fakePaperPageFetcher).result = PaperCheckResult{Category: CategoryOK, Info: info}
	deps.PaperBooksStore.(*fakePaperBooksStore).updateOldBook.CurrentPrice = book.NewPrice(792)

	if _, err := HandlePaperToKindleCheck(context.Background(), deps, checkJob("B0PAPER001")); err != nil {
		t.Fatalf("Check: %v", err)
	}
	enq := deps.Enqueuer.(*fakeEnqueuer)
	var hasGist, hasDetail bool
	for i := range enq.jobs {
		switch enq.jobs[i].Kind {
		case job.KindGistUpdate:
			hasGist = true
		case job.KindPaperToKindleDetail:
			hasDetail = true
		}
	}
	if !hasGist {
		t.Errorf("gist must be enqueued even when price already set (7.5): %+v", enq.jobs)
	}
	if !hasDetail {
		t.Errorf("detail must be enqueued: %+v", enq.jobs)
	}
	last := deps.PaperBooksStore.(*fakePaperBooksStore).lastUpdate
	if !last.CurrentPrice.Valid() || last.CurrentPrice.Yen() != 792 {
		t.Errorf("price must not change when already set, got %+v", last.CurrentPrice)
	}
}

func TestCheck_TerminalCasesReturnNil(t *testing.T) {
	cases := []struct {
		name string
		info PaperPageInfo
		cat  Category
	}{
		{name: "not_paper_book", cat: CategoryOK, info: PaperPageInfo{ASIN: "B0PAPER001", HasPaperSwatch: false, HasKindleSwatch: true, KindleSwatchASIN: "B0K"}},
		{name: "kindle_not_available", cat: CategoryOK, info: PaperPageInfo{ASIN: "B0PAPER001", HasPaperSwatch: true, HasKindleSwatch: false}},
		{name: "no_kindle_swatch_asin", cat: CategoryOK, info: PaperPageInfo{ASIN: "B0PAPER001", HasPaperSwatch: true, HasKindleSwatch: true, KindleSwatchASIN: ""}},
		{name: "not_found", cat: CategoryNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := baseDeps()
			deps.PaperPageFetcher.(*fakePaperPageFetcher).result = PaperCheckResult{Category: tc.cat, Info: tc.info}
			if _, err := HandlePaperToKindleCheck(context.Background(), deps, checkJob("B0PAPER001")); err != nil {
				t.Fatalf("terminal must return nil: %v", err)
			}
			if len(deps.Enqueuer.(*fakeEnqueuer).jobs) != 0 {
				t.Errorf("terminal must not enqueue detail")
			}
		})
	}
}

func TestCheck_TargetRemovedIsTerminal(t *testing.T) {
	deps := baseDeps()
	deps.PaperPageFetcher.(*fakePaperPageFetcher).result = PaperCheckResult{Category: CategoryOK, Info: paperInfoWithSwatch("B0PAPER001", "B0KINDLE01")}
	deps.PaperBooksStore.(*fakePaperBooksStore).updateApplied = false

	if _, err := HandlePaperToKindleCheck(context.Background(), deps, checkJob("B0PAPER001")); err != nil {
		t.Fatalf("target_removed terminal: %v", err)
	}
	if len(deps.Enqueuer.(*fakeEnqueuer).jobs) != 0 {
		t.Errorf("target_removed must not enqueue detail")
	}
}

func TestCheck_RetryableReturnsError(t *testing.T) {
	deps := baseDeps()
	deps.PaperPageFetcher.(*fakePaperPageFetcher).result = PaperCheckResult{Category: CategoryRetryable}

	_, err := HandlePaperToKindleCheck(context.Background(), deps, checkJob("B0PAPER001"))
	if err == nil {
		t.Fatal("retryable must return error")
	}
	var re *ErrRetryableFetch
	if !errors.As(err, &re) {
		t.Errorf("error must be ErrRetryableFetch, got %T", err)
	}
}

func TestDetail_FetchesOnceAndApplies(t *testing.T) {
	fetcher := &fakeKindlePageFetcher{result: KindleDetailResult{Category: CategoryOK, Info: kindleInfo("B0KINDLE01")}}
	deps := baseDeps()
	deps.KindlePageFetcher = fetcher

	if _, err := HandlePaperToKindleDetail(context.Background(), deps, detailJob("B0KINDLE01", "B0PAPER001")); err != nil {
		t.Fatalf("Detail: %v", err)
	}
	if fetcher.calls != 1 {
		t.Errorf("kindle page fetch calls = %d, want 1", fetcher.calls)
	}
	if len(deps.NotifiedStore.(*fakeNotifiedStore).upserts) != 1 {
		t.Errorf("notified upsert count = %d, want 1", len(deps.NotifiedStore.(*fakeNotifiedStore).upserts))
	}
	if len(deps.PaperBooksStore.(*fakePaperBooksStore).deleted) != 1 {
		t.Errorf("paper book delete count = %d, want 1", len(deps.PaperBooksStore.(*fakePaperBooksStore).deleted))
	}
	enq := deps.Enqueuer.(*fakeEnqueuer)
	if len(enq.jobs) != 1 || enq.jobs[0].Target.GistType != "paper_to_kindle" {
		t.Errorf("Paper-to-Kindle gist job missing: %+v", enq.jobs)
	}
}

func TestDetail_EditionMismatchCasesReturnNil(t *testing.T) {
	cases := []struct {
		name string
		info KindlePageInfo
	}{
		{name: "asin_mismatch", info: KindlePageInfo{ASIN: "B0PAPER001", Title: "T", HasKindleSwatch: true, CurrentPrice: book.NewPrice(1), HasReleaseDate: true, ReleaseDate: releaseDay}},
		{name: "not_kindle", info: KindlePageInfo{ASIN: "B0KINDLE01", Title: "T", HasKindleSwatch: false}},
		{name: "release_date_mismatch", info: KindlePageInfo{ASIN: "B0KINDLE01", Title: "T", HasKindleSwatch: true, CurrentPrice: book.NewPrice(1), HasReleaseDate: true, ReleaseDate: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := baseDeps()
			deps.KindlePageFetcher.(*fakeKindlePageFetcher).result = KindleDetailResult{Category: CategoryOK, Info: tc.info}
			if _, err := HandlePaperToKindleDetail(context.Background(), deps, detailJob("B0KINDLE01", "B0PAPER001")); err != nil {
				t.Fatalf("edition_mismatch terminal: %v", err)
			}
			if len(deps.NotifiedStore.(*fakeNotifiedStore).upserts) != 0 {
				t.Errorf("edition_mismatch must not save")
			}
		})
	}
}

func TestDetail_ParseFailureIsRetryable(t *testing.T) {
	cases := []struct {
		name string
		info KindlePageInfo
	}{
		{name: "empty_title", info: KindlePageInfo{ASIN: "B0KINDLE01", Title: "", HasKindleSwatch: true, CurrentPrice: book.NewPrice(1), HasReleaseDate: true, ReleaseDate: releaseDay}},
		{name: "price_invalid", info: KindlePageInfo{ASIN: "B0KINDLE01", Title: "T", HasKindleSwatch: true, HasReleaseDate: true, ReleaseDate: releaseDay}},
		{name: "release_missing", info: KindlePageInfo{ASIN: "B0KINDLE01", Title: "T", HasKindleSwatch: true, CurrentPrice: book.NewPrice(1)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := baseDeps()
			deps.KindlePageFetcher.(*fakeKindlePageFetcher).result = KindleDetailResult{Category: CategoryOK, Info: tc.info}
			if _, err := HandlePaperToKindleDetail(context.Background(), deps, detailJob("B0KINDLE01", "B0PAPER001")); err == nil {
				t.Fatalf("parse failure must be retryable: %s", tc.name)
			}
		})
	}
}

func TestApply_AlreadyKnownSkipsNotifyButUpsertsAndEnqueuesGist(t *testing.T) {
	deps := baseDeps()
	deps.KnownStateQuerier.(*fakeKnownStateQuerier).state = KnownState{
		NotifiedExists: true, UpcomingExists: true, UnprocessedExists: false, PaperBookExists: true,
	}

	if _, err := HandlePaperToKindleDetail(context.Background(), deps, detailJob("B0KINDLE01", "B0PAPER001")); err != nil {
		t.Fatalf("Detail: %v", err)
	}
	if deps.Notifier.(*fakeNotifier).called {
		t.Errorf("既知ASINは通知しない")
	}
	if len(deps.UpcomingStore.(*fakeUpcomingStore).upserts) != 1 {
		t.Errorf("通知済みでも upcoming へ upsert する（7.5）")
	}
	if len(deps.Enqueuer.(*fakeEnqueuer).jobs) != 1 {
		t.Errorf("通知済みでも gist job を省略しない（7.5）")
	}
}

func TestApply_UnprocessedExistingSkipsNotifiedUpcomingUpsert(t *testing.T) {
	deps := baseDeps()
	deps.KnownStateQuerier.(*fakeKnownStateQuerier).state = KnownState{
		NotifiedExists: false, UpcomingExists: false, UnprocessedExists: true, PaperBookExists: true,
	}

	if _, err := HandlePaperToKindleDetail(context.Background(), deps, detailJob("B0KINDLE01", "B0PAPER001")); err != nil {
		t.Fatalf("Detail: %v", err)
	}
	if len(deps.NotifiedStore.(*fakeNotifiedStore).upserts) != 0 {
		t.Errorf("unprocessed 既存なら notified/upcoming へ upsert しない（SPEC 14.4 step2）")
	}
}

func TestApply_ManualDeleteWhenPaperBookAbsentAndCandidateUnknown(t *testing.T) {
	deps := baseDeps()
	deps.KnownStateQuerier.(*fakeKnownStateQuerier).state = KnownState{} // 全て false = 手動削除

	if _, err := HandlePaperToKindleDetail(context.Background(), deps, detailJob("B0KINDLE01", "B0PAPER001")); err != nil {
		t.Fatalf("Detail: %v", err)
	}
	if len(deps.NotifiedStore.(*fakeNotifiedStore).upserts) != 0 {
		t.Errorf("手動削除は新規保存しない")
	}
	if len(deps.Enqueuer.(*fakeEnqueuer).jobs) != 0 {
		t.Errorf("手動削除は gist job を投入しない")
	}
}

func TestApply_PreviousRunSuccessReEnqueuesGist(t *testing.T) {
	deps := baseDeps()
	deps.KnownStateQuerier.(*fakeKnownStateQuerier).state = KnownState{
		NotifiedExists: true, UpcomingExists: true, UnprocessedExists: false, PaperBookExists: false,
	}

	if _, err := HandlePaperToKindleDetail(context.Background(), deps, detailJob("B0KINDLE01", "B0PAPER001")); err != nil {
		t.Fatalf("Detail: %v", err)
	}
	if len(deps.Enqueuer.(*fakeEnqueuer).jobs) != 1 {
		t.Errorf("先行実行成功は gist job を再投入する（7.5）")
	}
	if len(deps.PaperBooksStore.(*fakePaperBooksStore).deleted) != 0 {
		t.Errorf("paper_books 既削除なら Delete を呼ばない")
	}
}

func TestApply_PartialFailureReturnsError(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Dependencies)
	}{
		{name: "notified_upsert_fail", mut: func(d *Dependencies) { d.NotifiedStore.(*fakeNotifiedStore).upsertErr = errors.New("s3") }},
		{name: "upcoming_upsert_fail", mut: func(d *Dependencies) { d.UpcomingStore.(*fakeUpcomingStore).upsertErr = errors.New("s3") }},
		{name: "paper_delete_fail", mut: func(d *Dependencies) { d.PaperBooksStore.(*fakePaperBooksStore).deleteErr = errors.New("s3") }},
		{name: "gist_enqueue_fail", mut: func(d *Dependencies) { d.Enqueuer.(*fakeEnqueuer).err = errors.New("sqs") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := baseDeps()
			tc.mut(&deps)
			if _, err := HandlePaperToKindleDetail(context.Background(), deps, detailJob("B0KINDLE01", "B0PAPER001")); err == nil {
				t.Fatalf("partial failure must return error for reconcile: %s", tc.name)
			}
		})
	}
}

func TestApply_NotifyFailureDoesNotRollback(t *testing.T) {
	deps := baseDeps()
	deps.Notifier.(*fakeNotifier).err = errors.New("slack down")

	if _, err := HandlePaperToKindleDetail(context.Background(), deps, detailJob("B0KINDLE01", "B0PAPER001")); err != nil {
		t.Fatalf("notify failure must not fail the job: %v", err)
	}
}

func TestApply_NotificationMessageContainsPaperAndKindleLines(t *testing.T) {
	deps := baseDeps()
	notifier := &fakeNotifier{}
	deps.Notifier = notifier

	if _, err := HandlePaperToKindleDetail(context.Background(), deps, detailJob("B0KINDLE01", "B0PAPER001")); err != nil {
		t.Fatalf("Detail: %v", err)
	}
	if !strings.Contains(notifier.message, "📕 紙書籍(792円)") {
		t.Errorf("紙書籍行(価格あり)を含むこと（SPEC 17.1）: %s", notifier.message)
	}
	if !strings.Contains(notifier.message, "📱 電子書籍(759円)") {
		t.Errorf("電子書籍行を含むこと（SPEC 17.1）: %s", notifier.message)
	}
}

func TestApply_NotificationMessageShowsPaperPriceUnavailableWhenAbsent(t *testing.T) {
	deps := baseDeps()
	deps.PaperBooksStore.(*fakePaperBooksStore).paperBook.CurrentPrice = book.UnknownPrice() // 紙価格未取得
	notifier := &fakeNotifier{}
	deps.Notifier = notifier

	if _, err := HandlePaperToKindleDetail(context.Background(), deps, detailJob("B0KINDLE01", "B0PAPER001")); err != nil {
		t.Fatalf("Detail: %v", err)
	}
	if !strings.Contains(notifier.message, "📕 紙書籍(価格取得不可)") {
		t.Errorf("紙価格未取得時は価格取得不可（SPEC 14.2/17.1）: %s", notifier.message)
	}
}

func TestApply_SavedKindleURLContainsPartnerTag(t *testing.T) {
	deps := baseDeps()
	deps.Config = Config{PartnerTag: "testtag-22"}

	if _, err := HandlePaperToKindleDetail(context.Background(), deps, detailJob("B0KINDLE01", "B0PAPER001")); err != nil {
		t.Fatalf("Detail: %v", err)
	}
	const wantKindleURL = "https://www.amazon.co.jp/dp/B0KINDLE01?tag=testtag-22"
	notified := deps.NotifiedStore.(*fakeNotifiedStore)
	if len(notified.upserts) != 1 || notified.upserts[0].URL != wantKindleURL {
		t.Errorf("saved notified URL = %+v, want %q", notified.upserts, wantKindleURL)
	}
	upcoming := deps.UpcomingStore.(*fakeUpcomingStore)
	if len(upcoming.upserts) != 1 || upcoming.upserts[0].URL != wantKindleURL {
		t.Errorf("saved upcoming URL = %+v, want %q", upcoming.upserts, wantKindleURL)
	}
}

func TestApply_SavedKindleURLHasNoTagWhenPartnerTagEmpty(t *testing.T) {
	deps := baseDeps()

	if _, err := HandlePaperToKindleDetail(context.Background(), deps, detailJob("B0KINDLE01", "B0PAPER001")); err != nil {
		t.Fatalf("Detail: %v", err)
	}
	notified := deps.NotifiedStore.(*fakeNotifiedStore)
	if len(notified.upserts) != 1 {
		t.Fatalf("notified upsert count = %d, want 1", len(notified.upserts))
	}
	const wantKindleURL = "https://www.amazon.co.jp/dp/B0KINDLE01"
	if notified.upserts[0].URL != wantKindleURL {
		t.Errorf("saved notified URL = %q, want %q (tag なし)", notified.upserts[0].URL, wantKindleURL)
	}
}

func TestApply_NotificationKindleURLContainsPartnerTag(t *testing.T) {
	deps := baseDeps()
	deps.Config = Config{PartnerTag: "testtag-22"}
	notifier := &fakeNotifier{}
	deps.Notifier = notifier

	if _, err := HandlePaperToKindleDetail(context.Background(), deps, detailJob("B0KINDLE01", "B0PAPER001")); err != nil {
		t.Fatalf("Detail: %v", err)
	}
	if !strings.Contains(notifier.message, "https://www.amazon.co.jp/dp/B0KINDLE01?tag=testtag-22") {
		t.Errorf("通知 Kindle URL に partner tag を含むこと: %s", notifier.message)
	}
	if strings.Contains(notifier.message, "B0PAPER001?tag=") {
		t.Errorf("紙書籍URLには partner tag を付けない: %s", notifier.message)
	}
}

func TestBuildDetailJob_IsDeterministicAndUsesAmazonRequests(t *testing.T) {
	j := checkJob("B0PAPER001")
	d := buildDetailJob(j, "B0KINDLE01")
	if got := job.MessageGroup(d.Kind); got != "amazon-requests" {
		t.Errorf("detail MessageGroup = %q, want amazon-requests", got)
	}
	if buildDetailJob(j, "B0KINDLE01").JobID != d.JobID {
		t.Errorf("detail JobID is not deterministic")
	}
	gist := buildPaperToKindleGistJob(j, gistStagePaperPriceInit, "B0PAPER001")
	if got := job.MessageGroup(gist.Kind); got != "external-updates" {
		t.Errorf("gist MessageGroup = %q, want external-updates", got)
	}
	if job.AmazonRequests(gist.Kind) != 0 {
		t.Errorf("gist_update must not access Amazon")
	}
}

func TestBuildPaperGistJob_DiscriminatorIsDeterministic(t *testing.T) {
	j := checkJob("B0PAPER001")
	priceInitA := buildPaperToKindleGistJob(j, gistStagePaperPriceInit, "B0PAPER001")
	deleteA := buildPaperToKindleGistJob(j, gistStagePaperDelete, "B0PAPER001")
	priceInitB := buildPaperToKindleGistJob(j, gistStagePaperPriceInit, "B0PAPER002")

	if priceInitA.JobID == deleteA.JobID {
		t.Errorf("price_init and delete must differ: %s", priceInitA.JobID)
	}
	if scheduling.DedupID(priceInitA.JobID) == scheduling.DedupID(deleteA.JobID) {
		t.Errorf("price_init and delete dedup must differ")
	}
	if priceInitA.JobID == priceInitB.JobID {
		t.Errorf("different paper ASIN must differ: %s", priceInitA.JobID)
	}
	if buildPaperToKindleGistJob(j, gistStagePaperPriceInit, "B0PAPER001").JobID != priceInitA.JobID {
		t.Errorf("same state change must be deterministic")
	}
	if priceInitA.Target.GistType != gistPaperID {
		t.Errorf("GistType = %q, want %q", priceInitA.Target.GistType, gistPaperID)
	}
}

func TestCheck_FetchErrorIsRetryable(t *testing.T) {
	deps := baseDeps()
	deps.PaperPageFetcher.(*fakePaperPageFetcher).err = errors.New("dial tcp: timeout")

	oc, err := HandlePaperToKindleCheck(context.Background(), deps, checkJob("B0PAPER001"))
	if err == nil {
		t.Fatal("fetch error must return error")
	}
	if oc.ErrorType != errorTypeFetchError {
		t.Errorf("error_type = %q, want fetch_error", oc.ErrorType)
	}
	if oc.HTTPStatus != 0 || oc.ResponseBytes != 0 {
		t.Errorf("fetch error must carry no HTTP metrics: status=%d bytes=%d", oc.HTTPStatus, oc.ResponseBytes)
	}
}

func TestCheck_UnknownCategoryIsError(t *testing.T) {
	deps := baseDeps()
	deps.PaperPageFetcher.(*fakePaperPageFetcher).result = PaperCheckResult{Category: Category(99)}

	oc, err := HandlePaperToKindleCheck(context.Background(), deps, checkJob("B0PAPER001"))
	if err == nil {
		t.Fatal("unknown category must return error")
	}
	if oc.ErrorType != errorTypeUnknownCategory {
		t.Errorf("error_type = %q, want unknown_category", oc.ErrorType)
	}
}

func TestCheck_PaperPageASINMismatchIsTerminal(t *testing.T) {
	info := paperInfoWithSwatch("B0PAPER001", "B0KINDLE01")
	info.ASIN = "B0WRONG0001" // 要求 B0PAPER001 と異なる
	deps := baseDeps()
	deps.PaperPageFetcher.(*fakePaperPageFetcher).result = PaperCheckResult{Category: CategoryOK, Info: info}

	oc, err := HandlePaperToKindleCheck(context.Background(), deps, checkJob("B0PAPER001"))
	if err != nil {
		t.Fatalf("asin_mismatch is terminal, no error: %v", err)
	}
	if oc.Result != execution.ResultTerminal || oc.ErrorType != errorTypeAsinMismatch {
		t.Errorf("result/error_type = %q/%q, want terminal/asin_mismatch", oc.Result, oc.ErrorType)
	}
	if len(deps.Enqueuer.(*fakeEnqueuer).jobs) != 0 {
		t.Errorf("asin_mismatch must not enqueue")
	}
}

func TestCheck_PermanentClientErrorIsTerminal(t *testing.T) {
	deps := baseDeps()
	deps.PaperPageFetcher.(*fakePaperPageFetcher).result = PaperCheckResult{Category: CategoryPermanentClientError, HTTPStatus: 401}

	oc, err := HandlePaperToKindleCheck(context.Background(), deps, checkJob("B0PAPER001"))
	if err != nil {
		t.Fatalf("permanent client error is terminal: %v", err)
	}
	if oc.Result != execution.ResultTerminal || oc.ErrorType != "permanent_client_error" {
		t.Errorf("result/error_type = %q/%q, want terminal/permanent_client_error", oc.Result, oc.ErrorType)
	}
	if len(deps.Enqueuer.(*fakeEnqueuer).jobs) != 0 {
		t.Errorf("permanent client error must not enqueue")
	}
}

func TestCheck_GistEnqueueFailureAfterPriceInitIsError(t *testing.T) {
	info := paperInfoWithSwatch("B0PAPER001", "B0KINDLE01")
	deps := baseDeps()
	deps.PaperPageFetcher.(*fakePaperPageFetcher).result = PaperCheckResult{Category: CategoryOK, Info: info}
	deps.Enqueuer.(*fakeEnqueuer).err = errors.New("sqs throttled")

	oc, err := HandlePaperToKindleCheck(context.Background(), deps, checkJob("B0PAPER001"))
	if err == nil {
		t.Fatal("gist enqueue failure must return error")
	}
	if oc.ErrorType != errorTypeEnqueueFailed {
		t.Errorf("error_type = %q, want enqueue_failed", oc.ErrorType)
	}
}

func TestCheck_RedeliveryReconcilesMissingGistAfterPriceInit(t *testing.T) {
	info := PaperPageInfo{
		ASIN: "B0PAPER001", Title: "紙タイトル", PaperPrice: book.NewPrice(792),
		HasPaperSwatch: true, HasKindleSwatch: false,
	}
	store := &statefulPaperBooksStore{book: book.KindleBook{ReleaseDate: releaseDay}, exists: true}
	enq := &failOnceEnqueuer{failErr: errors.New("sqs throttled")}
	deps := baseDeps()
	deps.PaperPageFetcher.(*fakePaperPageFetcher).result = PaperCheckResult{Category: CategoryOK, Info: info}
	deps.PaperBooksStore = store
	deps.Enqueuer = enq
	j := checkJob("B0PAPER001")

	oc1, err1 := HandlePaperToKindleCheck(context.Background(), deps, j)
	if err1 == nil {
		t.Fatal("first run must fail when gist enqueue fails after price init")
	}
	if oc1.ErrorType != errorTypeEnqueueFailed {
		t.Errorf("first run error_type = %q, want enqueue_failed", oc1.ErrorType)
	}
	if !store.book.CurrentPrice.Valid() || store.book.CurrentPrice.Yen() != 792 {
		t.Errorf("price must persist before gist enqueue failure, got %+v", store.book.CurrentPrice)
	}

	oc2, err2 := HandlePaperToKindleCheck(context.Background(), deps, j)
	if err2 != nil {
		t.Fatalf("second run must reconcile gist without error: %v", err2)
	}
	if oc2.Result != execution.ResultTerminal || oc2.ErrorType != errorTypeKindleNA {
		t.Errorf("second run result/error_type = %q/%q, want terminal/kindle_not_available", oc2.Result, oc2.ErrorType)
	}
	var gistCount int
	for i := range enq.succeeded {
		if enq.succeeded[i].Kind == job.KindGistUpdate && enq.succeeded[i].Target.GistType == gistPaperID {
			gistCount++
		}
	}
	if gistCount != 1 {
		t.Errorf("redelivery must enqueue paper gist exactly once, got %d", gistCount)
	}
}

func TestCheck_DetailEnqueueFailureIsError(t *testing.T) {
	info := PaperPageInfo{
		ASIN: "B0PAPER001", Title: "紙タイトル", PaperPrice: book.UnknownPrice(),
		HasPaperSwatch: true, HasKindleSwatch: true, KindleSwatchASIN: "B0KINDLE01",
	}
	deps := baseDeps()
	deps.PaperPageFetcher.(*fakePaperPageFetcher).result = PaperCheckResult{Category: CategoryOK, Info: info}
	deps.Enqueuer.(*fakeEnqueuer).err = errors.New("sqs throttled")

	oc, err := HandlePaperToKindleCheck(context.Background(), deps, checkJob("B0PAPER001"))
	if err == nil {
		t.Fatal("detail enqueue failure must return error")
	}
	if oc.ErrorType != errorTypeEnqueueFailed {
		t.Errorf("error_type = %q, want enqueue_failed", oc.ErrorType)
	}
}

func TestCheck_EnqueuesDetailWhenPaperPriceUnavailable(t *testing.T) {
	info := PaperPageInfo{
		ASIN: "B0PAPER001", Title: "紙タイトル", PaperPrice: book.UnknownPrice(),
		HasPaperSwatch: true, HasKindleSwatch: true, KindleSwatchASIN: "B0KINDLE01",
	}
	deps := baseDeps()
	deps.PaperPageFetcher.(*fakePaperPageFetcher).result = PaperCheckResult{Category: CategoryOK, Info: info}
	store := deps.PaperBooksStore.(*fakePaperBooksStore)

	if _, err := HandlePaperToKindleCheck(context.Background(), deps, checkJob("B0PAPER001")); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if store.updateCalled {
		t.Errorf("paper price unavailable must not call price initialization")
	}
	enq := deps.Enqueuer.(*fakeEnqueuer)
	if len(enq.jobs) != 1 || enq.jobs[0].Kind != job.KindPaperToKindleDetail {
		t.Errorf("want 1 detail job only when paper price unavailable, got %+v", enq.jobs)
	}
	for i := range enq.jobs {
		if enq.jobs[i].Kind == job.KindGistUpdate {
			t.Errorf("gist must not be enqueued when price not initialized: %+v", enq.jobs)
		}
	}
}

func TestDetail_FetchErrorIsRetryable(t *testing.T) {
	deps := baseDeps()
	deps.KindlePageFetcher.(*fakeKindlePageFetcher).err = errors.New("dial tcp: timeout")

	oc, err := HandlePaperToKindleDetail(context.Background(), deps, detailJob("B0KINDLE01", "B0PAPER001"))
	if err == nil {
		t.Fatal("fetch error must return error")
	}
	if oc.ErrorType != errorTypeFetchError {
		t.Errorf("error_type = %q, want fetch_error", oc.ErrorType)
	}
}

func TestDetail_RetryableReturnsError(t *testing.T) {
	deps := baseDeps()
	deps.KindlePageFetcher.(*fakeKindlePageFetcher).result = KindleDetailResult{Category: CategoryRetryable}

	_, err := HandlePaperToKindleDetail(context.Background(), deps, detailJob("B0KINDLE01", "B0PAPER001"))
	if err == nil {
		t.Fatal("retryable must return error")
	}
	var re *ErrRetryableFetch
	if !errors.As(err, &re) {
		t.Fatalf("error must be ErrRetryableFetch, got %T", err)
	}
	if re.ASIN != "B0KINDLE01" || !strings.Contains(re.Error(), "B0KINDLE01") {
		t.Errorf("ErrRetryableFetch must carry ASIN: ASIN=%q msg=%q", re.ASIN, re.Error())
	}
}

func TestDetail_NotFoundAndPermanentAreTerminal(t *testing.T) {
	cases := []struct {
		name     string
		cat      Category
		wantType string
	}{
		{name: "not_found", cat: CategoryNotFound, wantType: "not_found"},
		{name: "permanent_client_error", cat: CategoryPermanentClientError, wantType: "permanent_client_error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := baseDeps()
			deps.KindlePageFetcher.(*fakeKindlePageFetcher).result = KindleDetailResult{Category: tc.cat}
			oc, err := HandlePaperToKindleDetail(context.Background(), deps, detailJob("B0KINDLE01", "B0PAPER001"))
			if err != nil {
				t.Fatalf("terminal must return nil error: %v", err)
			}
			if oc.Result != execution.ResultTerminal || oc.ErrorType != tc.wantType {
				t.Errorf("result/error_type = %q/%q, want terminal/%s", oc.Result, oc.ErrorType, tc.wantType)
			}
		})
	}
}

func TestDetail_UnknownCategoryIsError(t *testing.T) {
	deps := baseDeps()
	deps.KindlePageFetcher.(*fakeKindlePageFetcher).result = KindleDetailResult{Category: Category(99)}

	oc, err := HandlePaperToKindleDetail(context.Background(), deps, detailJob("B0KINDLE01", "B0PAPER001"))
	if err == nil {
		t.Fatal("unknown category must return error")
	}
	if oc.ErrorType != errorTypeUnknownCategory {
		t.Errorf("error_type = %q, want unknown_category", oc.ErrorType)
	}
}

func TestDetail_SameAsinIsTerminal(t *testing.T) {
	info := KindlePageInfo{ASIN: "B0PAPER001", Title: "T", HasKindleSwatch: true,
		CurrentPrice: book.NewPrice(1), HasReleaseDate: true, ReleaseDate: releaseDay}
	deps := baseDeps()
	deps.KindlePageFetcher.(*fakeKindlePageFetcher).result = KindleDetailResult{Category: CategoryOK, Info: info}

	oc, err := HandlePaperToKindleDetail(context.Background(), deps, detailJob("B0PAPER001", "B0PAPER001"))
	if err != nil {
		t.Fatalf("same_asin is terminal, no error: %v", err)
	}
	if oc.Result != execution.ResultTerminal || oc.ErrorType != errorTypeSameAsin {
		t.Errorf("result/error_type = %q/%q, want terminal/same_asin", oc.Result, oc.ErrorType)
	}
	if len(deps.NotifiedStore.(*fakeNotifiedStore).upserts) != 0 {
		t.Errorf("same_asin must not save")
	}
}

func TestDetail_AsinMismatchIsTerminal(t *testing.T) {
	info := kindleInfo("B0WRONG0001") // 要求 B0KINDLE01 と異なる
	deps := baseDeps()
	deps.KindlePageFetcher.(*fakeKindlePageFetcher).result = KindleDetailResult{Category: CategoryOK, Info: info}

	oc, err := HandlePaperToKindleDetail(context.Background(), deps, detailJob("B0KINDLE01", "B0PAPER001"))
	if err != nil {
		t.Fatalf("asin_mismatch is terminal, no error: %v", err)
	}
	if oc.Result != execution.ResultTerminal || oc.ErrorType != errorTypeAsinMismatch {
		t.Errorf("result/error_type = %q/%q, want terminal/asin_mismatch", oc.Result, oc.ErrorType)
	}
}

func TestDetail_PaperBookLoadFailureIsError(t *testing.T) {
	deps := baseDeps()
	deps.KindlePageFetcher.(*fakeKindlePageFetcher).result = KindleDetailResult{Category: CategoryOK, Info: kindleInfo("B0KINDLE01")}
	deps.PaperBooksStore.(*fakePaperBooksStore).paperErr = errors.New("s3 down")

	oc, err := HandlePaperToKindleDetail(context.Background(), deps, detailJob("B0KINDLE01", "B0PAPER001"))
	if err == nil {
		t.Fatal("paper book load failure must return error")
	}
	if oc.ErrorType != errorTypeStoreFailed {
		t.Errorf("error_type = %q, want store_failed", oc.ErrorType)
	}
}

func TestDetail_KnownStateFailureIsError(t *testing.T) {
	deps := baseDeps()
	deps.KnownStateQuerier.(*fakeKnownStateQuerier).err = errors.New("s3 down")

	oc, err := HandlePaperToKindleDetail(context.Background(), deps, detailJob("B0KINDLE01", "B0PAPER001"))
	if err == nil {
		t.Fatal("known state failure must return error")
	}
	if oc.ErrorType != errorTypeKnownState {
		t.Errorf("error_type = %q, want known_state", oc.ErrorType)
	}
}

func TestApply_PartialFailurePreservesPriorSideEffects(t *testing.T) {
	deps := baseDeps()
	deps.UpcomingStore.(*fakeUpcomingStore).upsertErr = errors.New("s3 down")

	oc, err := HandlePaperToKindleDetail(context.Background(), deps, detailJob("B0KINDLE01", "B0PAPER001"))
	if err == nil {
		t.Fatal("upcoming upsert failure must return error for reconcile")
	}
	if oc.ErrorType != errorTypeUpcomingUpsert {
		t.Errorf("error_type = %q, want upcoming_upsert", oc.ErrorType)
	}
	if len(deps.NotifiedStore.(*fakeNotifiedStore).upserts) != 1 {
		t.Errorf("notified must be persisted before upcoming failure, got %d", len(deps.NotifiedStore.(*fakeNotifiedStore).upserts))
	}
	if len(deps.PaperBooksStore.(*fakePaperBooksStore).deleted) != 0 {
		t.Errorf("paper book must not be deleted before upcoming succeeds")
	}
}

func TestApply_SavedBookMaxPriceUsesKindlePriceNotPaper(t *testing.T) {
	deps := baseDeps()
	deps.PaperBooksStore.(*fakePaperBooksStore).paperBook.CurrentPrice = book.NewPrice(792)

	if _, err := HandlePaperToKindleDetail(context.Background(), deps, detailJob("B0KINDLE01", "B0PAPER001")); err != nil {
		t.Fatalf("Detail: %v", err)
	}
	notified := deps.NotifiedStore.(*fakeNotifiedStore).upserts[0]
	if !notified.MaxPrice.Valid() || notified.MaxPrice.Yen() != 759 {
		t.Errorf("saved MaxPrice = %+v, want Kindle 759 (not paper 792)", notified.MaxPrice)
	}
	if !notified.CurrentPrice.Valid() || notified.CurrentPrice.Yen() != 759 {
		t.Errorf("saved CurrentPrice = %+v, want Kindle 759", notified.CurrentPrice)
	}
}

func TestApply_IdempotentAcrossDuplicateDelivery(t *testing.T) {
	deps := baseDeps()
	j := detailJob("B0KINDLE01", "B0PAPER001")

	if _, err := HandlePaperToKindleDetail(context.Background(), deps, j); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if _, err := HandlePaperToKindleDetail(context.Background(), deps, j); err != nil {
		t.Fatalf("duplicate delivery must reconcile without error: %v", err)
	}
	jobs := deps.Enqueuer.(*fakeEnqueuer).jobs
	if len(jobs) < 2 {
		t.Fatalf("gist job enqueued on both runs, got %d", len(jobs))
	}
	if jobs[0].JobID != jobs[1].JobID {
		t.Errorf("duplicate gist job_id must be identical for idempotent retry: %q vs %q", jobs[0].JobID, jobs[1].JobID)
	}
}
