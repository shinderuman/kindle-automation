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

// --- stubs ---

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
		SearchFetcher:  &fakeSearchFetcher{},
		ProductFetcher: &fakeProductFetcher{},
		NotifiedStore:  &fakeNotifiedStore{},
		UpcomingStore:  &fakeUpcomingStore{},
		AuthorStore:    &fakeAuthorStore{},
		Enqueuer:       &fakeEnqueuer{},
		Notifier:       &fakeNotifier{},
		Config:         Config{ExcludedKeywords: []string{"除外"}},
		Clock:          fixedClock,
	}
}

func completeHit(asin string) SearchHit {
	return SearchHit{
		ASIN: asin, Title: "タイトル", URL: "https://u/" + asin,
		KindlePrice: book.NewPrice(800), ReleaseDate: futureDate, HasReleaseDate: true,
		Contributors: []string{"海李"}, IsKindle: true,
	}
}

// --- 純粋関数 ---

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

// TestAuthorMatches は contributor 境界を保持した []string を受け取り、
// 正規化した完全名同士を比較することを検証する（SPECIFICATION.md 13.3）。
// 空白トークン単位の部分一致は行わないため、姓だけ同一の別人を誤検出しない。
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
	// 各 contributor ごとに役割表記を除去して比較する。
	if !AuthorMatches("海李", []string{"海李 (著)"}) {
		t.Errorf("役割括弧を除去したcontributorと一致する場合はtrue")
	}
	// 複数 contributor のいずれかと完全一致すればtrue。
	if !AuthorMatches("やきいもほくほく", []string{"上原誠", "やきいもほくほく"}) {
		t.Errorf("複数contributorのいずれかに完全一致する場合はtrue")
	}
	// 「山田 太郎」と対象「山田次郎」は姓だけ同じ別人。完全名が異なるためfalse。
	if AuthorMatches("山田次郎", []string{"山田 太郎"}) {
		t.Errorf("空白入り別人（山田 太郎 vs 山田次郎）は姓部分一致でもfalse")
	}
	// 完全名が異なる部分一致（上原 vs 上原誠）はfalse。
	if AuthorMatches("上原", []string{"上原誠"}) {
		t.Errorf("部分名（上原）は完全名（上原誠）と異なるためfalse")
	}
}

// --- HandleNewReleaseSearch ---

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
	hit.HasReleaseDate = false // 発売日不足 → detail
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
	isbn := completeHit("1234567890")
	keyword := completeHit("B0KEYWORD01")
	keyword.Title = "除外タイトル"
	yearMonth := completeHit("B0YEARMONT1")
	yearMonth.Title = "2026年8月号"
	authorMismatch := completeHit("B0AUTHOR001")
	authorMismatch.Contributors = []string{"別人"}
	missing := completeHit("B0MISSING01")
	missing.URL = ""
	fetcher := &fakeSearchFetcher{result: SearchResult{Category: SearchOK, Hits: []SearchHit{isbn, keyword, yearMonth, authorMismatch, missing}}}
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
	// SPECIFICATION.md 13.2: 検索結果の先頭10件だけを候補として処理する。
	const total = 12
	hits := make([]SearchHit, total)
	for i := 0; i < total; i++ {
		hits[i] = completeHit(fmt.Sprintf("B0C%07d", i)) // 10桁 ASIN
	}
	fetcher := &fakeSearchFetcher{result: SearchResult{Category: SearchOK, Hits: hits}}
	deps := baseDeps()
	deps.SearchFetcher = fetcher

	if _, err := HandleNewReleaseSearch(context.Background(), deps, searchJob("海李")); err != nil {
		t.Fatalf("HandleNewReleaseSearch: %v", err)
	}
	enq := deps.Enqueuer.(*fakeEnqueuer)
	// completeHit は result job へ変換されるため、先頭10件分だけ投入される。
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

// --- HandleNewReleaseDetail ---

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

// --- applyCandidate (result job 経由) ---

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
	// 通知済みでも upcoming 補完と Author gist を省略しない（SPEC 7.5）。
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
	// 通知は保存成功後のみなので呼ばれない。
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

// 通知失敗は Notifier adapter が notification_error（SPECIFICATION.md 18.3）で記録するため、
// new_release ユースケースでは job を completed のままにし、保存済み状態を巻き戻さない。
func TestApplyCandidate_NotifyFailureDoesNotRollback(t *testing.T) {
	deps := baseDeps()
	deps.Notifier.(*fakeNotifier).err = errors.New("slack down")

	if _, err := HandleNewReleaseResult(context.Background(), deps, resultJob("B0FX3X569X", "海李", futureProduct("B0FX3X569X"))); err != nil {
		t.Fatalf("notify failure must not fail the job: %v", err)
	}
}

