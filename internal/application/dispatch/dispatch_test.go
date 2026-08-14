package dispatch

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/shinderuman/kindle-automation/internal/domain/scheduling"
	"github.com/shinderuman/kindle-automation/internal/job"
)

type fakeEnqueuer struct {
	batches [][]job.Job
	calls   int
	failOn  int
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
	for _, batch := range f.batches {
		out = append(out, batch...)
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

func baseDeps(enq *fakeEnqueuer) Dependencies {
	return Dependencies{
		AsinListReader: fakeAsinReader{},
		AuthorReader:   fakeAuthorReader{},
		ConfigReader:   fakeConfig{enabled: true},
		Enqueuer:       enq,
		Keys:           Keys{Unprocessed: "unprocessed", Authors: "authors", PaperBooks: "paper"},
	}
}

var cycleStart = time.Date(2026, 8, 14, 15, 0, 0, 0, time.UTC)

func eventAtSlot(checkType job.CheckType, slot int) Event {
	offset := time.Duration(0)
	switch checkType {
	case job.CheckNewRelease:
		offset = time.Minute
	case job.CheckPaperToKindle:
		offset = 2 * time.Minute
	}
	return Event{
		CheckType:   checkType,
		ScheduledAt: cycleStart.Add(time.Duration(slot)*dispatchInterval + offset),
	}
}

func valuesForSlot(t *testing.T, prefix string, count, slot, slotCount int) []string {
	t.Helper()
	values := make([]string, 0, count)
	for i := 0; len(values) < count; i++ {
		value := fmt.Sprintf("%s%06d", prefix, i)
		index, err := scheduling.ShardIndex(value, slotCount)
		if err != nil {
			t.Fatalf("ShardIndex: %v", err)
		}
		if index == slot {
			values = append(values, value)
		}
	}
	return values
}

func TestRun_SaleFirstSlotEnqueuesOnlyItsShard(t *testing.T) {
	enq := &fakeEnqueuer{failOn: -1}
	deps := baseDeps(enq)
	selected := valuesForSlot(t, "B0SALE", 2, 0, 24)
	other := valuesForSlot(t, "B0OTHER", 1, 1, 24)[0]
	deps.AsinListReader = fakeAsinReader{asins: []string{selected[0], other, selected[1]}}

	result, err := Run(context.Background(), deps, eventAtSlot(job.CheckSale, 0))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	jobs := enq.allJobs()
	if len(jobs) != 2 {
		t.Fatalf("jobs = %d, want 2", len(jobs))
	}
	for _, queued := range jobs {
		if queued.Kind != job.KindSaleCheck || queued.Target.ASIN == other {
			t.Errorf("unexpected job: %+v", queued)
		}
	}
	if result.CycleID != "sale:2026-08-14T15:00:00Z" || result.SlotIndex != 0 || result.SlotCount != 24 {
		t.Errorf("cycle metadata = %+v", result)
	}
	if result.CycleTargetCount != 3 || result.TargetCount != 2 || result.EnqueuedCount != 2 {
		t.Errorf("counts = %+v", result)
	}
}

func TestRun_SaleFinalSlotEnqueuesFinalizeLast(t *testing.T) {
	enq := &fakeEnqueuer{failOn: -1}
	deps := baseDeps(enq)
	deps.AsinListReader = fakeAsinReader{asins: valuesForSlot(t, "B0SALE", 2, 23, 24)}

	result, err := Run(context.Background(), deps, eventAtSlot(job.CheckSale, 23))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	jobs := enq.allJobs()
	if len(jobs) != 3 || jobs[len(jobs)-1].Kind != job.KindSaleFinalize {
		t.Fatalf("jobs = %+v", jobs)
	}
	if result.TargetCount != 2 || result.EnqueuedCount != 3 {
		t.Errorf("counts = %+v", result)
	}
}

func TestRun_SaleFinalSlotEnqueuesFinalizeWhenShardIsEmpty(t *testing.T) {
	enq := &fakeEnqueuer{failOn: -1}
	deps := baseDeps(enq)
	deps.AsinListReader = fakeAsinReader{}

	result, err := Run(context.Background(), deps, eventAtSlot(job.CheckSale, 23))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	jobs := enq.allJobs()
	if len(jobs) != 1 || jobs[0].Kind != job.KindSaleFinalize {
		t.Fatalf("jobs = %+v", jobs)
	}
	if result.TargetCount != 0 || result.EnqueuedCount != 1 {
		t.Errorf("counts = %+v", result)
	}
}

func TestRun_SaleFailurePreventsFinalize(t *testing.T) {
	enq := &fakeEnqueuer{failOn: 0}
	deps := baseDeps(enq)
	deps.AsinListReader = fakeAsinReader{asins: valuesForSlot(t, "B0SALE", 1, 23, 24)}

	if _, err := Run(context.Background(), deps, eventAtSlot(job.CheckSale, 23)); err == nil {
		t.Fatal("Run should fail")
	}
	for _, queued := range enq.allJobs() {
		if queued.Kind == job.KindSaleFinalize {
			t.Fatal("sale_finalize must not be enqueued")
		}
	}
}

func TestRun_SaleFinalizeFailureIsReturned(t *testing.T) {
	enq := &fakeEnqueuer{failOn: 1}
	deps := baseDeps(enq)
	deps.AsinListReader = fakeAsinReader{asins: valuesForSlot(t, "B0SALE", 1, 23, 24)}

	if _, err := Run(context.Background(), deps, eventAtSlot(job.CheckSale, 23)); err == nil {
		t.Fatal("Run should fail")
	}
}

func TestRun_NewReleaseAndPaperUseCurrentShard(t *testing.T) {
	tests := []struct {
		name      string
		checkType job.CheckType
		kind      job.Kind
	}{
		{name: "new release", checkType: job.CheckNewRelease, kind: job.KindNewReleaseSearch},
		{name: "paper to kindle", checkType: job.CheckPaperToKindle, kind: job.KindPaperToKindleCheck},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			enq := &fakeEnqueuer{failOn: -1}
			deps := baseDeps(enq)
			selected := valuesForSlot(t, "TARGET", 2, 17, 72)
			other := valuesForSlot(t, "OTHER", 1, 18, 72)[0]
			if tc.checkType == job.CheckNewRelease {
				deps.AuthorReader = fakeAuthorReader{names: []string{selected[0], other, selected[1]}}
			} else {
				deps.AsinListReader = fakeAsinReader{asins: []string{selected[0], other, selected[1]}}
			}

			result, err := Run(context.Background(), deps, eventAtSlot(tc.checkType, 17))
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if result.CycleTargetCount != 3 || result.TargetCount != 2 || result.EnqueuedCount != 2 || result.SlotCount != 72 {
				t.Errorf("result = %+v", result)
			}
			for _, queued := range enq.allJobs() {
				if queued.Kind != tc.kind || job.MessageGroup(queued.Kind) != "amazon-requests" {
					t.Errorf("unexpected job: %+v", queued)
				}
			}
		})
	}
}

