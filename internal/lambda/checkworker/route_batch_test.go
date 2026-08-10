package checkworker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/events"

	"github.com/shinderuman/kindle-automation/internal/application/execution"
	"github.com/shinderuman/kindle-automation/internal/application/newrelease"
	"github.com/shinderuman/kindle-automation/internal/domain/book"
	"github.com/shinderuman/kindle-automation/internal/gist"
	"github.com/shinderuman/kindle-automation/internal/job"
	"github.com/shinderuman/kindle-automation/internal/storage"
)

// --- 0-Amazon kind の routing 検証用 stub ---

// singleEnqueuer は sale/newrelease 両 Enqueuer（Enqueue1件）を満たし投入を記録する。
type singleEnqueuer struct {
	jobs []job.Job
	err  error
}

func (e *singleEnqueuer) Enqueue(_ context.Context, j job.Job) error {
	if e.err != nil {
		return e.err
	}
	e.jobs = append(e.jobs, j)
	return nil
}

type nrNotifiedStore struct{}

func (nrNotifiedStore) Exists(context.Context, string) (bool, error) { return false, nil }
func (nrNotifiedStore) ApplyRetentionAndExists(context.Context, string, time.Time) (bool, error) {
	return false, nil
}
func (nrNotifiedStore) Upsert(context.Context, book.KindleBook) error { return nil }

type nrUpcomingStore struct{}

func (nrUpcomingStore) Upsert(context.Context, book.KindleBook) error { return nil }

type nrAuthorStore struct{ changed bool }

func (s *nrAuthorStore) UpdateLatestRelease(context.Context, string, time.Time, string, string) (bool, error) {
	return s.changed, nil
}

type silentNotifier struct{}

func (silentNotifier) Notify(context.Context, string) error { return nil }

// pastReleaseClock は newWorker の固定 clock（2026-08-09）より過去へ評価される候補日を返す。
func pastReleaseDate() time.Time { return time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC) }

// nrDepsWithStubs は new_release_result を Amazon 未アクセスで完了できる最小 stub 依存を返す。
func nrDepsWithStubs(enq *singleEnqueuer) newrelease.Dependencies {
	return newrelease.Dependencies{
		NotifiedStore: nrNotifiedStore{},
		UpcomingStore: nrUpcomingStore{},
		AuthorStore:   &nrAuthorStore{changed: false},
		Enqueuer:      enq,
		Notifier:      silentNotifier{},
		Config:        newrelease.Config{},
		Clock:         func() time.Time { return time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC) },
	}
}

// sale_finalize と new_release_result は Amazon へ 0 回で各ユースケースへ routing される。
// 未知 kind の default 分岐（unknown_kind）へ落ちないことを outcome/error で検証する（SPECIFICATION.md 7.3）。
func TestRoute_NonAmazonKindsDispatchWithoutAmazon(t *testing.T) {
	saleF, nrF, paperF := retryableFetchers()
	enq := &singleEnqueuer{}
	w := newWorker(saleF, nrF, paperF, gist.Dependencies{})
	w.SaleDeps.Enqueuer = enq
	w.NRDeps = nrDepsWithStubs(enq)

	t.Run("sale_finalize", func(t *testing.T) {
		enq.jobs = nil
		j := job.Job{Version: job.Version, JobID: "sf", Kind: job.KindSaleFinalize, CheckType: job.CheckSale, CycleID: "c"}
		oc, err := w.route(context.Background(), j)
		if err != nil {
			t.Fatalf("sale_finalize route: %v", err)
		}
		if oc.Result != execution.ResultCompleted {
			t.Errorf("result = %v, want completed", oc.Result)
		}
		if len(enq.jobs) != 1 || enq.jobs[0].Kind != job.KindGistUpdate {
			t.Errorf("sale_finalize must enqueue 1 gist_update, got %v", enq.jobs)
		}
		if saleF.calls+nrF.searchCalls+nrF.productCalls+paperF.paperCalls+paperF.kindleCalls != 0 {
			t.Errorf("sale_finalize must not call amazon: %d/%d/%d/%d/%d",
				saleF.calls, nrF.searchCalls, nrF.productCalls, paperF.paperCalls, paperF.kindleCalls)
		}
	})

	t.Run("new_release_result", func(t *testing.T) {
		j := job.Job{
			Version: job.Version, JobID: "nrr", Kind: job.KindNewReleaseResult,
			CheckType: job.CheckNewRelease, CycleID: "c",
			Target: job.Target{
				ASIN: "B0FX3X569X", AuthorName: "海李",
				Product: &job.SearchProduct{
					ASIN: "B0FX3X569X", Title: "候補", URL: "https://www.amazon.co.jp/dp/B0FX3X569X?tag=t",
					ItemType: job.ItemTypeKindle, ReleaseDate: pastReleaseDate(),
				},
			},
		}
		oc, err := w.route(context.Background(), j)
		if err != nil {
			t.Fatalf("new_release_result route: %v", err)
		}
		if oc.Result != execution.ResultCompleted {
			t.Errorf("result = %v, want completed", oc.Result)
		}
		if saleF.calls+nrF.searchCalls+nrF.productCalls+paperF.paperCalls+paperF.kindleCalls != 0 {
			t.Errorf("new_release_result must not call amazon: %d/%d/%d/%d/%d",
				saleF.calls, nrF.searchCalls, nrF.productCalls, paperF.paperCalls, paperF.kindleCalls)
		}
	})
}

