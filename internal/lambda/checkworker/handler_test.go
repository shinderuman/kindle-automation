package checkworker

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/aws/smithy-go"

	"github.com/shinderuman/kindle-automation/internal/application/newrelease"
	"github.com/shinderuman/kindle-automation/internal/application/papertokindle"
	"github.com/shinderuman/kindle-automation/internal/application/sale"
	"github.com/shinderuman/kindle-automation/internal/config"
	"github.com/shinderuman/kindle-automation/internal/domain/book"
	"github.com/shinderuman/kindle-automation/internal/gist"
	"github.com/shinderuman/kindle-automation/internal/job"
	"github.com/shinderuman/kindle-automation/internal/storage"
)

// --- カウンタ付き stub fetcher 群 ---

type countSaleFetcher struct {
	calls  int
	result sale.FetchResult
}

func (f *countSaleFetcher) FetchProduct(_ context.Context, _ string) (sale.FetchResult, error) {
	f.calls++
	return f.result, nil
}

// countNRFetcher は新刊の SearchFetcher と ProductFetcher の両方を満たし呼出を個別カウントする。
type countNRFetcher struct {
	searchCalls   int
	productCalls  int
	searchResult  newrelease.SearchResult
	productResult newrelease.ProductResult
}

func (f *countNRFetcher) FetchSearch(_ context.Context, _ string) (newrelease.SearchResult, error) {
	f.searchCalls++
	return f.searchResult, nil
}

func (f *countNRFetcher) FetchProduct(_ context.Context, _ string) (newrelease.ProductResult, error) {
	f.productCalls++
	return f.productResult, nil
}

type countPaperFetcher struct {
	paperCalls   int
	kindleCalls  int
	paperResult  papertokindle.PaperCheckResult
	kindleResult papertokindle.KindleDetailResult
}

func (f *countPaperFetcher) FetchPaperPage(_ context.Context, _ string) (papertokindle.PaperCheckResult, error) {
	f.paperCalls++
	return f.paperResult, nil
}

func (f *countPaperFetcher) FetchKindlePage(_ context.Context, _ string) (papertokindle.KindleDetailResult, error) {
	f.kindleCalls++
	return f.kindleResult, nil
}

// testCheckerConfig は HandleSQSEvent が refreshVariableConfig で読める有効な checker 設定。
// SaleChecker 有効・閾値正・Gist 設定ありで Validate を通す。
const testCheckerConfig = `{"SaleChecker":{"Enabled":true,"GistID":"gist-test","GistFilename":"sale.md","SaleThreshold":100,"PointPercent":10,"PriceChangeAmount":50}}`

func newWorker(saleF *countSaleFetcher, nrF *countNRFetcher, paperF *countPaperFetcher, gistDeps gist.Dependencies) *Worker {
	clock := func() time.Time { return time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC) }
	store := storage.NewMemStore()
	store.Seed("checker_configs.json", testCheckerConfig)
	store.Seed("excluded_title_keywords.json", "[]")
	return &Worker{
		SaleDeps: sale.Dependencies{
			Fetcher: saleF,
			Config:  sale.Config{UnprocessedKey: "unprocessed_asins.json"},
			Clock:   clock,
		},
		NRDeps: newrelease.Dependencies{
			SearchFetcher:  nrF,
			ProductFetcher: nrF,
			Config:         newrelease.Config{},
			Clock:          clock,
		},
		PaperDeps: papertokindle.Dependencies{
			PaperPageFetcher:  paperF,
			KindlePageFetcher: paperF,
			Clock:             clock,
		},
		GistDeps:                 gistDeps,
		store:                    store,
		checkerConfigKey:         "checker_configs.json",
		excludedTitleKeywordsKey: "excluded_title_keywords.json",
	}
}

