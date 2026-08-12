package newrelease

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/shinderuman/kindle-automation/internal/application/execution"
	"github.com/shinderuman/kindle-automation/internal/domain/book"
	"github.com/shinderuman/kindle-automation/internal/domain/scheduling"
	"github.com/shinderuman/kindle-automation/internal/job"
)

var fixedClock = func() time.Time { return time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC) }
var futureDate = time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)
var pastDate = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

func searchJob(author string) job.Job {
	return job.Job{Version: job.Version, JobID: "s", Kind: job.KindNewReleaseSearch,
		CheckType: job.CheckNewRelease, CycleID: "nr:c", Target: job.Target{AuthorName: author}}
}

func resultJob(asin, author string, product *job.SearchProduct) job.Job {
	return job.Job{Version: job.Version, JobID: "r", Kind: job.KindNewReleaseResult,
		CheckType: job.CheckNewRelease, CycleID: "nr:c",
		Target: job.Target{ASIN: asin, AuthorName: author, Product: product}}
}

func detailJob(asin, author string) job.Job {
	return job.Job{Version: job.Version, JobID: "d", Kind: job.KindNewReleaseDetail,
		CheckType: job.CheckNewRelease, CycleID: "nr:c",
		Target: job.Target{ASIN: asin, AuthorName: author}}
}

func paperDetailJob(asin, author string) job.Job {
	return job.Job{Version: job.Version, JobID: "p", Kind: job.KindNewReleasePaperDetail,
		CheckType: job.CheckNewRelease, CycleID: "nr:c",
		Target: job.Target{ASIN: asin, AuthorName: author}}
}

func paperPaperPageInfo(asin string) PaperPageInfo {
	return PaperPageInfo{
		ASIN: asin, Title: "紙タイトル", URL: "https://u/" + asin,
		PaperPrice: book.NewPrice(900), ReleaseDate: futureDate, HasReleaseDate: true,
		Contributors: []string{"海李"},
	}
}

type fakeSearchFetcher struct {
	result SearchResult
	err    error
	calls  int
}

func (f *fakeSearchFetcher) FetchSearch(_ context.Context, _ string) (SearchResult, error) {
	f.calls++
	return f.result, f.err
}

type fakeProductFetcher struct {
	result ProductResult
	err    error
	calls  int
}

func (f *fakeProductFetcher) FetchProduct(_ context.Context, _ string) (ProductResult, error) {
	f.calls++
	return f.result, f.err
}

type fakePaperPageFetcher struct {
	result PaperPageResult
	err    error
	calls  int
}

func (f *fakePaperPageFetcher) FetchPaperPage(_ context.Context, _ string) (PaperPageResult, error) {
	f.calls++
	return f.result, f.err
}

type fakePaperCandidateStore struct {
	changed     bool
	upserts     []book.KindleBook
	upsertErr   error
	changeFor   map[string]bool
	exists      bool
	existsErr   error
	existsFor   map[string]bool
	existsCalls int
}

func (s *fakePaperCandidateStore) UpsertChanged(_ context.Context, b book.KindleBook) (bool, error) {
	if s.upsertErr != nil {
		return false, s.upsertErr
	}
	s.upserts = append(s.upserts, b)
	if s.changeFor != nil {
		return s.changeFor[b.ASIN], nil
	}
	return s.changed, nil
}

func (s *fakePaperCandidateStore) Exists(_ context.Context, asin string) (bool, error) {
	s.existsCalls++
	if s.existsErr != nil {
		return false, s.existsErr
	}
	if s.existsFor != nil {
		return s.existsFor[asin], nil
	}
	return s.exists, nil
}

type fakeNotifiedStore struct {
	exists          bool
	existsErr       error
	alreadyNotified bool
	retentionErr    error
	upserts         []book.KindleBook
	upsertErr       error
}

func (s *fakeNotifiedStore) Exists(_ context.Context, _ string) (bool, error) {
	return s.exists, s.existsErr
}
func (s *fakeNotifiedStore) ApplyRetentionAndExists(_ context.Context, _ string, _ time.Time) (bool, error) {
	return s.alreadyNotified, s.retentionErr
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

type fakeAuthorStore struct {
	changed   bool
	err       error
	gotAuthor string
	gotURL    string
	gotTitle  string
}

func (s *fakeAuthorStore) UpdateLatestRelease(_ context.Context, author string, _ time.Time, title, url string) (bool, error) {
	s.gotAuthor = author
	s.gotTitle = title
	s.gotURL = url
	return s.changed, s.err
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
		SearchFetcher:       &fakeSearchFetcher{},
		ProductFetcher:      &fakeProductFetcher{},
		PaperPageFetcher:    &fakePaperPageFetcher{},
		NotifiedStore:       &fakeNotifiedStore{},
		UpcomingStore:       &fakeUpcomingStore{},
		AuthorStore:         &fakeAuthorStore{},
		PaperCandidateStore: &fakePaperCandidateStore{},
		Enqueuer:            &fakeEnqueuer{},
		Notifier:            &fakeNotifier{},
		Config:              Config{ExcludedKeywords: []string{"除外"}},
		Clock:               fixedClock,
	}
}

func completeHit(asin string) SearchHit {
	return SearchHit{
		ASIN: asin, Title: "タイトル", URL: "https://u/" + asin,
		KindlePrice: book.NewPrice(800), ReleaseDate: futureDate, HasReleaseDate: true,
		Contributors: []string{"海李"}, IsKindle: true,
	}
}

