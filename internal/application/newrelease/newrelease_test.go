package newrelease

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

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
	alreadyNotified bool
	upserts         []book.KindleBook
	upsertErr       error
}

func (s *fakeNotifiedStore) Exists(_ context.Context, _ string) (bool, error) { return s.exists, nil }
func (s *fakeNotifiedStore) ApplyRetentionAndExists(_ context.Context, _ string, _ time.Time) (bool, error) {
	return s.alreadyNotified, nil
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
// 正規化した完全名同士を比較することを検証する（SPECIFICATION.md 13.3, bug1）。
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
	// bug1: 「山田 太郎」と対象「山田次郎」は姓だけ同じ別人。完全名が異なるためfalse。
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