// 空 record の event は設定読込後、処理対象なしで成功する（BatchSize=1 だが空も正常終了）。
func TestHandleSQSEvent_EmptyRecordsSucceeds(t *testing.T) {
	saleF, nrF, paperF := retryableFetchers()
	w := newWorker(saleF, nrF, paperF, gist.Dependencies{})
	if err := w.HandleSQSEvent(context.Background(), events.SQSEvent{}); err != nil {
		t.Fatalf("empty event must succeed: %v", err)
	}
	if saleF.calls+nrF.searchCalls+nrF.productCalls+paperF.paperCalls+paperF.kindleCalls != 0 {
		t.Errorf("empty event must not call amazon")
	}
}

// 複数 record は順に処理される。全て成功なら error にならない（SPECIFICATION.md 7.1 BatchSize=1 でも handler は逐次処理）。
func TestHandleSQSEvent_MultipleRecordsProcessedInOrder(t *testing.T) {
	store := storage.NewMemStore()
	store.Seed("checker_configs.json", checkerConfigWithSaleGistID("g1"))
	store.Seed("excluded_title_keywords.json", "[]")
	updater := &recordingGistUpdater{}
	w := &Worker{
		GistDeps: gist.Dependencies{
			SaleBooks:  emptyBookList{},
			PaperBooks: emptyBookList{},
			Authors:    emptyAuthorList{},
			Updater:    updater,
			Settings:   gist.Settings{Sale: gist.Target{ID: "g1", Filename: "sale.md"}},
		},
		store:                    store,
		checkerConfigKey:         "checker_configs.json",
		excludedTitleKeywordsKey: "excluded_title_keywords.json",
	}
	body := mustEncode(t, job.Job{
		Version: job.Version, JobID: "id", Kind: job.KindGistUpdate,
		CheckType: job.CheckSale, CycleID: "c", Target: job.Target{GistType: gist.TypeSale},
	})
	event := events.SQSEvent{Records: []events.SQSMessage{
		{MessageId: "m1", Body: body},
		{MessageId: "m2", Body: body},
	}}

	if err := w.HandleSQSEvent(context.Background(), event); err != nil {
		t.Fatalf("multiple records should succeed: %v", err)
	}
	if len(updater.ids) != 2 {
		t.Errorf("updater calls = %d, want 2 (両 record を処理)", len(updater.ids))
	}
}

// 未対応 version は job schema error となり Amazon へ進まず error を返す（SPECIFICATION.md 7.2）。
func TestHandleSQSEvent_UnsupportedVersionErrors(t *testing.T) {
	saleF, nrF, paperF := retryableFetchers()
	w := newWorker(saleF, nrF, paperF, gist.Dependencies{})
	body := `{"version":2,"job_id":"id","kind":"sale_check","check_type":"sale","cycle_id":"c","scheduled_at":"2026-08-09T00:00:00Z","target":{"asin":"B0FX3X569X"}}`
	event := events.SQSEvent{Records: []events.SQSMessage{{MessageId: "m1", Body: body}}}

	err := w.HandleSQSEvent(context.Background(), event)
	if err == nil {
		t.Fatal("unsupported version must error")
	}
	if !errors.Is(err, job.ErrUnsupportedVersion) {
		t.Errorf("err = %v, want wrap of ErrUnsupportedVersion", err)
	}
	if saleF.calls != 0 {
		t.Errorf("must not call amazon on unsupported version: %d", saleF.calls)
	}
}

// 必須 field 欠落（sale_check の asin）も job schema error となり Amazon へ進まない（SPECIFICATION.md 7.2）。
func TestHandleSQSEvent_MissingRequiredFieldErrors(t *testing.T) {
	saleF, nrF, paperF := retryableFetchers()
	w := newWorker(saleF, nrF, paperF, gist.Dependencies{})
	// asin 無し → requireASIN → ErrMissingField。
	body := `{"version":1,"job_id":"id","kind":"sale_check","check_type":"sale","cycle_id":"c","scheduled_at":"2026-08-09T00:00:00Z","target":{}}`
	event := events.SQSEvent{Records: []events.SQSMessage{{MessageId: "m1", Body: body}}}

	err := w.HandleSQSEvent(context.Background(), event)
	if err == nil {
		t.Fatal("missing required field must error")
	}
	if !errors.Is(err, job.ErrMissingField) {
		t.Errorf("err = %v, want wrap of ErrMissingField", err)
	}
	if saleF.calls != 0 {
		t.Errorf("must not call amazon on missing field: %d", saleF.calls)
	}
}