// retryable を返すことで各 handler は Amazon 1回アクセス後に ErrRetryableFetch で早期復帰する。
func retryableFetchers() (*countSaleFetcher, *countNRFetcher, *countPaperFetcher) {
	return &countSaleFetcher{result: sale.FetchResult{Category: sale.CategoryRetryable}},
		&countNRFetcher{
			searchResult:  newrelease.SearchResult{Category: newrelease.SearchRetryable},
			productResult: newrelease.ProductResult{Category: newrelease.ProductRetryable},
		},
		&countPaperFetcher{
			paperResult:  papertokindle.PaperCheckResult{Category: papertokindle.CategoryRetryable},
			kindleResult: papertokindle.KindleDetailResult{Category: papertokindle.CategoryRetryable},
		}
}

func TestRoute_AmazonJobsCallFetcherExactlyOnce(t *testing.T) {
	cases := []struct {
		name   string
		kind   job.Kind
		target job.Target
	}{
		{name: "sale_check", kind: job.KindSaleCheck, target: job.Target{ASIN: "B0FX3X569X"}},
		{name: "new_release_search", kind: job.KindNewReleaseSearch, target: job.Target{AuthorName: "海李"}},
		{name: "new_release_detail", kind: job.KindNewReleaseDetail, target: job.Target{ASIN: "B0FX3X569X", AuthorName: "海李"}},
		{name: "paper_to_kindle_check", kind: job.KindPaperToKindleCheck, target: job.Target{ASIN: "4434361325"}},
		{name: "paper_to_kindle_detail", kind: job.KindPaperToKindleDetail, target: job.Target{ASIN: "B0FX3X569X", SourceASIN: "4434361325"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			saleF, nrF, paperF := retryableFetchers()
			w := newWorker(saleF, nrF, paperF, gist.Dependencies{})
			j := job.Job{Version: job.Version, JobID: "id", Kind: tc.kind, CheckType: job.CheckSale, CycleID: "c", Target: tc.target}

			// retryable は error（SQS 再配信）となる。
			if _, err := w.route(context.Background(), j); err == nil {
				t.Fatalf("route should return retryable error")
			}

			// 各 job は Amazon へ最大1回。他の fetcher は呼ばれない。
			switch tc.kind {
			case job.KindSaleCheck:
				assertCount(t, "sale", saleF.calls, 1)
				assertCount(t, "nr.search", nrF.searchCalls, 0)
				assertCount(t, "paper", paperF.paperCalls, 0)
			case job.KindNewReleaseSearch:
				assertCount(t, "nr.search", nrF.searchCalls, 1)
				assertCount(t, "nr.product", nrF.productCalls, 0)
				assertCount(t, "sale", saleF.calls, 0)
			case job.KindNewReleaseDetail:
				assertCount(t, "nr.product", nrF.productCalls, 1)
				assertCount(t, "nr.search", nrF.searchCalls, 0)
			case job.KindPaperToKindleCheck:
				assertCount(t, "paper", paperF.paperCalls, 1)
				assertCount(t, "paper.kindle", paperF.kindleCalls, 0)
			case job.KindPaperToKindleDetail:
				assertCount(t, "paper.kindle", paperF.kindleCalls, 1)
				assertCount(t, "paper", paperF.paperCalls, 0)
			}
		})
	}
}

func assertCount(t *testing.T, label string, got, want int) {
	t.Helper()
	if got != want {
		t.Errorf("%s calls = %d, want %d", label, got, want)
	}
}

// gist_update は Amazon を1回も呼ばず Gist 更新だけを行う（SPECIFICATION.md 7.3, job.AmazonRequests==0）。
func TestRoute_GistUpdateDoesNotCallAmazon(t *testing.T) {
	saleF, nrF, paperF := retryableFetchers()
	updater := &recordingGistUpdater{}
	deps := gist.Dependencies{
		SaleBooks:  emptyBookList{},
		PaperBooks: emptyBookList{},
		Authors:    emptyAuthorList{},
		Updater:    updater,
		Settings:   gist.Settings{Sale: gist.Target{ID: "g1", Filename: "sale.md"}},
	}
	w := newWorker(saleF, nrF, paperF, deps)
	j := job.Job{Version: job.Version, Kind: job.KindGistUpdate, Target: job.Target{GistType: "sale"}}

	if _, err := w.route(context.Background(), j); err != nil {
		t.Fatalf("route gist_update: %v", err)
	}
	if saleF.calls+nrF.searchCalls+nrF.productCalls+paperF.paperCalls+paperF.kindleCalls != 0 {
		t.Errorf("gist_update must not call amazon: counts=%d/%d/%d/%d/%d",
			saleF.calls, nrF.searchCalls, nrF.productCalls, paperF.paperCalls, paperF.kindleCalls)
	}
	if !updater.called {
		t.Errorf("gist Updater.Update must be called")
	}
}