func TestNormalizeAuthorName(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "全角英数字を半角へ", in: "Ａｕｔｈｏｒ１", want: "Author1"},
		{name: "全角スペースを除去", in: "海　李", want: "海李"},
		{name: "半角スペースを除去", in: "海 李", want: "海李"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalizeAuthorName(tc.in); got != tc.want {
				t.Errorf("NormalizeAuthorName(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestIsISBNASIN(t *testing.T) {
	if !IsISBNASIN("1234567890") {
		t.Errorf("10桁数字はISBN")
	}
	if !IsISBNASIN("1234567890123") {
		t.Errorf("13桁数字はISBN")
	}
	if IsISBNASIN("B0FX3X569X") {
		t.Errorf("Kindle ASIN はISBNではない")
	}
}

func TestExcludedByKeywordAndYearMonth(t *testing.T) {
	if !ExcludedByKeyword("タイトル除外対象", []string{"除外"}) {
		t.Errorf("キーワード含むときtrue")
	}
	if ExcludedByKeyword("タイトル", []string{"除外"}) {
		t.Errorf("キーワード含まないときfalse")
	}
	if !ExcludedByYearMonth("2026年8月新刊") {
		t.Errorf("年月パターン含むときtrue")
	}
	if ExcludedByYearMonth("タイトル") {
		t.Errorf("年月パターン含まないときfalse")
	}
}

func TestAuthorMatches(t *testing.T) {
	if !AuthorMatches("海李", []string{"海李"}) {
		t.Errorf("完全一致する場合はtrue")
	}
	if AuthorMatches("海李", []string{"別人"}) {
		t.Errorf("不一致はfalse")
	}
	if AuthorMatches("海李", nil) {
		t.Errorf("contributor空はfalse")
	}
	if !AuthorMatches("ＡＢＣ", []string{"ABC"}) {
		t.Errorf("全角半角違いは正規化で一致")
	}
	if !AuthorMatches("海李", []string{"海李 (著)"}) {
		t.Errorf("役割括弧を除去したcontributorと一致する場合はtrue")
	}
	if !AuthorMatches("やきいもほくほく", []string{"上原誠", "やきいもほくほく"}) {
		t.Errorf("複数contributorのいずれかに完全一致する場合はtrue")
	}
	if AuthorMatches("山田次郎", []string{"山田 太郎"}) {
		t.Errorf("空白入り別人（山田 太郎 vs 山田次郎）は姓部分一致でもfalse")
	}
	if AuthorMatches("上原", []string{"上原誠"}) {
		t.Errorf("部分名（上原）は完全名（上原誠）と異なるためfalse")
	}
}

func TestHandleNewReleaseSearch_FetchesOnceAndEnqueuesResultForCompleteCandidate(t *testing.T) {
	fetcher := &fakeSearchFetcher{result: SearchResult{Category: SearchOK, Hits: []SearchHit{completeHit("B0FX3X569X")}}}
	deps := baseDeps()
	deps.SearchFetcher = fetcher

	if _, err := HandleNewReleaseSearch(context.Background(), deps, searchJob("海李")); err != nil {
		t.Fatalf("HandleNewReleaseSearch: %v", err)
	}
	if fetcher.calls != 1 {
		t.Errorf("search calls = %d, want 1", fetcher.calls)
	}
	enq := deps.Enqueuer.(*fakeEnqueuer)
	if len(enq.jobs) != 1 || enq.jobs[0].Kind != job.KindNewReleaseResult {
		t.Fatalf("want 1 result job, got %+v", enq.jobs)
	}
	if enq.jobs[0].Target.Product == nil {
		t.Errorf("result job must carry product")
	}
}

func TestHandleNewReleaseSearch_EnqueuesDetailWhenIncomplete(t *testing.T) {
	hit := completeHit("B0FX3X569X")
	hit.HasReleaseDate = false
	fetcher := &fakeSearchFetcher{result: SearchResult{Category: SearchOK, Hits: []SearchHit{hit}}}
	deps := baseDeps()
	deps.SearchFetcher = fetcher

	if _, err := HandleNewReleaseSearch(context.Background(), deps, searchJob("海李")); err != nil {
		t.Fatalf("HandleNewReleaseSearch: %v", err)
	}
	enq := deps.Enqueuer.(*fakeEnqueuer)
	if len(enq.jobs) != 1 || enq.jobs[0].Kind != job.KindNewReleaseDetail {
		t.Fatalf("want 1 detail job, got %+v", enq.jobs)
	}
	if enq.jobs[0].Target.Product != nil {
		t.Errorf("detail job must not carry product")
	}
}

func TestHandleNewReleaseSearch_DoesNotSaveOrNotify(t *testing.T) {
	fetcher := &fakeSearchFetcher{result: SearchResult{Category: SearchOK, Hits: []SearchHit{completeHit("B0FX3X569X")}}}
	deps := baseDeps()
	deps.SearchFetcher = fetcher

	if _, err := HandleNewReleaseSearch(context.Background(), deps, searchJob("海李")); err != nil {
		t.Fatalf("HandleNewReleaseSearch: %v", err)
	}
	notified := deps.NotifiedStore.(*fakeNotifiedStore)
	upcoming := deps.UpcomingStore.(*fakeUpcomingStore)
	notifier := deps.Notifier.(*fakeNotifier)
	if len(notified.upserts) != 0 {
		t.Errorf("search job must not save notified")
	}
	if len(upcoming.upserts) != 0 {
		t.Errorf("search job must not save upcoming")
	}
	if notifier.called {
		t.Errorf("search job must not notify")
	}
}

func TestHandleNewReleaseSearch_SkipsExcludedCandidates(t *testing.T) {
	keyword := completeHit("B0KEYWORD01")
	keyword.Title = "除外タイトル"
	yearMonth := completeHit("B0YEARMONT1")
	yearMonth.Title = "2026年8月号"
	authorMismatch := completeHit("B0AUTHOR001")
	authorMismatch.Contributors = []string{"別人"}
	missing := completeHit("B0MISSING01")
	missing.URL = ""
	fetcher := &fakeSearchFetcher{result: SearchResult{Category: SearchOK, Hits: []SearchHit{keyword, yearMonth, authorMismatch, missing}}}
	deps := baseDeps()
	deps.SearchFetcher = fetcher

	if _, err := HandleNewReleaseSearch(context.Background(), deps, searchJob("海李")); err != nil {
		t.Fatalf("HandleNewReleaseSearch: %v", err)
	}
	enq := deps.Enqueuer.(*fakeEnqueuer)
	if len(enq.jobs) != 0 {
		t.Errorf("excluded candidates must not enqueue, got %+v", enq.jobs)
	}
}

func TestHandleNewReleaseSearch_SkipsNotifiedExisting(t *testing.T) {
	fetcher := &fakeSearchFetcher{result: SearchResult{Category: SearchOK, Hits: []SearchHit{completeHit("B0FX3X569X")}}}
	deps := baseDeps()
	deps.SearchFetcher = fetcher
	deps.NotifiedStore.(*fakeNotifiedStore).exists = true

	if _, err := HandleNewReleaseSearch(context.Background(), deps, searchJob("海李")); err != nil {
		t.Fatalf("HandleNewReleaseSearch: %v", err)
	}
	if got := len(deps.Enqueuer.(*fakeEnqueuer).jobs); got != 0 {
		t.Errorf("notified existing must not enqueue, got %d", got)
	}
}

func TestHandleNewReleaseSearch_CapsCandidatesAtTen(t *testing.T) {
	const total = 12
	hits := make([]SearchHit, total)
	for i := 0; i < total; i++ {
		hits[i] = completeHit(fmt.Sprintf("B0C%07d", i))
	}
	fetcher := &fakeSearchFetcher{result: SearchResult{Category: SearchOK, Hits: hits}}
	deps := baseDeps()
	deps.SearchFetcher = fetcher

	if _, err := HandleNewReleaseSearch(context.Background(), deps, searchJob("海李")); err != nil {
		t.Fatalf("HandleNewReleaseSearch: %v", err)
	}
	enq := deps.Enqueuer.(*fakeEnqueuer)
	if len(enq.jobs) != maxSearchCandidates {
		t.Errorf("enqueued jobs = %d, want %d (先頭10件へ制限)", len(enq.jobs), maxSearchCandidates)
	}
}

func TestHandleNewReleaseSearch_RetryableReturnsError(t *testing.T) {
	deps := baseDeps()
	deps.SearchFetcher.(*fakeSearchFetcher).result = SearchResult{Category: SearchRetryable}

	_, err := HandleNewReleaseSearch(context.Background(), deps, searchJob("海李"))
	if err == nil {
		t.Fatal("retryable must return error")
	}
	var re *ErrRetryableFetch
	if !errors.As(err, &re) {
		t.Errorf("error must be ErrRetryableFetch, got %T", err)
	}
}

func TestHandleNewReleaseDetail_FetchesOnceAndApplies(t *testing.T) {
	info := ProductInfo{
		ASIN: "B0FX3X569X", Title: "タイトル", URL: "https://u",
		CurrentPrice: book.NewPrice(800), ReleaseDate: futureDate, HasReleaseDate: true,
		HasKindleSwatch: true, Contributors: []string{"海李"},
	}
	fetcher := &fakeProductFetcher{result: ProductResult{Category: ProductOK, Info: info}}
	deps := baseDeps()
	deps.ProductFetcher = fetcher
	deps.AuthorStore.(*fakeAuthorStore).changed = true

	if _, err := HandleNewReleaseDetail(context.Background(), deps, detailJob("B0FX3X569X", "海李")); err != nil {
		t.Fatalf("HandleNewReleaseDetail: %v", err)
	}
	if fetcher.calls != 1 {
		t.Errorf("product fetch calls = %d, want 1", fetcher.calls)
	}
	if len(deps.NotifiedStore.(*fakeNotifiedStore).upserts) != 1 {
		t.Errorf("notified upsert count = %d, want 1", len(deps.NotifiedStore.(*fakeNotifiedStore).upserts))
	}
}

func TestHandleNewReleaseDetail_TerminalCasesReturnNil(t *testing.T) {
	cases := []struct {
		name string
		cat  ProductCategory
		info ProductInfo
	}{
		{name: "not_found", cat: ProductNotFound},
		{name: "asin_mismatch", cat: ProductOK, info: ProductInfo{ASIN: "B0DIFFERNT1", HasKindleSwatch: true, CurrentPrice: book.NewPrice(1), HasReleaseDate: true}},
		{name: "not_kindle", cat: ProductOK, info: ProductInfo{ASIN: "B0FX3X569X", Title: "T", HasKindleSwatch: false}},
		{name: "author_mismatch", cat: ProductOK, info: ProductInfo{ASIN: "B0FX3X569X", Title: "T", HasKindleSwatch: true, CurrentPrice: book.NewPrice(1), HasReleaseDate: true, Contributors: []string{"別人"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := baseDeps()
			deps.ProductFetcher.(*fakeProductFetcher).result = ProductResult{Category: tc.cat, Info: tc.info}
			if _, err := HandleNewReleaseDetail(context.Background(), deps, detailJob("B0FX3X569X", "海李")); err != nil {
				t.Fatalf("terminal must return nil: %v", err)
			}
			if len(deps.NotifiedStore.(*fakeNotifiedStore).upserts) != 0 {
				t.Errorf("terminal must not save")
			}
		})
	}
}

func TestHandleNewReleaseDetail_RetryablePriceError(t *testing.T) {
	info := ProductInfo{ASIN: "B0FX3X569X", Title: "T", HasKindleSwatch: true, HasReleaseDate: true} // CurrentPrice Invalid
	deps := baseDeps()
	deps.ProductFetcher.(*fakeProductFetcher).result = ProductResult{Category: ProductOK, Info: info}

	if _, err := HandleNewReleaseDetail(context.Background(), deps, detailJob("B0FX3X569X", "海李")); err == nil {
		t.Fatal("price unavailable must be retryable error")
	}
}

func TestHandleNewReleaseDetail_EmptyTitleIsRetryable(t *testing.T) {
	info := ProductInfo{ASIN: "B0FX3X569X", Title: "", HasKindleSwatch: true, CurrentPrice: book.NewPrice(1), HasReleaseDate: true}
	deps := baseDeps()
	deps.ProductFetcher.(*fakeProductFetcher).result = ProductResult{Category: ProductOK, Info: info}

	if _, err := HandleNewReleaseDetail(context.Background(), deps, detailJob("B0FX3X569X", "海李")); err == nil {
		t.Fatal("empty title must be retryable error (SPEC 13.4)")
	}
}

func futureProduct(asin string) *job.SearchProduct {
	return &job.SearchProduct{
		ASIN: asin, Title: "タイトル", URL: "https://u",
		KindlePrice: 800, ReleaseDate: futureDate, AuthorLabel: "海李",
	}
}

func TestApplyCandidate_FutureReleaseUpsertsNotifiesAndEnqueuesGist(t *testing.T) {
	deps := baseDeps()
	deps.AuthorStore.(*fakeAuthorStore).changed = true

	if _, err := HandleNewReleaseResult(context.Background(), deps, resultJob("B0FX3X569X", "海李", futureProduct("B0FX3X569X"))); err != nil {
		t.Fatalf("HandleNewReleaseResult: %v", err)
	}
	if len(deps.NotifiedStore.(*fakeNotifiedStore).upserts) != 1 {
		t.Errorf("notified upsert count = %d, want 1", len(deps.NotifiedStore.(*fakeNotifiedStore).upserts))
	}
	if len(deps.UpcomingStore.(*fakeUpcomingStore).upserts) != 1 {
		t.Errorf("upcoming upsert count = %d, want 1", len(deps.UpcomingStore.(*fakeUpcomingStore).upserts))
	}
	if !deps.Notifier.(*fakeNotifier).called {
		t.Errorf("未通知なら通知する")
	}
	enq := deps.Enqueuer.(*fakeEnqueuer)
	if len(enq.jobs) != 1 || enq.jobs[0].Kind != job.KindGistUpdate || enq.jobs[0].Target.GistType != "new_release" {
		t.Errorf("Author gist job missing: %+v", enq.jobs)
	}
}

func TestApplyCandidate_PastReleaseSkipsUpsertAndNotify(t *testing.T) {
	product := futureProduct("B0FX3X569X")
	product.ReleaseDate = pastDate
	deps := baseDeps()

	if _, err := HandleNewReleaseResult(context.Background(), deps, resultJob("B0FX3X569X", "海李", product)); err != nil {
		t.Fatalf("HandleNewReleaseResult: %v", err)
	}
	if len(deps.NotifiedStore.(*fakeNotifiedStore).upserts) != 0 {
		t.Errorf("過去発売は notified へ保存しない")
	}
	if deps.Notifier.(*fakeNotifier).called {
		t.Errorf("過去発売は通知しない")
	}
}

func TestApplyCandidate_AlreadyNotifiedSkipsNotifyButUpsertsAndReconciles(t *testing.T) {
	deps := baseDeps()
	deps.NotifiedStore.(*fakeNotifiedStore).alreadyNotified = true
	deps.AuthorStore.(*fakeAuthorStore).changed = true

	if _, err := HandleNewReleaseResult(context.Background(), deps, resultJob("B0FX3X569X", "海李", futureProduct("B0FX3X569X"))); err != nil {
		t.Fatalf("HandleNewReleaseResult: %v", err)
	}
	if deps.Notifier.(*fakeNotifier).called {
		t.Errorf("通知済みは通知しない")
	}
	if len(deps.UpcomingStore.(*fakeUpcomingStore).upserts) != 1 {
		t.Errorf("通知済みでも upcoming へ upsert する")
	}
	enq := deps.Enqueuer.(*fakeEnqueuer)
	if len(enq.jobs) != 1 || enq.jobs[0].Target.GistType != "new_release" {
		t.Errorf("通知済みでも Author gist を省略しない: %+v", enq.jobs)
	}
}

func TestApplyCandidate_NotifiedUpsertFailureReturnsError(t *testing.T) {
	deps := baseDeps()
	deps.NotifiedStore.(*fakeNotifiedStore).upsertErr = errors.New("s3 conflict")

	_, err := HandleNewReleaseResult(context.Background(), deps, resultJob("B0FX3X569X", "海李", futureProduct("B0FX3X569X")))
	if err == nil {
		t.Fatal("notified upsert failure must return error for reconcile")
	}
	if deps.Notifier.(*fakeNotifier).called {
		t.Errorf("must not notify when save failed")
	}
}

func TestApplyCandidate_UpcomingUpsertFailureReturnsError(t *testing.T) {
	deps := baseDeps()
	deps.UpcomingStore.(*fakeUpcomingStore).upsertErr = errors.New("s3 conflict")

	if _, err := HandleNewReleaseResult(context.Background(), deps, resultJob("B0FX3X569X", "海李", futureProduct("B0FX3X569X"))); err == nil {
		t.Fatal("upcoming upsert failure must return error for reconcile")
	}
}

func TestApplyCandidate_NotifyFailureDoesNotRollback(t *testing.T) {
	deps := baseDeps()
	deps.Notifier.(*fakeNotifier).err = errors.New("slack down")

	if _, err := HandleNewReleaseResult(context.Background(), deps, resultJob("B0FX3X569X", "海李", futureProduct("B0FX3X569X"))); err != nil {
		t.Fatalf("notify failure must not fail the job: %v", err)
	}
}

func TestApplyCandidate_AuthorStoreUnchangedStillEnqueuesGist(t *testing.T) {
	deps := baseDeps()
	deps.AuthorStore.(*fakeAuthorStore).changed = false

	if _, err := HandleNewReleaseResult(context.Background(), deps, resultJob("B0FX3X569X", "海李", futureProduct("B0FX3X569X"))); err != nil {
		t.Fatalf("HandleNewReleaseResult: %v", err)
	}
	enq := deps.Enqueuer.(*fakeEnqueuer)
	if len(enq.jobs) != 1 || enq.jobs[0].Kind != job.KindGistUpdate || enq.jobs[0].Target.GistType != "new_release" {
		t.Errorf("Author 変更なしでも gist job を投入する: %+v", enq.jobs)
	}
}

func TestApplyCandidate_GistEnqueueFailureReturnsError(t *testing.T) {
	deps := baseDeps()
	deps.AuthorStore.(*fakeAuthorStore).changed = true
	deps.Enqueuer.(*fakeEnqueuer).err = errors.New("sqs down")

	_, err := HandleNewReleaseResult(context.Background(), deps, resultJob("B0FX3X569X", "海李", futureProduct("B0FX3X569X")))
	if err == nil {
		t.Fatal("gist enqueue failure must return error and not proceed to upsert")
	}
	if len(deps.NotifiedStore.(*fakeNotifiedStore).upserts) != 0 {
		t.Errorf("gist enqueue 失敗時は notified へ upsert しない")
	}
	if len(deps.UpcomingStore.(*fakeUpcomingStore).upserts) != 0 {
		t.Errorf("gist enqueue 失敗時は upcoming へ upsert しない")
	}
}

type reconcileAuthorStore struct {
	calls int
	err   error
}

func (s *reconcileAuthorStore) UpdateLatestRelease(_ context.Context, _ string, _ time.Time, _, _ string) (bool, error) {
	s.calls++
	if s.err != nil {
		return false, s.err
	}
	return s.calls == 1, nil
}

type reconcileEnqueuer struct {
	jobs      []job.Job
	failFirst bool
	called    int
}

func (e *reconcileEnqueuer) Enqueue(_ context.Context, j job.Job) error {
	e.called++
	if e.failFirst && e.called == 1 {
		return errors.New("sqs down")
	}
	e.jobs = append(e.jobs, j)
	return nil
}

func TestApplyCandidate_GistReconcilesAcrossEnqueueFailureAndRedelivery(t *testing.T) {
	author := &reconcileAuthorStore{}
	enq := &reconcileEnqueuer{failFirst: true}
	deps := baseDeps()
	deps.AuthorStore = author
	deps.Enqueuer = enq
	j := resultJob("B0FX3X569X", "海李", futureProduct("B0FX3X569X"))

	oc1, err1 := HandleNewReleaseResult(context.Background(), deps, j)
	if err1 == nil {
		t.Fatalf("first run: gist enqueue failure must return error, got %+v", oc1)
	}
	if oc1.ErrorType != errorTypeEnqueueFailed {
		t.Errorf("first run error_type = %q, want %q", oc1.ErrorType, errorTypeEnqueueFailed)
	}
	if author.calls != 1 {
		t.Errorf("first run: author store calls = %d, want 1", author.calls)
	}
	if len(enq.jobs) != 0 {
		t.Errorf("first run: gist must not be recorded as enqueued on failure")
	}
	if len(deps.NotifiedStore.(*fakeNotifiedStore).upserts) != 0 || len(deps.UpcomingStore.(*fakeUpcomingStore).upserts) != 0 {
		t.Errorf("first run: must not upsert before successful gist enqueue")
	}

	oc2, err2 := HandleNewReleaseResult(context.Background(), deps, j)
	if err2 != nil {
		t.Fatalf("second run (redelivery): must succeed with gist reconciled: %v", err2)
	}
	if oc2.Result != execution.ResultCompleted {
		t.Errorf("second run result = %v, want completed", oc2.Result)
	}
	if author.calls != 2 {
		t.Errorf("second run: author store calls = %d, want 2", author.calls)
	}
	if len(enq.jobs) != 1 || enq.jobs[0].Kind != job.KindGistUpdate || enq.jobs[0].Target.GistType != "new_release" {
		t.Fatalf("second run: gist job must be enqueued even with authorChanged=false: %+v", enq.jobs)
	}
	wantID := buildAuthorGistJob(j, "B0FX3X569X").JobID
	if enq.jobs[0].JobID != wantID {
		t.Errorf("second run gist JobID = %q, want deterministic %q", enq.jobs[0].JobID, wantID)
	}
}

func TestApplyCandidate_PastReleaseWithAuthorChangedEnqueuesGist(t *testing.T) {
	product := futureProduct("B0FX3X569X")
	product.ReleaseDate = pastDate
	deps := baseDeps()
	deps.AuthorStore.(*fakeAuthorStore).changed = true

	if _, err := HandleNewReleaseResult(context.Background(), deps, resultJob("B0FX3X569X", "海李", product)); err != nil {
		t.Fatalf("HandleNewReleaseResult: %v", err)
	}
	enq := deps.Enqueuer.(*fakeEnqueuer)
	if len(enq.jobs) != 1 || enq.jobs[0].Target.GistType != "new_release" {
		t.Errorf("過去分でも Author 変更時は gist job を投入する: %+v", enq.jobs)
	}
	if len(deps.NotifiedStore.(*fakeNotifiedStore).upserts) != 0 {
		t.Errorf("過去分は notified へ保存しない")
	}
	if deps.Notifier.(*fakeNotifier).called {
		t.Errorf("過去分は通知しない")
	}
}

func TestApplyCandidate_LatestReleaseURLIsCleanedOfAffiliateQuery(t *testing.T) {
	deps := baseDeps()
	deps.AuthorStore.(*fakeAuthorStore).changed = true
	taggedURL := "https://www.amazon.co.jp/dp/B0FX3X569X?tag=partner-22#frag"
	product := futureProduct("B0FX3X569X")
	product.URL = taggedURL

	if _, err := HandleNewReleaseResult(context.Background(), deps, resultJob("B0FX3X569X", "海李", product)); err != nil {
		t.Fatalf("HandleNewReleaseResult: %v", err)
	}
	gotURL := deps.AuthorStore.(*fakeAuthorStore).gotURL
	const wantURL = "https://www.amazon.co.jp/dp/B0FX3X569X"
	if gotURL != wantURL {
		t.Errorf("LatestReleaseURL = %q, want %q (affiliate query/fragment 除去)", gotURL, wantURL)
	}
	notified := deps.NotifiedStore.(*fakeNotifiedStore)
	if len(notified.upserts) != 1 || notified.upserts[0].URL != taggedURL {
		t.Errorf("notified URL は affiliate tag を保持する: got %+v", notified.upserts)
	}
}

func TestBuildJobs_AreDeterministicAndUseAmazonRequests(t *testing.T) {
	j := searchJob("海李")
	r := buildResultJob(j, completeHit("B0FX3X569X"))
	d := buildDetailJob(j, completeHit("B0FX3X569X"))
	if got := job.MessageGroup(r.Kind); got != "amazon-requests" {
		t.Errorf("result MessageGroup = %q, want amazon-requests", got)
	}
	if got := job.MessageGroup(d.Kind); got != "amazon-requests" {
		t.Errorf("detail MessageGroup = %q, want amazon-requests", got)
	}
	if buildResultJob(j, completeHit("B0FX3X569X")).JobID != r.JobID {
		t.Errorf("result JobID is not deterministic")
	}
	if buildDetailJob(j, completeHit("B0FX3X569X")).JobID != d.JobID {
		t.Errorf("detail JobID is not deterministic")
	}
}

func TestBuildAuthorGistJob_DiscriminatorIsDeterministic(t *testing.T) {
	const author = "海李"
	candA := buildAuthorGistJob(detailJob("B0FX3X569X", author), "B0FX3X569X")
	candB := buildAuthorGistJob(detailJob("B0FX3X5700", author), "B0FX3X5700")
	if candA.JobID == candB.JobID {
		t.Errorf("same author different candidates must differ: %s", candA.JobID)
	}
	if scheduling.DedupID(candA.JobID) == scheduling.DedupID(candB.JobID) {
		t.Errorf("same author different candidates dedup must differ")
	}
	if buildAuthorGistJob(detailJob("B0FX3X569X", author), "B0FX3X569X").JobID != candA.JobID {
		t.Errorf("same candidate retry must be deterministic")
	}
	other := buildAuthorGistJob(detailJob("B0FX3X569X", "佐藤"), "B0FX3X569X")
	if candA.JobID == other.JobID {
		t.Errorf("different authors must differ: %s", candA.JobID)
	}
	if scheduling.DedupID(candA.JobID) == scheduling.DedupID(other.JobID) {
		t.Errorf("different authors dedup must differ")
	}
	if got := job.MessageGroup(candA.Kind); got != "external-updates" {
		t.Errorf("MessageGroup = %q, want external-updates", got)
	}
	if candA.Target.GistType != gistNewRelID {
		t.Errorf("GistType = %q, want %q", candA.Target.GistType, gistNewRelID)
	}
}

func TestHandleNewReleaseSearch_SearchEmptyReturnsRetryableError(t *testing.T) {
	deps := baseDeps()
	deps.SearchFetcher.(*fakeSearchFetcher).result = SearchResult{Category: SearchEmpty, HTTPStatus: 200, ResponseBytes: 7}

	oc, err := HandleNewReleaseSearch(context.Background(), deps, searchJob("海李"))
	if err == nil {
		t.Fatal("search empty must return retryable error")
	}
	if oc.Result != execution.ResultError {
		t.Errorf("result = %v, want error (retryable)", oc.Result)
	}
	if oc.ErrorType != errorTypeSearchEmpty {
		t.Errorf("error_type = %q, want %q", oc.ErrorType, errorTypeSearchEmpty)
	}
	if oc.HTTPStatus != 200 || oc.ResponseBytes != 7 {
		t.Errorf("HTTPStatus=%d ResponseBytes=%d, want 200/7", oc.HTTPStatus, oc.ResponseBytes)
	}
}

func TestHandleNewReleaseSearch_FetchErrorReturnsError(t *testing.T) {
	deps := baseDeps()
	deps.SearchFetcher.(*fakeSearchFetcher).err = errors.New("dns failure")

	oc, err := HandleNewReleaseSearch(context.Background(), deps, searchJob("海李"))
	if err == nil {
		t.Fatal("fetch error must return error")
	}
	if oc.Result != execution.ResultError {
		t.Errorf("result = %v, want error", oc.Result)
	}
	if oc.ErrorType != errorTypeFetchError {
		t.Errorf("error_type = %q, want %q", oc.ErrorType, errorTypeFetchError)
	}
}

func TestHandleNewReleaseSearch_RetryableCategoryReturnsErrRetryableFetch(t *testing.T) {
	deps := baseDeps()
	deps.SearchFetcher.(*fakeSearchFetcher).result = SearchResult{Category: SearchRetryable, HTTPStatus: 503}

	_, err := HandleNewReleaseSearch(context.Background(), deps, searchJob("海李"))
	if err == nil {
		t.Fatal("retryable category must return error")
	}
	var re *ErrRetryableFetch
	if !errors.As(err, &re) {
		t.Errorf("error must be ErrRetryableFetch, got %T", err)
	}
}

func TestHandleNewReleaseSearch_UnknownCategoryReturnsError(t *testing.T) {
	deps := baseDeps()
	deps.SearchFetcher.(*fakeSearchFetcher).result = SearchResult{Category: SearchCategory(99)}

	oc, err := HandleNewReleaseSearch(context.Background(), deps, searchJob("海李"))
	if err == nil {
		t.Fatal("unknown category must return error")
	}
	if oc.ErrorType != errorTypeUnknownCategory {
		t.Errorf("error_type = %q, want %q", oc.ErrorType, errorTypeUnknownCategory)
	}
}

func TestHandleNewReleaseSearch_EnqueueFailureReturnsError(t *testing.T) {
	fetcher := &fakeSearchFetcher{result: SearchResult{Category: SearchOK, Hits: []SearchHit{completeHit("B0FX3X569X")}}}
	deps := baseDeps()
	deps.SearchFetcher = fetcher
	deps.Enqueuer.(*fakeEnqueuer).err = errors.New("sqs down")

	oc, err := HandleNewReleaseSearch(context.Background(), deps, searchJob("海李"))
	if err == nil {
		t.Fatal("enqueue failure must return error")
	}
	if oc.ErrorType != errorTypeEnqueueFailed {
		t.Errorf("error_type = %q, want %q", oc.ErrorType, errorTypeEnqueueFailed)
	}
}

func TestHandleNewReleaseResult_MissingProductReturnsError(t *testing.T) {
	deps := baseDeps()
	j := job.Job{Version: job.Version, JobID: "r", Kind: job.KindNewReleaseResult,
		CheckType: job.CheckNewRelease, CycleID: "nr:c",
		Target: job.Target{ASIN: "B0FX3X569X", AuthorName: "海李"}}

	oc, err := HandleNewReleaseResult(context.Background(), deps, j)
	if err == nil {
		t.Fatal("missing product must return error")
	}
	if oc.ErrorType != errorTypeMissingProduct {
		t.Errorf("error_type = %q, want %q", oc.ErrorType, errorTypeMissingProduct)
	}
}

func TestHandleNewReleaseDetail_DateUnavailableIsRetryable(t *testing.T) {
	info := ProductInfo{
		ASIN: "B0FX3X569X", Title: "T", HasKindleSwatch: true,
		CurrentPrice: book.NewPrice(800), HasReleaseDate: false,
	}
	deps := baseDeps()
	deps.ProductFetcher.(*fakeProductFetcher).result = ProductResult{Category: ProductOK, Info: info}

	oc, err := HandleNewReleaseDetail(context.Background(), deps, detailJob("B0FX3X569X", "海李"))
	if err == nil {
		t.Fatal("date unavailable must be retryable error")
	}
	if oc.Result != execution.ResultError {
		t.Errorf("result = %v, want error (retryable)", oc.Result)
	}
	if oc.ErrorType != errorTypeDateUnavailable {
		t.Errorf("error_type = %q, want %q", oc.ErrorType, errorTypeDateUnavailable)
	}
}

func TestHandleNewReleaseDetail_PermanentClientErrorIsTerminal(t *testing.T) {
	deps := baseDeps()
	deps.ProductFetcher.(*fakeProductFetcher).result = ProductResult{Category: ProductPermanentClientError, HTTPStatus: 400}

	oc, err := HandleNewReleaseDetail(context.Background(), deps, detailJob("B0FX3X569X", "海李"))
	if err != nil {
		t.Fatalf("terminal must return nil error: %v", err)
	}
	if oc.Result != execution.ResultTerminal {
		t.Errorf("result = %v, want terminal", oc.Result)
	}
	if oc.ErrorType != "permanent_client_error" {
		t.Errorf("error_type = %q, want permanent_client_error", oc.ErrorType)
	}
	if len(deps.NotifiedStore.(*fakeNotifiedStore).upserts) != 0 {
		t.Errorf("terminal must not save")
	}
}

func TestHandleNewReleaseDetail_NotFoundIsTerminal(t *testing.T) {
	deps := baseDeps()
	deps.ProductFetcher.(*fakeProductFetcher).result = ProductResult{Category: ProductNotFound, HTTPStatus: 404}

	oc, err := HandleNewReleaseDetail(context.Background(), deps, detailJob("B0FX3X569X", "海李"))
	if err != nil {
		t.Fatalf("terminal must return nil error: %v", err)
	}
	if oc.Result != execution.ResultTerminal {
		t.Errorf("result = %v, want terminal", oc.Result)
	}
	if oc.ErrorType != "not_found" {
		t.Errorf("error_type = %q, want not_found", oc.ErrorType)
	}
}

func TestHandleNewReleaseDetail_ExcludedKeywordAndYearMonthAreTerminal(t *testing.T) {
	cases := []struct {
		name  string
		title string
	}{
		{name: "除外キーワードはexcluded terminal", title: "除外タイトル"},
		{name: "年月パターンはexcluded terminal", title: "2026年8月号"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info := ProductInfo{
				ASIN: "B0FX3X569X", Title: tc.title, HasKindleSwatch: true,
				CurrentPrice: book.NewPrice(800), ReleaseDate: futureDate, HasReleaseDate: true,
				Contributors: []string{"海李"},
			}
			deps := baseDeps()
			deps.ProductFetcher.(*fakeProductFetcher).result = ProductResult{Category: ProductOK, Info: info}

			oc, err := HandleNewReleaseDetail(context.Background(), deps, detailJob("B0FX3X569X", "海李"))
			if err != nil {
				t.Fatalf("excluded must return nil error: %v", err)
			}
			if oc.Result != execution.ResultTerminal {
				t.Errorf("result = %v, want terminal", oc.Result)
			}
			if oc.ErrorType != errorTypeExcluded {
				t.Errorf("error_type = %q, want %q", oc.ErrorType, errorTypeExcluded)
			}
			if len(deps.NotifiedStore.(*fakeNotifiedStore).upserts) != 0 {
				t.Errorf("excluded must not save")
			}
		})
	}
}

func TestHandleNewReleaseDetail_FetchErrorReturnsError(t *testing.T) {
	deps := baseDeps()
	deps.ProductFetcher.(*fakeProductFetcher).err = errors.New("timeout")

	oc, err := HandleNewReleaseDetail(context.Background(), deps, detailJob("B0FX3X569X", "海李"))
	if err == nil {
		t.Fatal("fetch error must return error")
	}
	if oc.ErrorType != errorTypeFetchError {
		t.Errorf("error_type = %q, want %q", oc.ErrorType, errorTypeFetchError)
	}
}

func TestHandleNewReleaseDetail_RetryableCategoryReturnsErrRetryableFetch(t *testing.T) {
	deps := baseDeps()
	deps.ProductFetcher.(*fakeProductFetcher).result = ProductResult{Category: ProductRetryable, HTTPStatus: 503}

	oc, err := HandleNewReleaseDetail(context.Background(), deps, detailJob("B0FX3X569X", "海李"))
	if err == nil {
		t.Fatal("retryable category must return error")
	}
	if oc.Result != execution.ResultError {
		t.Errorf("result = %v, want error (retryable)", oc.Result)
	}
	if oc.ErrorType != errorTypeFetchRetryable {
		t.Errorf("error_type = %q, want %q", oc.ErrorType, errorTypeFetchRetryable)
	}
	var re *ErrRetryableFetch
	if !errors.As(err, &re) {
		t.Errorf("error must be ErrRetryableFetch, got %T", err)
	}
	if re.Error() == "" {
		t.Errorf("ErrRetryableFetch.Error() must be non-empty")
	}
}

func TestHandleNewReleaseDetail_UnknownCategoryReturnsError(t *testing.T) {
	deps := baseDeps()
	deps.ProductFetcher.(*fakeProductFetcher).result = ProductResult{Category: ProductCategory(99)}

	oc, err := HandleNewReleaseDetail(context.Background(), deps, detailJob("B0FX3X569X", "海李"))
	if err == nil {
		t.Fatal("unknown category must return error")
	}
	if oc.ErrorType != errorTypeUnknownCategory {
		t.Errorf("error_type = %q, want %q", oc.ErrorType, errorTypeUnknownCategory)
	}
}

func TestHandleNewReleaseSearch_RoutesNonKindleAndInvalidPriceToDetail(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(h *SearchHit)
	}{
		{name: "非Kindle(種別不明)はdetailへ", mutate: func(h *SearchHit) { h.IsKindle = false }},
		{name: "Kindle価格不正はdetailへ", mutate: func(h *SearchHit) { h.KindlePrice = book.UnknownPrice() }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hit := completeHit("B0FX3X569X")
			tc.mutate(&hit)
			fetcher := &fakeSearchFetcher{result: SearchResult{Category: SearchOK, Hits: []SearchHit{hit}}}
			deps := baseDeps()
			deps.SearchFetcher = fetcher

			if _, err := HandleNewReleaseSearch(context.Background(), deps, searchJob("海李")); err != nil {
				t.Fatalf("HandleNewReleaseSearch: %v", err)
			}
			enq := deps.Enqueuer.(*fakeEnqueuer)
			if len(enq.jobs) != 1 || enq.jobs[0].Kind != job.KindNewReleaseDetail {
				t.Fatalf("want 1 detail job, got %+v", enq.jobs)
			}
			if enq.jobs[0].Target.Product != nil {
				t.Errorf("detail job must not carry product")
			}
		})
	}
}

