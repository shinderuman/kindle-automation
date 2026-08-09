package papertokindle

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shinderuman/kindle-automation/internal/domain/book"
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

// --- stubs ---

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

// --- 純粋関数 ---

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

// --- HandlePaperToKindleCheck ---

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
	// 価格初期化（旧レコードは価格未設定）で gist、Kindle候補で detail を投入する。
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
	// CurrentPrice と MaxPrice を PaperPrice へ初期化した。
	if !store.lastUpdate.CurrentPrice.Valid() || store.lastUpdate.CurrentPrice.Yen() != 792 {
		t.Errorf("lastUpdate CurrentPrice = %+v, want 792", store.lastUpdate.CurrentPrice)
	}
	if !store.lastUpdate.MaxPrice.Valid() || store.lastUpdate.MaxPrice.Yen() != 792 {
		t.Errorf("lastUpdate MaxPrice = %+v, want 792", store.lastUpdate.MaxPrice)
	}
	// 価格初期化成功後に Paper 用 gist_update を投入する（SPECIFICATION.md 14.2/15）。
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

// 価格初期化の S3 保存失敗時は gist も detail も投入せず store_update の retryable error にする。
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

// 価格が既に初期化済みなら gist を投入せず、Kindle候補の detail だけ投入する（冪等）。
func TestCheck_PriceAlreadyInitializedIsIdempotent(t *testing.T) {
	info := paperInfoWithSwatch("B0PAPER001", "B0KINDLE01")
	deps := baseDeps()
	deps.PaperPageFetcher.(*fakePaperPageFetcher).result = PaperCheckResult{Category: CategoryOK, Info: info}
	deps.PaperBooksStore.(*fakePaperBooksStore).updateOldBook.CurrentPrice = book.NewPrice(792)

	if _, err := HandlePaperToKindleCheck(context.Background(), deps, checkJob("B0PAPER001")); err != nil {
		t.Fatalf("Check: %v", err)
	}
	enq := deps.Enqueuer.(*fakeEnqueuer)
	for i := range enq.jobs {
		if enq.jobs[i].Kind == job.KindGistUpdate {
			t.Errorf("gist must not be enqueued when price already initialized: %+v", enq.jobs)
		}
	}
	if len(enq.jobs) != 1 || enq.jobs[0].Kind != job.KindPaperToKindleDetail {
		t.Fatalf("want only 1 detail job when price already set, got %+v", enq.jobs)
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

// --- HandlePaperToKindleDetail ---

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
		{name: "same_asin", info: KindlePageInfo{ASIN: "B0PAPER001", Title: "T", HasKindleSwatch: true, CurrentPrice: book.NewPrice(1), HasReleaseDate: true, ReleaseDate: releaseDay}},
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

// --- applyDetectedBook ---

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
	// paper_books 既削除 + 候補保存済み = 先行実行成功として gist job 再投入。
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

// 通知失敗は Notifier adapter が notification_error（SPECIFICATION.md 18.3）で記録するため、
// paper_to_kindle ユースケースでは job を completed のままにし、保存済み状態を巻き戻さない。
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
	// SPECIFICATION.md 9.2: 保存用 Kindle URL へ Affiliate Tag を付ける。
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
	// partnerTag 未設定時は ?tag= を付けない（既存挙動の保護）。
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
	// 紙書籍URLは既存レコード由来のため partner tag を付けない。
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
	gist := buildPaperToKindleGistJob(j)
	if got := job.MessageGroup(gist.Kind); got != "external-updates" {
		t.Errorf("gist MessageGroup = %q, want external-updates", got)
	}
	if job.AmazonRequests(gist.Kind) != 0 {
		t.Errorf("gist_update must not access Amazon")
	}
}
