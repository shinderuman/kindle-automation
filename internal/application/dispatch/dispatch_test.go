package dispatch

import (
	"context"
	"errors"
	"fmt"
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
	err     error
}

func (f fakeConfig) IsEnabled(_ context.Context, _ job.CheckType) (bool, error) {
	return f.enabled, f.err
}

type fakeUpcoming struct {
	called bool
	merged int
	err    error
}

func (f *fakeUpcoming) MergeUpcoming(_ context.Context) (int, error) {
	f.called = true
	return f.merged, f.err
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

	all := enq.allJobs()
	if len(all) != 3 {
		t.Fatalf("len(jobs) = %d, want 3", len(all))
	}
	lastBatch := enq.batches[len(enq.batches)-1]
	if len(lastBatch) != 1 || lastBatch[0].Kind != job.KindSaleFinalize {
		t.Fatalf("last batch should be sale_finalize only: %+v", lastBatch)
	}
	for i, j := range all {
		if j.Kind == job.KindSaleFinalize && i != len(all)-1 {
			t.Errorf("sale_finalize at index %d, want last", i)
		}
	}
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

func TestRun_ConfigError_StopsBeforeEnqueue(t *testing.T) {
	enq := &fakeEnqueuer{failOn: -1}
	deps := baseDeps(enq)
	deps.ConfigReader = fakeConfig{enabled: true, err: errors.New("config down")}
	deps.AsinListReader = fakeAsinReader{asins: []string{"B0ASIN0001"}}

	_, err := Run(context.Background(), deps, saleEvent())
	if err == nil {
		t.Fatal("want error when config load fails")
	}
	if len(enq.batches) != 0 {
		t.Errorf("config error must not enqueue: %+v", enq.batches)
	}
}

func TestRun_UnknownCheckType_ReturnsError(t *testing.T) {
	enq := &fakeEnqueuer{failOn: -1}
	deps := baseDeps(enq)

	_, err := Run(context.Background(), deps, Event{CheckType: job.CheckType("bogus"), ScheduledAt: saleEvent().ScheduledAt})
	if err == nil {
		t.Fatal("want error for unknown check_type")
	}
	if len(enq.batches) != 0 {
		t.Errorf("unknown check_type must not enqueue: %+v", enq.batches)
	}
}

func TestRun_Sale_MergeUpcomingError_Stops(t *testing.T) {
	enq := &fakeEnqueuer{failOn: -1}
	deps := baseDeps(enq)
	deps.UpcomingMerger = &fakeUpcoming{err: errors.New("merge down")}
	deps.AsinListReader = fakeAsinReader{asins: []string{"B0ASIN0001"}}

	_, err := Run(context.Background(), deps, saleEvent())
	if err == nil {
		t.Fatal("want error when MergeUpcoming fails")
	}
	if len(enq.batches) != 0 {
		t.Errorf("merge error must not enqueue: %+v", enq.batches)
	}
}

func TestRun_Sale_LoadAsinsError_StopsAfterMerge(t *testing.T) {
	enq := &fakeEnqueuer{failOn: -1}
	upc := &fakeUpcoming{}
	deps := baseDeps(enq)
	deps.UpcomingMerger = upc
	deps.AsinListReader = fakeAsinReader{err: errors.New("load down")}

	_, err := Run(context.Background(), deps, saleEvent())
	if err == nil {
		t.Fatal("want error when LoadAsins fails")
	}
	if !upc.called {
		t.Errorf("MergeUpcoming must run before LoadAsins")
	}
	if len(enq.batches) != 0 {
		t.Errorf("load error must not enqueue: %+v", enq.batches)
	}
}

func TestRun_Sale_FinalizeEnqueueFailure_ReturnsError(t *testing.T) {
	enq := &fakeEnqueuer{failOn: 1} // call0=sale_check 成功、call1=sale_finalize 失敗
	deps := baseDeps(enq)
	deps.AsinListReader = fakeAsinReader{asins: []string{"B0ASIN0001"}}

	_, err := Run(context.Background(), deps, saleEvent())
	if err == nil {
		t.Fatal("want error when sale_finalize enqueue fails")
	}
}

func TestRun_Sale_MidBatchFailure_NoFinalize(t *testing.T) {
	enq := &fakeEnqueuer{failOn: 1} // 12件は 10+2。2件目(呼び出し1)で失敗
	deps := baseDeps(enq)
	asins := make([]string, 12)
	for i := range asins {
		asins[i] = fmt.Sprintf("B0ASIN%05d", i)
	}
	deps.AsinListReader = fakeAsinReader{asins: asins}

	_, err := Run(context.Background(), deps, saleEvent())
	if err == nil {
		t.Fatal("want error on mid-batch failure")
	}
	for _, j := range enq.allJobs() {
		if j.Kind == job.KindSaleFinalize {
			t.Errorf("sale_finalize must not be enqueued on mid-batch failure")
		}
	}
}

func TestRun_NewRelease_LoadAuthorsError_Stops(t *testing.T) {
	enq := &fakeEnqueuer{failOn: -1}
	deps := baseDeps(enq)
	deps.AuthorReader = fakeAuthorReader{err: errors.New("authors down")}

	_, err := Run(context.Background(), deps, Event{CheckType: job.CheckNewRelease, ScheduledAt: saleEvent().ScheduledAt})
	if err == nil {
		t.Fatal("want error when LoadAuthorNames fails")
	}
	if len(enq.batches) != 0 {
		t.Errorf("authors load error must not enqueue: %+v", enq.batches)
	}
}

func TestRun_NewRelease_EnqueueFailure_ReturnsError(t *testing.T) {
	enq := &fakeEnqueuer{failOn: 0}
	deps := baseDeps(enq)
	deps.AuthorReader = fakeAuthorReader{names: []string{"海李"}}

	_, err := Run(context.Background(), deps, Event{CheckType: job.CheckNewRelease, ScheduledAt: saleEvent().ScheduledAt})
	if err == nil {
		t.Fatal("want error on new_release enqueue failure")
	}
}

func TestRun_PaperToKindle_LoadAsinsError_Stops(t *testing.T) {
	enq := &fakeEnqueuer{failOn: -1}
	deps := baseDeps(enq)
	deps.AsinListReader = fakeAsinReader{err: errors.New("paper load down")}

	_, err := Run(context.Background(), deps, Event{CheckType: job.CheckPaperToKindle, ScheduledAt: saleEvent().ScheduledAt})
	if err == nil {
		t.Fatal("want error when paper LoadAsins fails")
	}
	if len(enq.batches) != 0 {
		t.Errorf("paper load error must not enqueue: %+v", enq.batches)
	}
}

func TestRun_PaperToKindle_EnqueueFailure_ReturnsError(t *testing.T) {
	enq := &fakeEnqueuer{failOn: 0}
	deps := baseDeps(enq)
	deps.AsinListReader = fakeAsinReader{asins: []string{"B0PAPER001"}}

	_, err := Run(context.Background(), deps, Event{CheckType: job.CheckPaperToKindle, ScheduledAt: saleEvent().ScheduledAt})
	if err == nil {
		t.Fatal("want error on paper enqueue failure")
	}
}

func TestRun_NewRelease_DedupAuthors(t *testing.T) {
	enq := &fakeEnqueuer{failOn: -1}
	deps := baseDeps(enq)
	deps.AuthorReader = fakeAuthorReader{names: []string{"海李", "海李", "作者B"}}

	if _, err := Run(context.Background(), deps, Event{CheckType: job.CheckNewRelease, ScheduledAt: saleEvent().ScheduledAt}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	searches := 0
	for _, j := range enq.allJobs() {
		if j.Kind == job.KindNewReleaseSearch {
			searches++
		}
	}
	if searches != 2 {
		t.Errorf("new_release_search count = %d, want 2 (dedup)", searches)
	}
}

func TestRun_PaperToKindle_DedupAsins(t *testing.T) {
	enq := &fakeEnqueuer{failOn: -1}
	deps := baseDeps(enq)
	deps.AsinListReader = fakeAsinReader{asins: []string{"B0PAPER001", "B0PAPER001", "B0PAPER002"}}

	if _, err := Run(context.Background(), deps, Event{CheckType: job.CheckPaperToKindle, ScheduledAt: saleEvent().ScheduledAt}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	checks := 0
	for _, j := range enq.allJobs() {
		if j.Kind == job.KindPaperToKindleCheck {
			checks++
		}
	}
	if checks != 2 {
		t.Errorf("paper_to_kindle_check count = %d, want 2 (dedup)", checks)
	}
}

func TestRun_Sale_DedupFiltersEmptyStrings(t *testing.T) {
	enq := &fakeEnqueuer{failOn: -1}
	deps := baseDeps(enq)
	deps.AsinListReader = fakeAsinReader{asins: []string{"B0ASIN0001", "", "B0ASIN0002"}}

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
		t.Errorf("sale_check count = %d, want 2 (empty filtered)", checks)
	}
}

func TestRun_PaperToKindle_AmazonJobsUseAmazonRequestsMessageGroup(t *testing.T) {
	enq := &fakeEnqueuer{failOn: -1}
	deps := baseDeps(enq)
	deps.AsinListReader = fakeAsinReader{asins: []string{"B0PAPER001"}}

	if _, err := Run(context.Background(), deps, Event{CheckType: job.CheckPaperToKindle, ScheduledAt: saleEvent().ScheduledAt}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, j := range enq.allJobs() {
		if got := job.MessageGroup(j.Kind); got != "amazon-requests" {
			t.Errorf("MessageGroup(%v) = %q, want amazon-requests", j.Kind, got)
		}
	}
}

func TestRun_Sale_CycleIDIsUTCNormalized(t *testing.T) {
	jst := time.FixedZone("JST", 9*60*60)
	enq := &fakeEnqueuer{failOn: -1}
	deps := baseDeps(enq)
	deps.AsinListReader = fakeAsinReader{asins: []string{"B0ASIN0001"}}
	event := Event{CheckType: job.CheckSale, ScheduledAt: time.Date(2026, 7, 23, 9, 0, 0, 0, jst)}

	if _, err := Run(context.Background(), deps, event); err != nil {
		t.Fatalf("Run: %v", err)
	}
	const wantCycle = "sale:2026-07-23T00:00:00Z"
	for _, j := range enq.allJobs() {
		if j.CycleID != wantCycle {
			t.Errorf("CycleID = %q, want %q", j.CycleID, wantCycle)
		}
	}
	check := enq.allJobs()[0]
	wantJobID := "sale_check:" + wantCycle + ":B0ASIN0001"
	if check.JobID != wantJobID {
		t.Errorf("sale_check JobID = %q, want %q", check.JobID, wantJobID)
	}
}

func TestRun_JobID_DiscriminatesByKindAndTarget(t *testing.T) {
	at := time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC)

	saleEnq := &fakeEnqueuer{failOn: -1}
	saleDeps := baseDeps(saleEnq)
	saleDeps.AsinListReader = fakeAsinReader{asins: []string{"B0SHARED001"}}
	if _, err := Run(context.Background(), saleDeps, Event{CheckType: job.CheckSale, ScheduledAt: at}); err != nil {
		t.Fatalf("sale Run: %v", err)
	}

	paperEnq := &fakeEnqueuer{failOn: -1}
	paperDeps := baseDeps(paperEnq)
	paperDeps.AsinListReader = fakeAsinReader{asins: []string{"B0SHARED001"}}
	if _, err := Run(context.Background(), paperDeps, Event{CheckType: job.CheckPaperToKindle, ScheduledAt: at}); err != nil {
		t.Fatalf("paper Run: %v", err)
	}

	saleCheckID := saleEnq.allJobs()[0].JobID
	paperCheckID := paperEnq.allJobs()[0].JobID
	if saleCheckID == paperCheckID {
		t.Errorf("same target across kinds must differ: sale=%q paper=%q", saleCheckID, paperCheckID)
	}
}