func TestHandleNewReleaseSearch_NotifiedExistsErrorPropagates(t *testing.T) {
	fetcher := &fakeSearchFetcher{result: SearchResult{Category: SearchOK, Hits: []SearchHit{completeHit("B0FX3X569X")}}}
	deps := baseDeps()
	deps.SearchFetcher = fetcher
	deps.NotifiedStore.(*fakeNotifiedStore).existsErr = errors.New("s3 get failed")

	oc, err := HandleNewReleaseSearch(context.Background(), deps, searchJob("海李"))
	if err == nil {
		t.Fatal("notified exists error must propagate")
	}
	if oc.ErrorType != errorTypeEnqueueFailed {
		t.Errorf("error_type = %q, want %q", oc.ErrorType, errorTypeEnqueueFailed)
	}
}

func TestIsFutureRelease_Boundary(t *testing.T) {
	now := fixedClock()
	if IsFutureRelease(now, now) {
		t.Errorf("release == now must not be future (After is strict)")
	}
	equalMidday := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	if !IsFutureRelease(equalMidday, now) {
		t.Errorf("release later same day must be future")
	}
	nextDay := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	if !IsFutureRelease(nextDay, now) {
		t.Errorf("release next day must be future")
	}
}

func TestNormalizeAuthorName_EmptyAndMixedSpaces(t *testing.T) {
	if got := NormalizeAuthorName(""); got != "" {
		t.Errorf("empty = %q, want empty", got)
	}
	if got := NormalizeAuthorName(" 海　李 "); got != "海李" {
		t.Errorf("mixed leading/trailing spaces = %q, want 海李", got)
	}
}