func TestRun_EachTargetAppearsOncePerCycle(t *testing.T) {
	tests := []struct {
		name      string
		checkType job.CheckType
		slots     int
		kind      job.Kind
	}{
		{name: "sale", checkType: job.CheckSale, slots: 24, kind: job.KindSaleCheck},
		{name: "new release", checkType: job.CheckNewRelease, slots: 72, kind: job.KindNewReleaseSearch},
		{name: "paper", checkType: job.CheckPaperToKindle, slots: 72, kind: job.KindPaperToKindleCheck},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			values := []string{"TARGET0001", "TARGET0002", "TARGET0001", "", "TARGET0003"}
			seen := map[string]int{}
			for slot := 0; slot < tc.slots; slot++ {
				enq := &fakeEnqueuer{failOn: -1}
				deps := baseDeps(enq)
				deps.AsinListReader = fakeAsinReader{asins: values}
				deps.AuthorReader = fakeAuthorReader{names: values}
				if _, err := Run(context.Background(), deps, eventAtSlot(tc.checkType, slot)); err != nil {
					t.Fatalf("slot %d: %v", slot, err)
				}
				for _, queued := range enq.allJobs() {
					if queued.Kind != tc.kind {
						continue
					}
					target := queued.Target.ASIN
					if tc.checkType == job.CheckNewRelease {
						target = queued.Target.AuthorName
					}
					seen[target]++
				}
			}
			for _, target := range []string{"TARGET0001", "TARGET0002", "TARGET0003"} {
				if seen[target] != 1 {
					t.Errorf("%s count = %d, want 1", target, seen[target])
				}
			}
		})
	}
}

func TestSelectShard_IsStableAcrossOrderAndUnrelatedInsertions(t *testing.T) {
	values := []string{"TARGET0001", "TARGET0002", "TARGET0003"}
	index, err := scheduling.ShardIndex(values[0], 24)
	if err != nil {
		t.Fatalf("ShardIndex: %v", err)
	}
	first, err := selectShard(values, index, 24)
	if err != nil {
		t.Fatalf("selectShard: %v", err)
	}
	second, err := selectShard([]string{"ADDED", values[2], values[0], values[1]}, index, 24)
	if err != nil {
		t.Fatalf("selectShard reordered: %v", err)
	}
	if !contains(first, values[0]) || !contains(second, values[0]) {
		t.Fatalf("target moved after list edit: first=%v second=%v", first, second)
	}
}

func TestRun_UsesSameCycleIDAcrossWindow(t *testing.T) {
	cycleIDs := map[string]struct{}{}
	for _, slot := range []int{0, 12, 23} {
		enq := &fakeEnqueuer{failOn: -1}
		deps := baseDeps(enq)
		deps.AsinListReader = fakeAsinReader{}
		result, err := Run(context.Background(), deps, eventAtSlot(job.CheckSale, slot))
		if err != nil {
			t.Fatalf("slot %d: %v", slot, err)
		}
		cycleIDs[result.CycleID] = struct{}{}
	}
	if len(cycleIDs) != 1 {
		t.Fatalf("cycle IDs = %v, want one", cycleIDs)
	}

	next := eventAtSlot(job.CheckSale, 0)
	next.ScheduledAt = next.ScheduledAt.Add(saleWindow)
	result, err := Run(context.Background(), baseDeps(&fakeEnqueuer{failOn: -1}), next)
	if err != nil {
		t.Fatalf("next cycle: %v", err)
	}
	if _, exists := cycleIDs[result.CycleID]; exists {
		t.Fatalf("next window reused cycle ID %s", result.CycleID)
	}
}

