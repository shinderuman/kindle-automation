package schedulechecks

import (
	"context"
	"errors"
	"testing"

	"github.com/shinderuman/kindle-automation/internal/application/dispatch"
	"github.com/shinderuman/kindle-automation/internal/job"
	"github.com/shinderuman/kindle-automation/internal/storage"
)

// --- HandleEvent 振り分けテスト用 stub ---

type recordingEnqueuer struct {
	jobs []job.Job
}

func (e *recordingEnqueuer) EnqueueBatch(_ context.Context, jobs []job.Job) error {
	e.jobs = append(e.jobs, jobs...)
	return nil
}

type fakeMerger struct{}

func (fakeMerger) MergeUpcoming(_ context.Context) (int, error) { return 0, nil }

type enabledConfig struct{}

func (enabledConfig) IsEnabled(_ context.Context, _ job.CheckType) (bool, error) { return true, nil }

type fakeSender struct {
	called bool
	err    error
}

func (s *fakeSender) Send(_ context.Context, _ string) error {
	s.called = true
	return s.err
}

func newScheduler(store storage.ObjectStore, enq *recordingEnqueuer, sender *fakeSender) *Scheduler {
	return &Scheduler{
		Deps: dispatch.Dependencies{
			AsinListReader: asinListReader{store: store},
			AuthorReader:   authorReader{store: store},
			ConfigReader:   enabledConfig{},
			Enqueuer:       enq,
			UpcomingMerger: fakeMerger{},
			Keys:           dispatch.Keys{Unprocessed: "unprocessed_asins.json", Authors: "authors.json", PaperBooks: "paper_books_asins.json"},
		},
		ErrorSender: sender,
	}
}

// Scheduler イベントは decode → dispatch.Run へ。空対象の Sale でも sale_finalize を1件投入する。
func TestHandleEvent_SchedulerRouteDispatches(t *testing.T) {
	store := storage.NewMemStore()
	enq := &recordingEnqueuer{}
	sched := newScheduler(store, enq, nil)
	body := `{"version":1,"source":"scheduler","check_type":"sale","scheduled_at":"2026-08-09T00:00:00Z"}`

	if err := sched.HandleEvent(context.Background(), []byte(body)); err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}
	finalize := 0
	for _, j := range enq.jobs {
		if j.Kind == job.KindSaleFinalize {
			finalize++
		}
	}
	if finalize != 1 {
		t.Errorf("sale_finalize count = %d, want 1 (jobs=%v)", finalize, enq.jobs)
	}
}

// Scheduler 入力の validation 失敗は error として伝播する。
func TestHandleEvent_SchedulerInvalidInputErrors(t *testing.T) {
	sched := newScheduler(storage.NewMemStore(), &recordingEnqueuer{}, nil)
	body := `{"version":1,"source":"scheduler","check_type":"bogus","scheduled_at":"2026-08-09T00:00:00Z"}`
	if err := sched.HandleEvent(context.Background(), []byte(body)); err == nil {
		t.Fatal("HandleEvent should fail on invalid schedule input")
	}
}

// Alarm イベントは Slack error channel へ通知する。
func TestHandleEvent_AlarmRouteNotifies(t *testing.T) {
	sender := &fakeSender{}
	sched := newScheduler(storage.NewMemStore(), &recordingEnqueuer{}, sender)
	body := `{"AlarmName":"WorkDLQDepth"}`

	if err := sched.HandleEvent(context.Background(), []byte(body)); err != nil {
		t.Fatalf("HandleEvent alarm: %v", err)
	}
	if !sender.called {
		t.Errorf("error sender must be called for alarm event")
	}
}

// Alarm 通知失敗は error を返す（Lambda 経由の再試行のため）。
func TestHandleAlarm_SendFailureReturnsError(t *testing.T) {
	sender := &fakeSender{err: errors.New("slack down")}
	sched := &Scheduler{ErrorSender: sender}
	if err := sched.HandleAlarm(context.Background(), "WorkDLQDepth"); err == nil {
		t.Fatal("HandleAlarm should return error on send failure")
	}
}

// ErrorSender 未設定でも alarm はログのみで成功する。
func TestHandleAlarm_NoSenderSucceeds(t *testing.T) {
	sched := &Scheduler{}
	if err := sched.HandleAlarm(context.Background(), "WorkDLQDepth"); err != nil {
		t.Fatalf("HandleAlarm with no sender should succeed: %v", err)
	}
}