func TestAuthorMatches_FullWidthRoleParenAndEmptyAuthor(t *testing.T) {
	if !AuthorMatches("海李", []string{"海李（著）"}) {
		t.Errorf("全角役割括弧（著）を除去したcontributorと一致する場合はtrue")
	}
	if !AuthorMatches("海李", []string{"海李（イラスト）"}) {
		t.Errorf("全角役割括弧（イラスト）を除去しても一致する場合はtrue")
	}
	if AuthorMatches("", []string{"海李"}) {
		t.Errorf("対象作者名空はfalse")
	}
}

func TestApplyCandidate_UpcomingFailureSkipsNotify(t *testing.T) {
	deps := baseDeps()
	deps.UpcomingStore.(*fakeUpcomingStore).upsertErr = errors.New("s3 conflict")

	if _, err := HandleNewReleaseResult(context.Background(), deps, resultJob("B0FX3X569X", "海李", futureProduct("B0FX3X569X"))); err == nil {
		t.Fatal("upcoming failure must return error")
	}
	if deps.Notifier.(*fakeNotifier).called {
		t.Errorf("must not notify when upcoming save failed")
	}
	if len(deps.NotifiedStore.(*fakeNotifiedStore).upserts) != 1 {
		t.Errorf("notified must be saved before upcoming")
	}
}