func TestApplyCandidate_AuthorStoreUnchangedSkipsGist(t *testing.T) {
	deps := baseDeps()
	deps.AuthorStore.(*fakeAuthorStore).changed = false

	if _, err := HandleNewReleaseResult(context.Background(), deps, resultJob("B0FX3X569X", "海李", futureProduct("B0FX3X569X"))); err != nil {
		t.Fatalf("HandleNewReleaseResult: %v", err)
	}
	if len(deps.Enqueuer.(*fakeEnqueuer).jobs) != 0 {
		t.Errorf("Author 変更なしは gist job を投入しない")
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
	// SPECIFICATION.md 9.3: LatestReleaseURL から query/fragment を除去する。
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
	// notified/upcoming は SPEC 9.2 の保存用 URL のため affiliate tag 付きのまま保持する。
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

// TestBuildAuthorGistJob_DiscriminatorIsDeterministic は Author 用 gist job_id の決定性と
// 衝突回避を検証する。SQS FIFO の MessageDeduplicationId は job_id の SHA-256 のため、
// job_id が衝突すると後続 job が5分 dedup で消失する（SPECIFICATION.md 7.2）。
// 同一 cycle・同一作者でも候補 A/B（異なるASIN）は別 job_id、同一候補の再試行は同一 job_id、
// 異なる作者も別 job_id になること。Target.GistType は new_release のまま変えない（gist updater 契約）。
func TestBuildAuthorGistJob_DiscriminatorIsDeterministic(t *testing.T) {
	const author = "海李"
	// 同一作者の候補 A/B（異なるASIN）。これが新旧ASINごとに段階的に LatestReleaseDate を更新する経路。
	candA := buildAuthorGistJob(detailJob("B0FX3X569X", author), "B0FX3X569X")
	candB := buildAuthorGistJob(detailJob("B0FX3X5700", author), "B0FX3X5700")
	if candA.JobID == candB.JobID {
		t.Errorf("same author different candidates must differ: %s", candA.JobID)
	}
	if scheduling.DedupID(candA.JobID) == scheduling.DedupID(candB.JobID) {
		t.Errorf("same author different candidates dedup must differ")
	}
	// 同一候補の再試行（Lambda/SQS 再配信）は同一 job_id で冪等になる。
	if buildAuthorGistJob(detailJob("B0FX3X569X", author), "B0FX3X569X").JobID != candA.JobID {
		t.Errorf("same candidate retry must be deterministic")
	}
	// 異なる作者も別 job_id になる。
	other := buildAuthorGistJob(detailJob("B0FX3X569X", "佐藤"), "B0FX3X569X")
	if candA.JobID == other.JobID {
		t.Errorf("different authors must differ: %s", candA.JobID)
	}
	if scheduling.DedupID(candA.JobID) == scheduling.DedupID(other.JobID) {
		t.Errorf("different authors dedup must differ")
	}
	// MessageGroupId は gist_update につき external-updates（SPECIFICATION.md 7.1/7.3）。
	if got := job.MessageGroup(candA.Kind); got != "external-updates" {
		t.Errorf("MessageGroup = %q, want external-updates", got)
	}
	if candA.Target.GistType != gistNewRelID {
		t.Errorf("GistType = %q, want %q", candA.Target.GistType, gistNewRelID)
	}
}

// --- retryable / terminal 分類の混同防止（SPECIFICATION.md 11.3, 13.4）---

// TestHandleNewReleaseSearch_SearchEmptyReturnsRetryableError は検索0件が search_empty の
// 再試行可能エラーになることを検証する（SPECIFICATION.md 11.3, 507）。
// terminal ではなく retryable（Lambda error → SQS 再配信）でなければならない。
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

// TestHandleNewReleaseSearch_FetchErrorReturnsError は FetchSearch の通信/decode エラーが
// fetch_error の retryable になることを検証する（SPECIFICATION.md 11.3 通信失敗）。
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

// TestHandleNewReleaseSearch_RetryableCategoryReturnsErrRetryableFetch は SearchRetryable が
// 型付き ErrRetryableFetch を返すことを検証する（分類の混同防止）。
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

// TestHandleNewReleaseSearch_UnknownCategoryReturnsError は未知の検索分類が
// unknown_category error になることを検証する（分類欠陥の表面化）。
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

// TestHandleNewReleaseSearch_EnqueueFailureReturnsError は候補 job 投入失敗が
// enqueue_failed で起動全体を失敗させることを検証する（SPECIFICATION.md 524）。
// 後続候補の投入は保証せず、失敗対象を失わず error を返す。
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

// TestHandleNewReleaseResult_MissingProductReturnsError は result job の product 欠落が
// missing_product で失敗することを検証する（job 投入側の schema 違反防御）。
func TestHandleNewReleaseResult_MissingProductReturnsError(t *testing.T) {
	deps := baseDeps()
	// product を持たない result job。
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

// TestHandleNewReleaseDetail_DateUnavailableIsRetryable は商品詳細で発売日を取得できない場合が
// 解析失敗の retryable になることを検証する（SPECIFICATION.md 13.4, 11.3 522）。
// terminal ではなく retryable で再試行しなければならない。
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

// TestHandleNewReleaseDetail_PermanentClientErrorIsTerminal は恒久 4xx が terminal になり、
// error_type=permanent_client_error で対象をリストへ残すことを検証する（SPECIFICATION.md 11.3 517）。
// not_found と区別し、retryable と混同しない。
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

// TestHandleNewReleaseDetail_NotFoundIsTerminal は 404/商品不存在が not_found terminal になることを検証する。
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

// TestHandleNewReleaseDetail_ExcludedKeywordAndYearMonthAreTerminal は商品詳細で除外キーワード・
// 年月タイトルに該当する候補が excluded terminal になることを検証する（SPECIFICATION.md 13.4/13.3）。
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

// TestHandleNewReleaseDetail_FetchErrorReturnsError は FetchProduct の通信エラーが
// fetch_error の retryable になることを検証する（SPECIFICATION.md 11.3 通信失敗）。
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

// TestHandleNewReleaseDetail_RetryableCategoryReturnsErrRetryableFetch は商品ページ取得の
// retryable 分類（403/429/5xx/CAPTCHA/構造欠落）が型付き ErrRetryableFetch になることを検証する
// （SPECIFICATION.md 11.3）。terminal や解析失敗と混同しない。
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

// TestHandleNewReleaseDetail_UnknownCategoryReturnsError は未知の商品分類が
// unknown_category error になることを検証する（分類欠陥の表面化）。
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

// --- 候補振り分け境界（SPECIFICATION.md 13.4）---

// TestHandleNewReleaseSearch_RoutesNonKindleAndInvalidPriceToDetail は検索結果で Kindle確定 or
// 正のKindle価格 or 発売日 のいずれかを確定できない候補が new_release_detail へ回されることを検証する。
// これらは result へ進めず、商品ページで再確認する（SPECIFICATION.md 13.4, 234）。
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

// TestHandleNewReleaseSearch_NotifiedExistsErrorPropagates は事前除外での notified 存在判定エラーが
// 投入失敗として伝播し失敗対象を失わないことを検証する（SPECIFICATION.md 13.3, 524）。
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

// --- 純粋関数: 発売日境界・正規化・役割表記（SPECIFICATION.md 13.3, 13.6）---

// TestIsFutureRelease_Boundary は ReleaseDate.After(now) の境界を検証する（SPECIFICATION.md 13.6）。
// 発売日==now は将来ではないため false、発売日が now より1日後なら true。
// これにより「ReleaseDateの時刻が処理時刻以前なら新刊予定として通知しない」境界を固定する。
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

// TestNormalizeAuthorName_EmptyAndMixedSpaces は空文字と半角/全角スペース混入を検証する。
func TestNormalizeAuthorName_EmptyAndMixedSpaces(t *testing.T) {
	if got := NormalizeAuthorName(""); got != "" {
		t.Errorf("empty = %q, want empty", got)
	}
	if got := NormalizeAuthorName(" 海　李 "); got != "海李" {
		t.Errorf("mixed leading/trailing spaces = %q, want 海李", got)
	}
}

// TestAuthorMatches_FullWidthRoleParenAndEmptyAuthor は全角役割括弧（著）の除去と、
// 対象作者名空の false を検証する（SPECIFICATION.md 13.3）。役割の半角/全角を問わない。
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

// --- 副作用順序: 保存失敗時の通知抑制・retention/authorStore error（SPECIFICATION.md 13.6）---

// TestApplyCandidate_UpcomingFailureSkipsNotify は upcoming 保存失敗時に通知しないことを検証する。
// 通知は notified と upcoming の両方の S3 保存成功後（SPECIFICATION.md 13.6 手順5-7の順序）。
func TestApplyCandidate_UpcomingFailureSkipsNotify(t *testing.T) {
	deps := baseDeps()
	deps.UpcomingStore.(*fakeUpcomingStore).upsertErr = errors.New("s3 conflict")

	if _, err := HandleNewReleaseResult(context.Background(), deps, resultJob("B0FX3X569X", "海李", futureProduct("B0FX3X569X"))); err == nil {
		t.Fatal("upcoming failure must return error")
	}
	// upcoming 保存失敗時は通知しない（保存成功後のみ通知）。
	if deps.Notifier.(*fakeNotifier).called {
		t.Errorf("must not notify when upcoming save failed")
	}
	// notified は upcoming の前に保存されるため成功している。
	if len(deps.NotifiedStore.(*fakeNotifiedStore).upserts) != 1 {
		t.Errorf("notified must be saved before upcoming")
	}
}

// TestApplyCandidate_RetentionFailureReturnsError は notified 保存期間適用の読み直し失敗が
// retention error になることを検証する（SPECIFICATION.md 13.6 手順1-3）。
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

// TestApplyCandidate_AuthorStoreFailureReturnsError は Author 最新作更新失敗が
// author_store error になり後続へ進まないことを検証する（SPECIFICATION.md 13.5）。
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
