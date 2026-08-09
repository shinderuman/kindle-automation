package dispatch

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shinderuman/kindle-automation/internal/job"
)

type fakeEnqueuer struct {
	batches [][]job.Job
	calls   int
	failOn  int // 何番目の batch（0始まり）で失敗させるか。-1 で失敗なし
}

func (f *fakeEnqueuer) EnqueueBatch(_ context.Context, jobs []job.Job) error {
	idx := f.calls
	f.calls++
	f.batches = append(f.batches, jobs)
	if idx == f.failOn {
		return errors.New("batch failed")
	}
	return nil
}

func (f *fakeEnqueuer) allJobs() []job.Job {
	var out []job.Job
	for _, b := range f.batches {
		out = append(out, b...)
	}
	return out
}

type fakeAsinReader struct {
	asins []string
	err   error
}

func (f fakeAsinReader) LoadAsins(_ context.Context, _ string) ([]string, error) {
	return f.asins, f.err
}

type fakeAuthorReader struct {
	names []string
	err   error
}

func (f fakeAuthorReader) LoadAuthorNames(_ context.Context, _ string) ([]string, error) {
	return f.names, f.err
}

type fakeConfig struct {
	enabled bool
}

func (f fakeConfig) IsEnabled(_ context.Context, _ job.CheckType) (bool, error) {
	return f.enabled, nil
}

type fakeUpcoming struct {
	called bool
	merged int
}

func (f *fakeUpcoming) MergeUpcoming(_ context.Context) (int, error) {
	f.called = true
	return f.merged, nil
}

func baseDeps(enq *fakeEnqueuer) Dependencies {
	return Dependencies{
		AsinListReader: fakeAsinReader{},
		AuthorReader:   fakeAuthorReader{},
		ConfigReader:   fakeConfig{enabled: true},
		Enqueuer:       enq,
		UpcomingMerger: &fakeUpcoming{},
		Keys:           Keys{Unprocessed: "unprocessed", Authors: "authors", PaperBooks: "paper"},
	}
}

func saleEvent() Event {
	return Event{
		CheckType:   job.CheckSale,
		ScheduledAt: time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC),
	}
}

func TestRun_Sale_EnqueuesOneJobPerAsinAndFinalizeLast(t *testing.T) {
	enq := &fakeEnqueuer{failOn: -1}
	deps := baseDeps(enq)
	deps.AsinListReader = fakeAsinReader{asins: []string{"B0ASIN0001", "B0ASIN0002"}}
	deps.UpcomingMerger = &fakeUpcoming{}

	result, err := Run(context.Background(), deps, saleEvent())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// 全 job = sale_check x2 + sale_finalize x1
	all := enq.allJobs()
	if len(all) != 3 {
		t.Fatalf("len(jobs) = %d, want 3", len(all))
	}
	// sale_finalize は最後の batch の1件のみ。
	lastBatch := enq.batches[len(enq.batches)-1]
	if len(lastBatch) != 1 || lastBatch[0].Kind != job.KindSaleFinalize {
		t.Fatalf("last batch should be sale_finalize only: %+v", lastBatch)
	}
	// sale_check は finalize より前。
	for i, j := range all {
		if j.Kind == job.KindSaleFinalize && i != len(all)-1 {
			t.Errorf("sale_finalize at index %d, want last", i)
		}
	}
	// DispatchResult（SPECIFICATION.md 18.3 cycle_dispatched）。
	if result.TargetCount != 2 || result.EnqueuedCount != 3 {
		t.Errorf("result = (target=%d enqueued=%d), want (2,3)", result.TargetCount, result.EnqueuedCount)
	}
	if result.Disabled {
		t.Errorf("result must not be disabled")
	}
}