func TestApplyCandidate_RetentionFailureReturnsError(t *testing.T) {
	deps := baseDeps()
	deps.NotifiedStore.(*fakeNotifiedStore).retentionErr = errors.New("s3 get failed")

	oc, err := HandleNewReleaseResult(context.Background(), deps, resultJob("B0FX3X569X", "海李", futureProduct("B0FX3X569X")))
	if err == nil {
		t.Fatal("retention failure must return error")
	}
	if oc.ErrorType != errorTypeRetention {
		t.Errorf("error_type = %q, want %q", oc.ErrorType, errorTypeRetention)
	}
}

func TestApplyCandidate_AuthorStoreFailureReturnsError(t *testing.T) {
	deps := baseDeps()
	deps.AuthorStore.(*fakeAuthorStore).err = errors.New("authors.json conflict")

	oc, err := HandleNewReleaseResult(context.Background(), deps, resultJob("B0FX3X569X", "海李", futureProduct("B0FX3X569X")))
	if err == nil {
		t.Fatal("author store failure must return error")
	}
	if oc.ErrorType != errorTypeAuthorStore {
		t.Errorf("error_type = %q, want %q", oc.ErrorType, errorTypeAuthorStore)
	}
}

func TestHandleNewReleaseSearch_RoutesISBNCandidateToPaperDetail(t *testing.T) {
	isbn := completeHit("1234567890123")
	fetcher := &fakeSearchFetcher{result: SearchResult{Category: SearchOK, Hits: []SearchHit{isbn}}}
	deps := baseDeps()
	deps.SearchFetcher = fetcher

	if _, err := HandleNewReleaseSearch(context.Background(), deps, searchJob("海李")); err != nil {
		t.Fatalf("HandleNewReleaseSearch: %v", err)
	}
	enq := deps.Enqueuer.(*fakeEnqueuer)
	if len(enq.jobs) != 1 || enq.jobs[0].Kind != job.KindNewReleasePaperDetail {
		t.Fatalf("ISBN candidate must enqueue new_release_paper_detail, got %+v", enq.jobs)
	}
	if enq.jobs[0].Target.ASIN != "1234567890123" {
		t.Errorf("paper detail ASIN = %q, want ISBN", enq.jobs[0].Target.ASIN)
	}
}

