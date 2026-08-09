package checkworker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/events"

	"github.com/shinderuman/kindle-automation/internal/application/newrelease"
	"github.com/shinderuman/kindle-automation/internal/application/papertokindle"
	"github.com/shinderuman/kindle-automation/internal/application/sale"
	"github.com/shinderuman/kindle-automation/internal/domain/book"
	"github.com/shinderuman/kindle-automation/internal/gist"
	"github.com/shinderuman/kindle-automation/internal/job"
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

func newWorker(saleF *countSaleFetcher, nrF *countNRFetcher, paperF *countPaperFetcher, gistDeps gist.Dependencies) *Worker {
	clock := func() time.Time { return time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC) }
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
		GistDeps: gistDeps,
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
}

func (u *recordingGistUpdater) Update(_ context.Context, _, _, _ string) error {
	u.called = true
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