func TestRun_JobIDsAreDeterministic(t *testing.T) {
	target := valuesForSlot(t, "B0SALE", 1, 3, 24)[0]
	run := func() job.Job {
		enq := &fakeEnqueuer{failOn: -1}
		deps := baseDeps(enq)
		deps.AsinListReader = fakeAsinReader{asins: []string{target}}
		if _, err := Run(context.Background(), deps, eventAtSlot(job.CheckSale, 3)); err != nil {
			t.Fatalf("Run: %v", err)
		}
		return enq.allJobs()[0]
	}
	first, second := run(), run()
	if first.JobID != second.JobID || first.CycleID != second.CycleID {
		t.Fatalf("jobs differ: first=%+v second=%+v", first, second)
	}
}

func TestRun_BatchesAtMostTenJobs(t *testing.T) {
	enq := &fakeEnqueuer{failOn: -1}
	deps := baseDeps(enq)
	deps.AsinListReader = fakeAsinReader{asins: valuesForSlot(t, "B0SALE", 25, 3, 24)}

	if _, err := Run(context.Background(), deps, eventAtSlot(job.CheckSale, 3)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(enq.batches) != 3 {
		t.Fatalf("batch count = %d, want 3", len(enq.batches))
	}
	for i, batch := range enq.batches {
		if len(batch) > 10 {
			t.Errorf("batch %d size = %d", i, len(batch))
		}
	}
}

func TestRun_DisabledIncludesCycleMetadataAndEnqueuesNothing(t *testing.T) {
	enq := &fakeEnqueuer{failOn: -1}
	deps := baseDeps(enq)
	deps.ConfigReader = fakeConfig{enabled: false}

	result, err := Run(context.Background(), deps, eventAtSlot(job.CheckNewRelease, 8))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !result.Disabled || result.CycleID == "" || result.SlotIndex != 8 || result.SlotCount != 72 {
		t.Errorf("result = %+v", result)
	}
	if len(enq.batches) != 0 {
		t.Fatalf("jobs = %+v", enq.batches)
	}
}

func TestRun_RejectsUnknownCheckType(t *testing.T) {
	enq := &fakeEnqueuer{failOn: -1}
	deps := baseDeps(enq)

	if _, err := Run(context.Background(), deps, Event{CheckType: job.CheckType("bogus"), ScheduledAt: cycleStart}); err == nil {
		t.Fatal("Run should fail")
	}
	if len(enq.batches) != 0 {
		t.Fatalf("jobs = %+v", enq.batches)
	}
}

func TestRun_PropagatesDependencyFailures(t *testing.T) {
	tests := []struct {
		name  string
		event Event
		setup func(*Dependencies)
	}{
		{
			name:  "config",
			event: eventAtSlot(job.CheckSale, 0),
			setup: func(deps *Dependencies) {
				deps.ConfigReader = fakeConfig{enabled: true, err: errors.New("config down")}
			},
		},
		{
			name:  "unprocessed",
			event: eventAtSlot(job.CheckSale, 0),
			setup: func(deps *Dependencies) { deps.AsinListReader = fakeAsinReader{err: errors.New("load down")} },
		},
		{
			name:  "authors",
			event: eventAtSlot(job.CheckNewRelease, 0),
			setup: func(deps *Dependencies) { deps.AuthorReader = fakeAuthorReader{err: errors.New("load down")} },
		},
		{
			name:  "paper",
			event: eventAtSlot(job.CheckPaperToKindle, 0),
			setup: func(deps *Dependencies) { deps.AsinListReader = fakeAsinReader{err: errors.New("load down")} },
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			enq := &fakeEnqueuer{failOn: -1}
			deps := baseDeps(enq)
			tc.setup(&deps)
			if _, err := Run(context.Background(), deps, tc.event); err == nil {
				t.Fatal("Run should fail")
			}
			if len(enq.batches) != 0 {
				t.Fatalf("jobs = %+v", enq.batches)
			}
		})
	}
}

func TestRun_PropagatesEnqueueFailure(t *testing.T) {
	tests := []struct {
		name      string
		checkType job.CheckType
	}{
		{name: "new release", checkType: job.CheckNewRelease},
		{name: "paper", checkType: job.CheckPaperToKindle},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			enq := &fakeEnqueuer{failOn: 0}
			deps := baseDeps(enq)
			value := valuesForSlot(t, "TARGET", 1, 4, 72)
			deps.AsinListReader = fakeAsinReader{asins: value}
			deps.AuthorReader = fakeAuthorReader{names: value}
			if _, err := Run(context.Background(), deps, eventAtSlot(tc.checkType, 4)); err == nil {
				t.Fatal("Run should fail")
			}
		})
	}
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