func TestHandleNewReleaseSearch_ISBNCandidateSkipsNotifiedCheck(t *testing.T) {
	isbn := completeHit("1234567890")
	fetcher := &fakeSearchFetcher{result: SearchResult{Category: SearchOK, Hits: []SearchHit{isbn}}}
	deps := baseDeps()
	deps.SearchFetcher = fetcher
	deps.NotifiedStore.(*fakeNotifiedStore).exists = true

	if _, err := HandleNewReleaseSearch(context.Background(), deps, searchJob("海李")); err != nil {
		t.Fatalf("HandleNewReleaseSearch: %v", err)
	}
	if got := len(deps.Enqueuer.(*fakeEnqueuer).jobs); got != 1 {
		t.Errorf("ISBN candidate must still enqueue paper detail when notified exists, got %d", got)
	}
}

func TestHandleNewReleasePaperDetail_FetchesOnceAndUpsertsPaperBook(t *testing.T) {
	fetcher := &fakePaperPageFetcher{result: PaperPageResult{Category: ProductOK, Info: paperPaperPageInfo("1234567890")}}
	store := &fakePaperCandidateStore{changed: true}
	deps := baseDeps()
	deps.PaperPageFetcher = fetcher
	deps.PaperCandidateStore = store

	if _, err := HandleNewReleasePaperDetail(context.Background(), deps, paperDetailJob("1234567890", "海李")); err != nil {
		t.Fatalf("HandleNewReleasePaperDetail: %v", err)
	}
	if fetcher.calls != 1 {
		t.Errorf("paper page fetch calls = %d, want 1", fetcher.calls)
	}
	if len(store.upserts) != 1 {
		t.Fatalf("paper upsert count = %d, want 1", len(store.upserts))
	}
	got := store.upserts[0]
	if got.CurrentPrice.Yen() != 900 || got.MaxPrice.Yen() != 900 {
		t.Errorf("paper price not set as CurrentPrice=MaxPrice: %+v", got)
	}
}

func TestHandleNewReleasePaperDetail_TerminalCasesReturnNil(t *testing.T) {
	cases := []struct {
		name string
		cat  ProductCategory
		info PaperPageInfo
	}{
		{name: "not_found", cat: ProductNotFound},
		{name: "asin_mismatch", cat: ProductOK, info: PaperPageInfo{ASIN: "9999999999", Title: "T", HasReleaseDate: true, Contributors: []string{"海李"}}},
		{name: "author_mismatch", cat: ProductOK, info: PaperPageInfo{ASIN: "1234567890", Title: "T", HasReleaseDate: true, Contributors: []string{"別人"}}},
		{name: "excluded_keyword", cat: ProductOK, info: PaperPageInfo{ASIN: "1234567890", Title: "除外タイトル", HasReleaseDate: true, Contributors: []string{"海李"}}},
		{name: "excluded_year_month", cat: ProductOK, info: PaperPageInfo{ASIN: "1234567890", Title: "2026年8月号", HasReleaseDate: true, Contributors: []string{"海李"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := baseDeps()
			deps.PaperPageFetcher.(*fakePaperPageFetcher).result = PaperPageResult{Category: tc.cat, Info: tc.info}
			if _, err := HandleNewReleasePaperDetail(context.Background(), deps, paperDetailJob("1234567890", "海李")); err != nil {
				t.Fatalf("terminal must return nil: %v", err)
			}
			if len(deps.PaperCandidateStore.(*fakePaperCandidateStore).upserts) != 0 {
				t.Errorf("terminal must not upsert paper book")
			}
		})
	}
}

func TestHandleNewReleasePaperDetail_EmptyTitleIsRetryable(t *testing.T) {
	info := PaperPageInfo{ASIN: "1234567890", Title: "", HasReleaseDate: true, Contributors: []string{"海李"}}
	deps := baseDeps()
	deps.PaperPageFetcher.(*fakePaperPageFetcher).result = PaperPageResult{Category: ProductOK, Info: info}

	if _, err := HandleNewReleasePaperDetail(context.Background(), deps, paperDetailJob("1234567890", "海李")); err == nil {
		t.Fatal("empty title must be retryable error")
	}
}

func TestHandleNewReleasePaperDetail_DateUnavailableIsRetryable(t *testing.T) {
	info := PaperPageInfo{ASIN: "1234567890", Title: "T", HasReleaseDate: false, Contributors: []string{"海李"}}
	deps := baseDeps()
	deps.PaperPageFetcher.(*fakePaperPageFetcher).result = PaperPageResult{Category: ProductOK, Info: info}

	if _, err := HandleNewReleasePaperDetail(context.Background(), deps, paperDetailJob("1234567890", "海李")); err == nil {
		t.Fatal("release date unavailable must be retryable error")
	}
}

func TestHandleNewReleasePaperDetail_PriceUnknownSavesZeroAndEnqueuesGist(t *testing.T) {
	info := paperPaperPageInfo("1234567890")
	info.PaperPrice = book.UnknownPrice()
	store := &fakePaperCandidateStore{changed: true}
	deps := baseDeps()
	deps.PaperPageFetcher.(*fakePaperPageFetcher).result = PaperPageResult{Category: ProductOK, Info: info}
	deps.PaperCandidateStore = store

	if _, err := HandleNewReleasePaperDetail(context.Background(), deps, paperDetailJob("1234567890", "海李")); err != nil {
		t.Fatalf("HandleNewReleasePaperDetail: %v", err)
	}
	got := store.upserts[0]
	if got.CurrentPrice.Valid() || got.MaxPrice.Valid() {
		t.Errorf("unknown paper price must save 0/0, got %+v", got)
	}
	enq := deps.Enqueuer.(*fakeEnqueuer)
	if len(enq.jobs) != 1 || enq.jobs[0].Target.GistType != "paper_to_kindle" {
		t.Errorf("changed paper_books must enqueue paper gist, got %+v", enq.jobs)
	}
}

func TestHandleNewReleasePaperDetail_EnqueuesGistEvenWhenUnchanged(t *testing.T) {
	store := &fakePaperCandidateStore{changed: false}
	deps := baseDeps()
	deps.PaperPageFetcher.(*fakePaperPageFetcher).result = PaperPageResult{Category: ProductOK, Info: paperPaperPageInfo("1234567890")}
	deps.PaperCandidateStore = store

	if _, err := HandleNewReleasePaperDetail(context.Background(), deps, paperDetailJob("1234567890", "海李")); err != nil {
		t.Fatalf("HandleNewReleasePaperDetail: %v", err)
	}
	if len(store.upserts) != 1 {
		t.Errorf("upsert still runs to keep idempotency, got %d", len(store.upserts))
	}
	enq := deps.Enqueuer.(*fakeEnqueuer)
	if len(enq.jobs) != 1 || enq.jobs[0].Target.GistType != "paper_to_kindle" {
		t.Errorf("upsert success must enqueue paper gist even when changed=false: %+v", enq.jobs)
	}
}

func TestHandleNewReleasePaperDetail_DoesNotTouchKindleListsOrAuthors(t *testing.T) {
	store := &fakePaperCandidateStore{changed: true}
	deps := baseDeps()
	deps.PaperPageFetcher.(*fakePaperPageFetcher).result = PaperPageResult{Category: ProductOK, Info: paperPaperPageInfo("1234567890")}
	deps.PaperCandidateStore = store

	if _, err := HandleNewReleasePaperDetail(context.Background(), deps, paperDetailJob("1234567890", "海李")); err != nil {
		t.Fatalf("HandleNewReleasePaperDetail: %v", err)
	}
	if len(deps.NotifiedStore.(*fakeNotifiedStore).upserts) != 0 {
		t.Errorf("paper detail must not upsert notified")
	}
	if len(deps.UpcomingStore.(*fakeUpcomingStore).upserts) != 0 {
		t.Errorf("paper detail must not upsert upcoming")
	}
	if deps.Notifier.(*fakeNotifier).called {
		t.Errorf("paper detail must not notify")
	}
	if deps.AuthorStore.(*fakeAuthorStore).gotAuthor != "" {
		t.Errorf("paper detail must not update authors")
	}
}

func TestHandleNewReleasePaperDetail_StoreFailureReturnsError(t *testing.T) {
	store := &fakePaperCandidateStore{upsertErr: errors.New("paper_books s3 conflict")}
	deps := baseDeps()
	deps.PaperPageFetcher.(*fakePaperPageFetcher).result = PaperPageResult{Category: ProductOK, Info: paperPaperPageInfo("1234567890")}
	deps.PaperCandidateStore = store

	oc, err := HandleNewReleasePaperDetail(context.Background(), deps, paperDetailJob("1234567890", "海李"))
	if err == nil {
		t.Fatal("store failure must return error")
	}
	if oc.ErrorType != errorTypePaperStore {
		t.Errorf("error_type = %q, want %q", oc.ErrorType, errorTypePaperStore)
	}
}

func TestHandleNewReleasePaperDetail_MinPriceExcludesPaperPriceAtOrBelow(t *testing.T) {
	cases := []struct {
		name     string
		price    float64
		excluded bool
	}{
		{name: "220は除外", price: 220, excluded: true},
		{name: "221は除外", price: 221, excluded: true},
		{name: "222は通過", price: 222, excluded: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info := paperPaperPageInfo("1234567890")
			info.PaperPrice = book.NewPrice(tc.price)
			store := &fakePaperCandidateStore{changed: true}
			deps := baseDeps()
			deps.Config.MinPrice = 221
			deps.PaperPageFetcher.(*fakePaperPageFetcher).result = PaperPageResult{Category: ProductOK, Info: info}
			deps.PaperCandidateStore = store

			oc, err := HandleNewReleasePaperDetail(context.Background(), deps, paperDetailJob("1234567890", "海李"))
			if err != nil {
				t.Fatalf("HandleNewReleasePaperDetail: %v", err)
			}
			if tc.excluded {
				if oc.Result != execution.ResultTerminal || oc.ErrorType != errorTypeMinPriceExcluded {
					t.Errorf("price %v must be excluded: %+v", tc.price, oc)
				}
				if len(store.upserts) != 0 {
					t.Errorf("excluded paper must not upsert")
				}
			} else {
				if len(store.upserts) != 1 {
					t.Errorf("price %v must upsert paper book", tc.price)
				}
			}
		})
	}
}