func TestRun_Sale_AmazonJobsUseAmazonRequestsMessageGroup(t *testing.T) {
	enq := &fakeEnqueuer{failOn: -1}
	deps := baseDeps(enq)
	deps.AsinListReader = fakeAsinReader{asins: []string{"B0ASIN0001"}}

	if _, err := Run(context.Background(), deps, saleEvent()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, j := range enq.allJobs() {
		if got := job.MessageGroup(j.Kind); got != "amazon-requests" {
			t.Errorf("MessageGroup(%v) = %q, want amazon-requests", j.Kind, got)
		}
	}
}

func TestRun_Sale_FinalizeEnqueuedEvenWhenEmpty(t *testing.T) {
	enq := &fakeEnqueuer{failOn: -1}
	deps := baseDeps(enq)
	deps.AsinListReader = fakeAsinReader{asins: nil}

	result, err := Run(context.Background(), deps, saleEvent())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	all := enq.allJobs()
	if len(all) != 1 || all[0].Kind != job.KindSaleFinalize {
		t.Fatalf("empty sale should still enqueue sale_finalize: %+v", all)
	}
	// 対象0件でも finalize 1件を投入する（SPECIFICATION.md 6）。
	if result.TargetCount != 0 || result.EnqueuedCount != 1 {
		t.Errorf("result = (target=%d enqueued=%d), want (0,1)", result.TargetCount, result.EnqueuedCount)
	}
}

func TestRun_Sale_FinalizeNotEnqueuedWhenCheckFailed(t *testing.T) {
	enq := &fakeEnqueuer{failOn: 0} // 最初の sale_check batch を失敗
	deps := baseDeps(enq)
	deps.AsinListReader = fakeAsinReader{asins: []string{"B0ASIN0001"}}

	_, err := Run(context.Background(), deps, saleEvent())
	if err == nil {
		t.Fatal("Run should fail when sale_check enqueue fails")
	}
	for _, j := range enq.allJobs() {
		if j.Kind == job.KindSaleFinalize {
			t.Errorf("sale_finalize must not be enqueued when sale_check failed")
		}
	}
}

func TestRun_Sale_MergeUpcomingCountReported(t *testing.T) {
	enq := &fakeEnqueuer{failOn: -1}
	upc := &fakeUpcoming{merged: 7}
	deps := baseDeps(enq)
	deps.UpcomingMerger = upc
	deps.AsinListReader = fakeAsinReader{asins: []string{"B0ASIN0001"}}

	result, err := Run(context.Background(), deps, saleEvent())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !upc.called {
		t.Errorf("MergeUpcoming must be called for sale cycle")
	}
	if result.UpcomingMerged != 7 {
		t.Errorf("UpcomingMerged = %d, want 7", result.UpcomingMerged)
	}
}

func TestRun_NewRelease_EnqueuesOneJobPerAuthor(t *testing.T) {
	enq := &fakeEnqueuer{failOn: -1}
	deps := baseDeps(enq)
	deps.AuthorReader = fakeAuthorReader{names: []string{"海李", "作者B"}}

	result, err := Run(context.Background(), deps, Event{CheckType: job.CheckNewRelease, ScheduledAt: saleEvent().ScheduledAt})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	all := enq.allJobs()
	if len(all) != 2 {
		t.Fatalf("len(jobs) = %d, want 2", len(all))
	}
	for _, j := range all {
		if j.Kind != job.KindNewReleaseSearch {
			t.Errorf("kind = %v, want new_release_search", j.Kind)
		}
		if got := job.MessageGroup(j.Kind); got != "amazon-requests" {
			t.Errorf("MessageGroup = %q, want amazon-requests", got)
		}
	}
	if result.TargetCount != 2 || result.EnqueuedCount != 2 || result.UpcomingMerged != 0 {
		t.Errorf("result = %+v, want target=2 enqueued=2 upcoming=0", result)
	}
}

func TestRun_PaperToKindle_EnqueuesOneJobPerAsin(t *testing.T) {
	enq := &fakeEnqueuer{failOn: -1}
	deps := baseDeps(enq)
	deps.AsinListReader = fakeAsinReader{asins: []string{"B0PAPER001"}}

	result, err := Run(context.Background(), deps, Event{CheckType: job.CheckPaperToKindle, ScheduledAt: saleEvent().ScheduledAt})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	all := enq.allJobs()
	if len(all) != 1 || all[0].Kind != job.KindPaperToKindleCheck {
		t.Fatalf("jobs = %+v", all)
	}
	if result.TargetCount != 1 || result.EnqueuedCount != 1 {
		t.Errorf("result = (target=%d enqueued=%d), want (1,1)", result.TargetCount, result.EnqueuedCount)
	}
}

func TestRun_Disabled_EnqueuesNothingAndReportsDisabled(t *testing.T) {
	enq := &fakeEnqueuer{failOn: -1}
	deps := baseDeps(enq)
	deps.ConfigReader = fakeConfig{enabled: false}
	deps.AsinListReader = fakeAsinReader{asins: []string{"B0ASIN0001"}}

	result, err := Run(context.Background(), deps, saleEvent())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(enq.batches) != 0 {
		t.Errorf("disabled checker must not enqueue: %+v", enq.batches)
	}
	if !result.Disabled {
		t.Errorf("result must be disabled when checker is disabled")
	}
}

func TestRun_DedupAsins(t *testing.T) {
	enq := &fakeEnqueuer{failOn: -1}
	deps := baseDeps(enq)
	deps.AsinListReader = fakeAsinReader{asins: []string{"B0ASIN0001", "B0ASIN0001", "B0ASIN0002"}}

	if _, err := Run(context.Background(), deps, saleEvent()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	checks := 0
	for _, j := range enq.allJobs() {
		if j.Kind == job.KindSaleCheck {
			checks++
		}
	}
	if checks != 2 {
		t.Errorf("sale_check count = %d, want 2 (dedup)", checks)
	}
}

func TestRun_JobIDIsDeterministic(t *testing.T) {
	event := saleEvent()
	mk := func() []job.Job {
		enq := &fakeEnqueuer{failOn: -1}
		deps := baseDeps(enq)
		deps.AsinListReader = fakeAsinReader{asins: []string{"B0ASIN0001"}}
		if _, err := Run(context.Background(), deps, event); err != nil {
			t.Fatalf("Run: %v", err)
		}
		return enq.allJobs()
	}
	first := mk()
	second := mk()
	if len(first) != len(second) {
		t.Fatalf("job count differs across runs")
	}
	for i := range first {
		if first[i].JobID != second[i].JobID {
			t.Errorf("JobID not deterministic: %q vs %q", first[i].JobID, second[i].JobID)
		}
	}
}

func TestRun_BatchSizeLimit(t *testing.T) {
	enq := &fakeEnqueuer{failOn: -1}
	deps := baseDeps(enq)
	asins := make([]string, 25)
	for i := range asins {
		asins[i] = "B0ASIN" + string(rune('A'+i)) + "0001"
	}
	deps.AsinListReader = fakeAsinReader{asins: asins}

	if _, err := Run(context.Background(), deps, saleEvent()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// 25 件は 10+10+5 の3 batch。ただし sale_finalize は別 batch。
	for i, b := range enq.batches {
		if len(b) > 10 {
			t.Errorf("batch %d has %d jobs, max 10", i, len(b))
		}
	}
}