// SQS event の decode 失敗は error として伝播し再配信させる。
func TestHandleSQSEvent_DecodeFailurePropagates(t *testing.T) {
	saleF, nrF, paperF := retryableFetchers()
	w := newWorker(saleF, nrF, paperF, gist.Dependencies{})
	event := events.SQSEvent{Records: []events.SQSMessage{{MessageId: "m1", Body: "not-json"}}}

	if err := w.HandleSQSEvent(context.Background(), event); err == nil {
		t.Fatal("HandleSQSEvent should fail on decode error")
	}
}

// route の業務 error も event から伝播する。
func TestHandleSQSEvent_RouteFailurePropagates(t *testing.T) {
	saleF, nrF, paperF := retryableFetchers()
	w := newWorker(saleF, nrF, paperF, gist.Dependencies{})
	body := mustEncode(t, job.Job{
		Version: job.Version, JobID: "id", Kind: job.KindSaleCheck,
		CheckType: job.CheckSale, CycleID: "c", Target: job.Target{ASIN: "B0FX3X569X"},
	})
	event := events.SQSEvent{Records: []events.SQSMessage{{MessageId: "m1", Body: body}}}

	if err := w.HandleSQSEvent(context.Background(), event); err == nil {
		t.Fatal("HandleSQSEvent should fail on retryable route error")
	}
	if saleF.calls != 1 {
		t.Errorf("sale fetcher calls = %d, want 1", saleF.calls)
	}
}

func mustEncode(t *testing.T, j job.Job) string {
	t.Helper()
	j.Validate()
	b, err := j.Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return string(b)
}

// --- gist テスト用 stub ---

type recordingGistUpdater struct {
	called bool
	ids    []string
}

func (u *recordingGistUpdater) Update(_ context.Context, id, _, _ string) error {
	u.called = true
	u.ids = append(u.ids, id)
	return nil
}

type emptyBookList struct{}

func (emptyBookList) Books(_ context.Context) ([]book.KindleBook, error) { return nil, nil }

type emptyAuthorList struct{}

func (emptyAuthorList) Authors(_ context.Context) ([]book.Author, error) { return nil, nil }

// job.Decode の error 種別検証用（schema 違反は terminal）。
func TestHandleSQSEvent_UnknownKindIsTerminal(t *testing.T) {
	saleF, nrF, paperF := retryableFetchers()
	w := newWorker(saleF, nrF, paperF, gist.Dependencies{})
	// version は正しいが kind が未知 → Encode は通るが Decode/Validate で ErrUnknownKind。
	body := `{"version":1,"job_id":"id","kind":"bogus","check_type":"sale","cycle_id":"c","scheduled_at":"2026-08-09T00:00:00Z","target":{"asin":"B0FX3X569X"}}`
	event := events.SQSEvent{Records: []events.SQSMessage{{MessageId: "m1", Body: body}}}

	err := w.HandleSQSEvent(context.Background(), event)
	if err == nil {
		t.Fatal("expected decode error for unknown kind")
	}
	if !errors.Is(err, job.ErrUnknownKind) {
		t.Errorf("err = %v, want wrap of ErrUnknownKind", err)
	}
}