func TestApplyCandidate_MinPriceExcludesKindleAtOrBelow(t *testing.T) {
	cases := []struct {
		name     string
		price    float64
		excluded bool
	}{
		{name: "220は除外", price: 220, excluded: true},
		{name: "221は除外", price: 221, excluded: true},
		{name: "222は通過", price: 222, excluded: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			product := futureProduct("B0FX3X569X")
			product.KindlePrice = tc.price
			deps := baseDeps()
			deps.Config.MinPrice = 221

			oc, err := HandleNewReleaseResult(context.Background(), deps, resultJob("B0FX3X569X", "海李", product))
			if err != nil {
				t.Fatalf("HandleNewReleaseResult: %v", err)
			}
			if tc.excluded {
				if oc.Result != execution.ResultTerminal || oc.ErrorType != errorTypeMinPriceExcluded {
					t.Errorf("Kindle price %v must be excluded: %+v", tc.price, oc)
				}
				if len(deps.NotifiedStore.(*fakeNotifiedStore).upserts) != 0 {
					t.Errorf("excluded Kindle must not upsert notified")
				}
			} else {
				if len(deps.NotifiedStore.(*fakeNotifiedStore).upserts) != 1 {
					t.Errorf("Kindle price %v must upsert notified", tc.price)
				}
			}
		})
	}
}

func TestHandleNewReleasePaperDetail_RetryableCategoryReturnsErrRetryableFetch(t *testing.T) {
	deps := baseDeps()
	deps.PaperPageFetcher.(*fakePaperPageFetcher).result = PaperPageResult{Category: ProductRetryable}

	_, err := HandleNewReleasePaperDetail(context.Background(), deps, paperDetailJob("1234567890", "海李"))
	if err == nil {
		t.Fatal("retryable must return error")
	}
}

func TestHandleNewReleasePaperDetail_GistJobIdIsDeterministicPerASIN(t *testing.T) {
	store := &fakePaperCandidateStore{changed: true}
	deps := baseDeps()
	deps.PaperPageFetcher.(*fakePaperPageFetcher).result = PaperPageResult{Category: ProductOK, Info: paperPaperPageInfo("1234567890")}
	deps.PaperCandidateStore = store

	if _, err := HandleNewReleasePaperDetail(context.Background(), deps, paperDetailJob("1234567890", "海李")); err != nil {
		t.Fatalf("HandleNewReleasePaperDetail: %v", err)
	}
	enq := deps.Enqueuer.(*fakeEnqueuer)
	if len(enq.jobs) != 1 {
		t.Fatalf("want 1 gist job, got %d", len(enq.jobs))
	}
	want := scheduling.JobID(string(job.KindGistUpdate), "nr:c", "paper_to_kindle:nr_paper:1234567890")
	if enq.jobs[0].JobID != want {
		t.Errorf("gist job_id = %q, want %q", enq.jobs[0].JobID, want)
	}
}

type reconcilePaperStore struct {
	upserts int
}

func (s *reconcilePaperStore) UpsertChanged(_ context.Context, b book.KindleBook) (bool, error) {
	s.upserts++
	if s.upserts == 1 {
		return true, nil
	}
	return false, nil
}

func (s *reconcilePaperStore) Exists(_ context.Context, _ string) (bool, error) {
	return false, nil
}

func TestHandleNewReleasePaperDetail_GistReconcilesAcrossEnqueueFailureAndRedelivery(t *testing.T) {
	store := &reconcilePaperStore{}
	enq := &reconcileEnqueuer{failFirst: true}
	deps := baseDeps()
	deps.PaperPageFetcher.(*fakePaperPageFetcher).result = PaperPageResult{Category: ProductOK, Info: paperPaperPageInfo("1234567890")}
	deps.PaperCandidateStore = store
	deps.Enqueuer = enq
	j := paperDetailJob("1234567890", "海李")

	oc1, err1 := HandleNewReleasePaperDetail(context.Background(), deps, j)
	if err1 == nil {
		t.Fatalf("first run: paper saved then gist enqueue failure must return error, got %+v", oc1)
	}
	if oc1.ErrorType != errorTypeEnqueueFailed {
		t.Errorf("first run error_type = %q, want %q", oc1.ErrorType, errorTypeEnqueueFailed)
	}
	if store.upserts != 1 {
		t.Errorf("first run: paper upsert count = %d, want 1", store.upserts)
	}
	if len(enq.jobs) != 0 {
		t.Errorf("first run: gist must not be recorded as enqueued on failure")
	}

	oc2, err2 := HandleNewReleasePaperDetail(context.Background(), deps, j)
	if err2 != nil {
		t.Fatalf("second run (redelivery): must succeed with gist reconciled: %v", err2)
	}
	if oc2.Result != execution.ResultCompleted {
		t.Errorf("second run result = %v, want completed", oc2.Result)
	}
	if store.upserts != 2 {
		t.Errorf("second run: paper upsert count = %d, want 2", store.upserts)
	}
	if len(enq.jobs) != 1 || enq.jobs[0].Kind != job.KindGistUpdate || enq.jobs[0].Target.GistType != "paper_to_kindle" {
		t.Fatalf("second run: gist job must be enqueued even with changed=false: %+v", enq.jobs)
	}
	wantID := buildNewReleasePaperGistJob(j, "1234567890").JobID
	if enq.jobs[0].JobID != wantID {
		t.Errorf("second run gist JobID = %q, want deterministic %q", enq.jobs[0].JobID, wantID)
	}
}

func TestHandleNewReleaseSearch_ISBNCandidateExistingPaperSkipsDetail(t *testing.T) {
	isbn := completeHit("1234567890123")
	fetcher := &fakeSearchFetcher{result: SearchResult{Category: SearchOK, Hits: []SearchHit{isbn}}}
	deps := baseDeps()
	deps.SearchFetcher = fetcher
	deps.PaperCandidateStore.(*fakePaperCandidateStore).exists = true

	if _, err := HandleNewReleaseSearch(context.Background(), deps, searchJob("海李")); err != nil {
		t.Fatalf("HandleNewReleaseSearch: %v", err)
	}
	if got := len(deps.Enqueuer.(*fakeEnqueuer).jobs); got != 0 {
		t.Errorf("ISBN candidate already in paper_books must not enqueue detail, got %d", got)
	}
}

func TestHandleNewReleaseSearch_ISBNCandidatePaperExistsErrorPropagates(t *testing.T) {
	isbn := completeHit("1234567890")
	fetcher := &fakeSearchFetcher{result: SearchResult{Category: SearchOK, Hits: []SearchHit{isbn}}}
	deps := baseDeps()
	deps.SearchFetcher = fetcher
	deps.PaperCandidateStore.(*fakePaperCandidateStore).existsErr = errors.New("paper_books s3 failed")

	oc, err := HandleNewReleaseSearch(context.Background(), deps, searchJob("海李"))
	if err == nil {
		t.Fatal("paper_books exists error must propagate")
	}
	if oc.ErrorType != errorTypeEnqueueFailed {
		t.Errorf("error_type = %q, want %q", oc.ErrorType, errorTypeEnqueueFailed)
	}
}

func TestHandleNewReleaseSearch_ISBNExistsCheckDoesNotAffectKindleCandidates(t *testing.T) {
	fetcher := &fakeSearchFetcher{result: SearchResult{Category: SearchOK, Hits: []SearchHit{completeHit("B0FX3X569X")}}}
	deps := baseDeps()
	deps.SearchFetcher = fetcher
	deps.PaperCandidateStore.(*fakePaperCandidateStore).exists = true

	if _, err := HandleNewReleaseSearch(context.Background(), deps, searchJob("海李")); err != nil {
		t.Fatalf("HandleNewReleaseSearch: %v", err)
	}
	enq := deps.Enqueuer.(*fakeEnqueuer)
	if len(enq.jobs) != 1 || enq.jobs[0].Kind != job.KindNewReleaseResult {
		t.Fatalf("Kindle candidate must route to result regardless of paper exists flag, got %+v", enq.jobs)
	}
	if deps.PaperCandidateStore.(*fakePaperCandidateStore).existsCalls != 0 {
		t.Errorf("paper exists check must not run for Kindle candidates")
	}
}

