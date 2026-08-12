package checkworker

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strings"
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
	"github.com/shinderuman/kindle-automation/internal/logging"
	"github.com/shinderuman/kindle-automation/internal/storage"
)

type countSaleFetcher struct {
	calls  int
	result sale.FetchResult
}

func (f *countSaleFetcher) FetchProduct(_ context.Context, _ string) (sale.FetchResult, error) {
	f.calls++
	return f.result, nil
}

// countNRFetcher は新刊の SearchFetcher・ProductFetcher・PaperPageFetcher の3つを満たし呼出を個別カウントする。
type countNRFetcher struct {
	searchCalls     int
	productCalls    int
	paperPageCalls  int
	searchResult    newrelease.SearchResult
	productResult   newrelease.ProductResult
	paperPageResult newrelease.PaperPageResult
}

func (f *countNRFetcher) FetchSearch(_ context.Context, _ string) (newrelease.SearchResult, error) {
	f.searchCalls++
	return f.searchResult, nil
}

func (f *countNRFetcher) FetchProduct(_ context.Context, _ string) (newrelease.ProductResult, error) {
	f.productCalls++
	return f.productResult, nil
}

func (f *countNRFetcher) FetchPaperPage(_ context.Context, _ string) (newrelease.PaperPageResult, error) {
	f.paperPageCalls++
	return f.paperPageResult, nil
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

const testCheckerConfig = `{"SaleChecker":{"Enabled":true,"GistID":"gist-test","GistFilename":"sale.md","SaleThreshold":100,"PointPercent":10,"PriceChangeAmount":50}}`

type nrPaperStoreStub struct{}

func (nrPaperStoreStub) UpsertChanged(context.Context, book.KindleBook) (bool, error) {
	return false, nil
}

func (nrPaperStoreStub) Exists(context.Context, string) (bool, error) {
	return false, nil
}

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
			SearchFetcher:       nrF,
			ProductFetcher:      nrF,
			PaperPageFetcher:    nrF,
			PaperCandidateStore: nrPaperStoreStub{},
			Config:              newrelease.Config{},
			Clock:               clock,
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
			searchResult:    newrelease.SearchResult{Category: newrelease.SearchRetryable},
			productResult:   newrelease.ProductResult{Category: newrelease.ProductRetryable},
			paperPageResult: newrelease.PaperPageResult{Category: newrelease.ProductRetryable},
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
		{name: "new_release_paper_detail", kind: job.KindNewReleasePaperDetail, target: job.Target{ASIN: "1234567890", AuthorName: "海李"}},
		{name: "paper_to_kindle_check", kind: job.KindPaperToKindleCheck, target: job.Target{ASIN: "4434361325"}},
		{name: "paper_to_kindle_detail", kind: job.KindPaperToKindleDetail, target: job.Target{ASIN: "B0FX3X569X", SourceASIN: "4434361325"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			saleF, nrF, paperF := retryableFetchers()
			w := newWorker(saleF, nrF, paperF, gist.Dependencies{})
			j := job.Job{Version: job.Version, JobID: "id", Kind: tc.kind, CheckType: job.CheckSale, CycleID: "c", Target: tc.target}

			if _, err := w.route(context.Background(), j); err == nil {
				t.Fatalf("route should return retryable error")
			}

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
			case job.KindNewReleasePaperDetail:
				assertCount(t, "nr.paper", nrF.paperPageCalls, 1)
				assertCount(t, "nr.product", nrF.productCalls, 0)
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

func TestHandleSQSEvent_DecodeFailurePropagates(t *testing.T) {
	saleF, nrF, paperF := retryableFetchers()
	w := newWorker(saleF, nrF, paperF, gist.Dependencies{})
	event := events.SQSEvent{Records: []events.SQSMessage{{MessageId: "m1", Body: "not-json"}}}

	if err := w.HandleSQSEvent(context.Background(), event); err == nil {
		t.Fatal("HandleSQSEvent should fail on decode error")
	}
}

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

// failingConfigStore は Get で常に S3 一時障害を模倣した error を返す。
type failingConfigStore struct{}

func (failingConfigStore) Get(_ context.Context, _ string) (storage.Object, error) {
	return storage.Object{}, errors.New("s3 transient: request timeout")
}

func (failingConfigStore) Put(_ context.Context, _ string, _ []byte, _ storage.PutOptions) error {
	return nil
}

func TestHandleSQSEvent_ConfigLoadFailureLogsErrorAndPropagates(t *testing.T) {
	saleF, nrF, paperF := retryableFetchers()
	var buf bytes.Buffer
	w := &Worker{
		SaleDeps:                 sale.Dependencies{Fetcher: saleF},
		NRDeps:                   newrelease.Dependencies{SearchFetcher: nrF, ProductFetcher: nrF},
		PaperDeps:                papertokindle.Dependencies{PaperPageFetcher: paperF, KindlePageFetcher: paperF},
		store:                    failingConfigStore{},
		checkerConfigKey:         "checker_configs.json",
		excludedTitleKeywordsKey: "excluded_title_keywords.json",
		Logger:                   logging.New(&buf, slog.LevelInfo),
	}
	body := mustEncode(t, job.Job{
		Version: job.Version, JobID: "job-1", Kind: job.KindSaleCheck,
		CheckType: job.CheckSale, CycleID: "cycle-1", Target: job.Target{ASIN: "B0FX3X569X"},
	})
	event := events.SQSEvent{Records: []events.SQSMessage{{
		MessageId: "m1", Body: body,
		Attributes: map[string]string{"ApproximateReceiveCount": "3"},
	}}}

	err := w.HandleSQSEvent(context.Background(), event)
	if err == nil {
		t.Fatal("HandleSQSEvent should return error on config load failure")
	}
	if !strings.Contains(err.Error(), "load variable config") {
		t.Errorf("err = %v, want wrap of load variable config", err)
	}
	if n := bytes.Count(buf.Bytes(), []byte("\n")); n != 1 {
		t.Fatalf("ERROR log lines = %d, want 1", n)
	}
	m := parseLog(t, buf.Bytes())
	if m["event"] != logging.EventJobError {
		t.Errorf("event = %v, want %q", m["event"], logging.EventJobError)
	}
	if m["level"] != "ERROR" {
		t.Errorf("level = %v, want ERROR", m["level"])
	}
	if m["error_type"] != "config_load" {
		t.Errorf("error_type = %v, want config_load", m["error_type"])
	}
	if m["job_id"] != "job-1" {
		t.Errorf("job_id = %v, want job-1", m["job_id"])
	}
	if m["receive_count"] != float64(3) {
		t.Errorf("receive_count = %v, want 3", m["receive_count"])
	}
	if saleF.calls != 0 || nrF.searchCalls != 0 || nrF.productCalls != 0 ||
		paperF.paperCalls != 0 || paperF.kindleCalls != 0 {
		t.Errorf("amazon fetchers must not be called before config load: %d/%d/%d/%d/%d",
			saleF.calls, nrF.searchCalls, nrF.productCalls, paperF.paperCalls, paperF.kindleCalls)
	}
}

func TestHandleSQSEvent_MissingExcludedKeywordsFailsConfigLoad(t *testing.T) {
	saleF, nrF, paperF := retryableFetchers()
	store := storage.NewMemStore()
	store.Seed("checker_configs.json", testCheckerConfig)
	// excluded_title_keywords.json は seed しない（object 不在・rename 相当）。
	var buf bytes.Buffer
	w := &Worker{
		SaleDeps:                 sale.Dependencies{Fetcher: saleF},
		NRDeps:                   newrelease.Dependencies{SearchFetcher: nrF, ProductFetcher: nrF},
		PaperDeps:                papertokindle.Dependencies{PaperPageFetcher: paperF, KindlePageFetcher: paperF},
		store:                    store,
		checkerConfigKey:         "checker_configs.json",
		excludedTitleKeywordsKey: "excluded_title_keywords.json",
		Logger:                   logging.New(&buf, slog.LevelInfo),
	}
	// warm invocation で前回成功時の古い除外語が残っている状態を模倣する。
	w.NRDeps.Config.ExcludedKeywords = []string{"stale-warm-value"}
	body := mustEncode(t, job.Job{
		Version: job.Version, JobID: "job-excl", Kind: job.KindNewReleaseSearch,
		CheckType: job.CheckNewRelease, CycleID: "cycle-1", Target: job.Target{AuthorName: "作者"},
	})
	event := events.SQSEvent{Records: []events.SQSMessage{{
		MessageId: "m1", Body: body,
		Attributes: map[string]string{"ApproximateReceiveCount": "2"},
	}}}

	err := w.HandleSQSEvent(context.Background(), event)
	if err == nil {
		t.Fatal("HandleSQSEvent should fail when excluded_title_keywords.json is missing")
	}
	if !strings.Contains(err.Error(), "load variable config") {
		t.Errorf("err = %v, want wrap of load variable config", err)
	}
	if n := bytes.Count(buf.Bytes(), []byte("\n")); n != 1 {
		t.Fatalf("ERROR log lines = %d, want 1", n)
	}
	m := parseLog(t, buf.Bytes())
	if m["event"] != logging.EventJobError {
		t.Errorf("event = %v, want %q", m["event"], logging.EventJobError)
	}
	if m["error_type"] != "config_load" {
		t.Errorf("error_type = %v, want config_load", m["error_type"])
	}
	if saleF.calls != 0 || nrF.searchCalls != 0 || nrF.productCalls != 0 ||
		paperF.paperCalls != 0 || paperF.kindleCalls != 0 {
		t.Errorf("amazon fetchers must not be called when excluded object is missing: %d/%d/%d/%d/%d",
			saleF.calls, nrF.searchCalls, nrF.productCalls, paperF.paperCalls, paperF.kindleCalls)
	}
}

func TestHandleSQSEvent_ConfigLoadFailureWithUndecodableMessage(t *testing.T) {
	var buf bytes.Buffer
	w := &Worker{
		store:                    failingConfigStore{},
		checkerConfigKey:         "checker_configs.json",
		excludedTitleKeywordsKey: "excluded_title_keywords.json",
		Logger:                   logging.New(&buf, slog.LevelInfo),
	}
	event := events.SQSEvent{Records: []events.SQSMessage{{MessageId: "m1", Body: "not-json"}}}

	err := w.HandleSQSEvent(context.Background(), event)
	if err == nil {
		t.Fatal("HandleSQSEvent should return error on config load failure")
	}
	if n := bytes.Count(buf.Bytes(), []byte("\n")); n != 1 {
		t.Fatalf("log lines = %d, want 1 (decode 不能でもログを失わない・重複しない)", n)
	}
	m := parseLog(t, buf.Bytes())
	if m["event"] != logging.EventJobError {
		t.Errorf("event = %v, want %q", m["event"], logging.EventJobError)
	}
	if m["error_type"] != "config_load" {
		t.Errorf("error_type = %v, want config_load", m["error_type"])
	}
}

func TestHandleSQSEvent_SuccessEmitsNoConfigLoadError(t *testing.T) {
	store := storage.NewMemStore()
	store.Seed("checker_configs.json", checkerConfigWithSaleGistID("g1"))
	store.Seed("excluded_title_keywords.json", "[]")
	var buf bytes.Buffer
	w := &Worker{
		GistDeps: gist.Dependencies{
			SaleBooks:  emptyBookList{},
			PaperBooks: emptyBookList{},
			Authors:    emptyAuthorList{},
			Updater:    &recordingGistUpdater{},
			Settings:   gist.Settings{Sale: gist.Target{ID: "g1", Filename: "sale.md"}},
		},
		store:                    store,
		checkerConfigKey:         "checker_configs.json",
		excludedTitleKeywordsKey: "excluded_title_keywords.json",
		Logger:                   logging.New(&buf, slog.LevelInfo),
	}
	body := mustEncode(t, job.Job{
		Version: job.Version, JobID: "id", Kind: job.KindGistUpdate,
		CheckType: job.CheckSale, CycleID: "c", Target: job.Target{GistType: gist.TypeSale},
	})
	event := events.SQSEvent{Records: []events.SQSMessage{{MessageId: "m1", Body: body}}}

	if err := w.HandleSQSEvent(context.Background(), event); err != nil {
		t.Fatalf("HandleSQSEvent success: %v", err)
	}
	for _, line := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		m := parseLog(t, line)
		if m["level"] == "ERROR" {
			t.Errorf("success path must not emit ERROR log: %s", string(line))
		}
		if m["error_type"] == "config_load" {
			t.Errorf("success path must not emit config_load error: %s", string(line))
		}
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

// stubSecretGetter は SSM GetParameter の stub。requested に呼出順の path を記録し不要 key 要求を検証する。
type stubSecretGetter struct {
	values    map[string]string
	requested []string
}

func (g *stubSecretGetter) GetParameter(_ context.Context, in *ssm.GetParameterInput, _ ...func(*ssm.Options)) (*ssm.GetParameterOutput, error) {
	g.requested = append(g.requested, *in.Name)
	if v, ok := g.values[*in.Name]; ok {
		return &ssm.GetParameterOutput{Parameter: &ssmtypes.Parameter{Value: aws.String(v)}}, nil
	}
	return nil, &smithy.GenericAPIError{Code: "ParameterNotFound"}
}

func newCheckWorkerSecretGetter() *stubSecretGetter {
	g := &stubSecretGetter{values: map[string]string{}}
	for _, key := range checkWorkerRequiredSecretKeys {
		g.values["/myapp/secure/"+key] = "value-" + key
	}
	return g
}

func TestCheckWorkerRequiredSecretKeys(t *testing.T) {
	want := []string{
		config.KeyAmazonPartnerTag,
		config.KeyGitHubToken,
		config.KeySlackBotToken,
		config.KeySlackNoticeChannel,
		config.KeyMastodonServer,
		config.KeyMastodonAccessToken,
	}
	if !reflect.DeepEqual(checkWorkerRequiredSecretKeys, want) {
		t.Fatalf("checkWorkerRequiredSecretKeys = %v, want %v", checkWorkerRequiredSecretKeys, want)
	}
	for _, unnecessary := range []string{config.KeySlackErrorChannel, config.KeyMastodonClientID, config.KeyMastodonClientSecret} {
		if containsKey(checkWorkerRequiredSecretKeys, unnecessary) {
			t.Errorf("check-worker must not require unnecessary key %s", unnecessary)
		}
	}
}

func TestCheckWorkerSecrets_AllPresentSucceedsAndOmitsUnnecessary(t *testing.T) {
	g := newCheckWorkerSecretGetter()
	secrets, err := config.LoadSecrets(context.Background(), g, checkWorkerRequiredSecretKeys, nil)
	if err != nil {
		t.Fatalf("LoadSecrets all present: %v", err)
	}
	if secrets.AmazonPartnerTag == "" || secrets.GitHubToken == "" ||
		secrets.SlackBotToken == "" || secrets.SlackNoticeChannel == "" ||
		secrets.MastodonServer == "" || secrets.MastodonAccessToken == "" {
		t.Errorf("required secrets not populated: %+v", secrets)
	}
	for _, key := range []string{config.KeySlackErrorChannel, config.KeyMastodonClientID, config.KeyMastodonClientSecret} {
		if containsKey(g.requested, "/myapp/secure/"+key) || containsKey(g.requested, "/myapp/plain/"+key) {
			t.Errorf("check-worker must not request unnecessary key %s", key)
		}
	}
}

func TestCheckWorkerSecrets_RequiredMissingErrors(t *testing.T) {
	cases := []struct {
		name string
		drop string
	}{
		{name: "amazon partner tag", drop: config.KeyAmazonPartnerTag},
		{name: "github token", drop: config.KeyGitHubToken},
		{name: "slack bot token (slack partial: channel only)", drop: config.KeySlackBotToken},
		{name: "slack notice channel (slack partial: token only)", drop: config.KeySlackNoticeChannel},
		{name: "mastodon server (mastodon partial: token only)", drop: config.KeyMastodonServer},
		{name: "mastodon access token (mastodon partial: server only)", drop: config.KeyMastodonAccessToken},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := newCheckWorkerSecretGetter()
			delete(g.values, "/myapp/secure/"+tc.drop)
			delete(g.values, "/myapp/plain/"+tc.drop)
			_, err := config.LoadSecrets(context.Background(), g, checkWorkerRequiredSecretKeys, nil)
			if err == nil {
				t.Fatalf("LoadSecrets should fail when required key %s is missing", tc.drop)
			}
		})
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