// 同一 Worker（composition root 相当）を再構築せず HandleSQSEvent を2回呼び、間に stub S3 の
// checker 設定を変更すると2回目が新値を読むことを検証する（warm execution environment でも反映）。
func TestHandleSQSEvent_RefreshesCheckerConfigPerInvocation(t *testing.T) {
	store := storage.NewMemStore()
	store.Seed("checker_configs.json", checkerConfigWithSaleGistID("gist-v1"))
	store.Seed("excluded_title_keywords.json", "[]")
	updater := &recordingGistUpdater{}
	w := &Worker{
		GistDeps: gist.Dependencies{
			SaleBooks:  emptyBookList{},
			PaperBooks: emptyBookList{},
			Authors:    emptyAuthorList{},
			Updater:    updater,
		},
		store:                    store,
		checkerConfigKey:         "checker_configs.json",
		excludedTitleKeywordsKey: "excluded_title_keywords.json",
	}
	body := mustEncode(t, job.Job{
		Version: job.Version, JobID: "id", Kind: job.KindGistUpdate,
		CheckType: job.CheckSale, CycleID: "c", Target: job.Target{GistType: gist.TypeSale},
	})
	event := events.SQSEvent{Records: []events.SQSMessage{{MessageId: "m1", Body: body}}}

	if err := w.HandleSQSEvent(context.Background(), event); err != nil {
		t.Fatalf("first HandleSQSEvent: %v", err)
	}
	// Worker を再構築せず S3 の checker 設定だけ変更する。
	store.Seed("checker_configs.json", checkerConfigWithSaleGistID("gist-v2"))
	if err := w.HandleSQSEvent(context.Background(), event); err != nil {
		t.Fatalf("second HandleSQSEvent: %v", err)
	}

	want := []string{"gist-v1", "gist-v2"}
	if !reflect.DeepEqual(updater.ids, want) {
		t.Errorf("gist IDs = %v, want %v (2回目の invocation が新値を見ること)", updater.ids, want)
	}
}

func checkerConfigWithSaleGistID(gistID string) string {
	return `{"SaleChecker":{"Enabled":true,"GistID":"` + gistID + `","GistFilename":"sale.md","SaleThreshold":100,"PointPercent":10,"PriceChangeAmount":50}}`
}

// --- secret key 集合の回帰テスト ---

// stubSecretGetter は SSM GetParameter の stub。値があれば返し、なければ ParameterNotFound。
type stubSecretGetter struct {
	values map[string]string
}

func (g *stubSecretGetter) GetParameter(_ context.Context, in *ssm.GetParameterInput, _ ...func(*ssm.Options)) (*ssm.GetParameterOutput, error) {
	if v, ok := g.values[*in.Name]; ok {
		return &ssm.GetParameterOutput{Parameter: &ssmtypes.Parameter{Value: aws.String(v)}}, nil
	}
	return nil, &smithy.GenericAPIError{Code: "ParameterNotFound"}
}

// check-worker の secret key 集合は SLACK_ERROR_CHANNEL 等の不要 key を含まず、
// required だけ存在し optional が全て欠けても起動（LoadSecrets）を妨げない。
func TestCheckWorkerSecretKeySet(t *testing.T) {
	// check-worker が使用しない key は required・optional いずれにも含まれない。
	allKeys := append(append([]string{}, checkWorkerRequiredSecretKeys...), checkWorkerOptionalSecretKeys...)
	for _, unnecessary := range []string{config.KeySlackErrorChannel, config.KeyMastodonClientID, config.KeyMastodonClientSecret} {
		if containsKey(allKeys, unnecessary) {
			t.Errorf("check-worker must not load unnecessary key %s", unnecessary)
		}
	}

	// required だけ SSM に存在し optional が全て欠けても LoadSecrets は成功する。
	g := &stubSecretGetter{values: map[string]string{}}
	for _, key := range checkWorkerRequiredSecretKeys {
		g.values["/myapp/secure/"+key] = "v"
	}
	if _, err := config.LoadSecrets(context.Background(), g, checkWorkerRequiredSecretKeys, checkWorkerOptionalSecretKeys); err != nil {
		t.Fatalf("LoadSecrets with only required keys must succeed: %v", err)
	}
}

func containsKey(keys []string, want string) bool {
	for _, k := range keys {
		if k == want {
			return true
		}
	}
	return false
}