func TestIsRecentPaperRelease_BoundaryMatchesUserScript(t *testing.T) {
	// fixedClock = 2026-08-09 00:00:00 UTC = 2026-08-09 09:00 JST。JST暦日の今日は 2026-08-09。
	now := fixedClock()
	tests := []struct {
		name    string
		release time.Time
		recent  bool
	}{
		{name: "今日はrecent", release: time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC), recent: true},
		{name: "1日前はrecent", release: time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC), recent: true},
		{name: "6日前はrecent", release: time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC), recent: true},
		{name: "7日前境界はrecent外", release: time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC), recent: false},
		{name: "8日前はrecent外", release: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), recent: false},
		{name: "未来はrecent", release: futureDate, recent: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsRecentPaperRelease(tc.release, now); got != tc.recent {
				t.Errorf("IsRecentPaperRelease(%s, now) = %v, want %v", tc.release.Format("2006-01-02"), got, tc.recent)
			}
		})
	}
}

func TestIsRecentPaperRelease_JSTCalendarDayCrossing(t *testing.T) {
	// 処理時刻がJST深夜0時をまたぐ境界でも暦日で安定する。
	// 2026-08-09 00:00 JST 直前（= 2026-08-08 23:00 JST = 2026-08-08 14:00 UTC）でも
	// JST暦日の今日は 2026-08-08 となり、7日前境界は 2026-08-01。
	eve := time.Date(2026, 8, 8, 14, 0, 0, 0, time.UTC)
	if !IsRecentPaperRelease(time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC), eve) {
		t.Errorf("2026-08-08 JST 23:00 基準で 2026-08-02 はrecent（6日前）")
	}
	if IsRecentPaperRelease(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), eve) {
		t.Errorf("2026-08-08 JST 23:00 基準で 2026-08-01 はrecent外（7日前境界）")
	}
}

func paperInfoWithDate(asin string, release time.Time) PaperPageInfo {
	return PaperPageInfo{
		ASIN: asin, Title: "紙タイトル", URL: "https://u/" + asin,
		PaperPrice: book.NewPrice(900), ReleaseDate: release, HasReleaseDate: true,
		Contributors: []string{"海李"},
	}
}

func TestHandleNewReleasePaperDetail_PaperRecentExcludesOldRelease(t *testing.T) {
	eightDaysAgo := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	store := &fakePaperCandidateStore{changed: true}
	deps := baseDeps()
	deps.PaperPageFetcher.(*fakePaperPageFetcher).result = PaperPageResult{Category: ProductOK, Info: paperInfoWithDate("1234567890", eightDaysAgo)}
	deps.PaperCandidateStore = store

	oc, err := HandleNewReleasePaperDetail(context.Background(), deps, paperDetailJob("1234567890", "海李"))
	if err != nil {
		t.Fatalf("recent excluded must return nil error: %v", err)
	}
	if oc.Result != execution.ResultTerminal || oc.ErrorType != errorTypePaperRecent {
		t.Errorf("old paper must be terminal paper_recent_excluded: %+v", oc)
	}
	if len(store.upserts) != 0 {
		t.Errorf("recent外の紙書籍はpaper_booksへ保存しない")
	}
	enq := deps.Enqueuer.(*fakeEnqueuer)
	if len(enq.jobs) != 0 {
		t.Errorf("recent外の紙書籍はPaper Gistを投入しない: %+v", enq.jobs)
	}
	if len(deps.NotifiedStore.(*fakeNotifiedStore).upserts) != 0 || len(deps.UpcomingStore.(*fakeUpcomingStore).upserts) != 0 {
		t.Errorf("recent除外経路はnotified/upcomingへ触れない")
	}
	if deps.AuthorStore.(*fakeAuthorStore).gotAuthor != "" {
		t.Errorf("recent除外経路はauthors.LatestReleaseを更新しない")
	}
}

func TestHandleNewReleasePaperDetail_RecentPaperIsSaved(t *testing.T) {
	today := time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)
	store := &fakePaperCandidateStore{changed: true}
	deps := baseDeps()
	deps.PaperPageFetcher.(*fakePaperPageFetcher).result = PaperPageResult{Category: ProductOK, Info: paperInfoWithDate("1234567890", today)}
	deps.PaperCandidateStore = store

	oc, err := HandleNewReleasePaperDetail(context.Background(), deps, paperDetailJob("1234567890", "海李"))
	if err != nil {
		t.Fatalf("HandleNewReleasePaperDetail: %v", err)
	}
	if oc.Result != execution.ResultCompleted {
		t.Errorf("recent紙書籍は保存完了: %+v", oc)
	}
	if len(store.upserts) != 1 {
		t.Errorf("recent紙書籍はpaper_booksへ保存する: got %d", len(store.upserts))
	}
	enq := deps.Enqueuer.(*fakeEnqueuer)
	if len(enq.jobs) != 1 || enq.jobs[0].Target.GistType != "paper_to_kindle" {
		t.Errorf("recent紙書籍はPaper Gistを投入する: %+v", enq.jobs)
	}
}

func TestHandleNewReleasePaperDetail_MinPriceAndRecentBothRequired(t *testing.T) {
	today := time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)
	eightDaysAgo := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name       string
		release    time.Time
		price      float64
		wantResult string
		wantErr    string
		wantSaved  bool
	}{
		{name: "recent+低価格はmin_price除外", release: today, price: 220, wantResult: execution.ResultTerminal, wantErr: errorTypeMinPriceExcluded, wantSaved: false},
		{name: "recent外+有効価格はrecent除外", release: eightDaysAgo, price: 900, wantResult: execution.ResultTerminal, wantErr: errorTypePaperRecent, wantSaved: false},
		{name: "recent+有効価格は保存", release: today, price: 900, wantResult: execution.ResultCompleted, wantErr: "", wantSaved: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info := paperInfoWithDate("1234567890", tc.release)
			info.PaperPrice = book.NewPrice(tc.price)
			store := &fakePaperCandidateStore{changed: true}
			deps := baseDeps()
			deps.Config.MinPrice = 221
			deps.PaperPageFetcher.(*fakePaperPageFetcher).result = PaperPageResult{Category: ProductOK, Info: info}
			deps.PaperCandidateStore = store

			oc, err := HandleNewReleasePaperDetail(context.Background(), deps, paperDetailJob("1234567890", "海李"))
			if err != nil {
				t.Fatalf("HandleNewReleasePaperDetail: %v", err)
			}
			if oc.Result != tc.wantResult {
				t.Errorf("result = %v, want %v", oc.Result, tc.wantResult)
			}
			if oc.ErrorType != tc.wantErr {
				t.Errorf("error_type = %q, want %q", oc.ErrorType, tc.wantErr)
			}
			if tc.wantSaved && len(store.upserts) != 1 {
				t.Errorf("want saved, got %d upserts", len(store.upserts))
			}
			if !tc.wantSaved && len(store.upserts) != 0 {
				t.Errorf("want not saved, got %d upserts", len(store.upserts))
			}
		})
	}
}

func TestHandleNewReleasePaperDetail_SevenDayBoundaryIsExcluded(t *testing.T) {
	sevenDaysAgo := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	store := &fakePaperCandidateStore{changed: true}
	deps := baseDeps()
	deps.PaperPageFetcher.(*fakePaperPageFetcher).result = PaperPageResult{Category: ProductOK, Info: paperInfoWithDate("1234567890", sevenDaysAgo)}
	deps.PaperCandidateStore = store

	oc, err := HandleNewReleasePaperDetail(context.Background(), deps, paperDetailJob("1234567890", "海李"))
	if err != nil {
		t.Fatalf("HandleNewReleasePaperDetail: %v", err)
	}
	if oc.Result != execution.ResultTerminal || oc.ErrorType != errorTypePaperRecent {
		t.Errorf("7日前境界の紙書籍はUserScriptと同じくrecent外: %+v", oc)
	}
	if len(store.upserts) != 0 {
		t.Errorf("7日前境界の紙書籍はpaper_booksへ保存しない")
	}
}

func TestHandleNewReleaseResult_KindleCandidateOutsideRecentWindowStillProcesses(t *testing.T) {
	// Kindle候補はrecent判定を適用せず、7日窓より古い過去発売でもAuthors更新・Gist投入が起きる。
	thirtyDaysAgo := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	product := futureProduct("B0FX3X569X")
	product.ReleaseDate = thirtyDaysAgo
	deps := baseDeps()
	deps.AuthorStore.(*fakeAuthorStore).changed = true

	if _, err := HandleNewReleaseResult(context.Background(), deps, resultJob("B0FX3X569X", "海李", product)); err != nil {
		t.Fatalf("HandleNewReleaseResult: %v", err)
	}
	if deps.AuthorStore.(*fakeAuthorStore).gotAuthor != "海李" {
		t.Errorf("Kindle候補はrecent窓適用外でAuthors更新対象のまま: gotAuthor=%q", deps.AuthorStore.(*fakeAuthorStore).gotAuthor)
	}
	enq := deps.Enqueuer.(*fakeEnqueuer)
	if len(enq.jobs) != 1 || enq.jobs[0].Target.GistType != "new_release" {
		t.Errorf("Kindle候補は過去発売でもAuthor gistを投入する: %+v", enq.jobs)
	}
	if len(deps.NotifiedStore.(*fakeNotifiedStore).upserts) != 0 {
		t.Errorf("Kindle過去発売はnotifiedへ入らない点は維持")
	}
}
