package schedulechecks

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/aws/smithy-go"

	"github.com/shinderuman/kindle-automation/internal/application/dispatch"
	"github.com/shinderuman/kindle-automation/internal/config"
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
	calls  int
	msg    string
	err    error
}

func (s *fakeSender) Send(_ context.Context, msg string) error {
	s.called = true
	s.calls++
	s.msg = msg
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

// CloudWatch Alarm 直接 invoke の実イベント（AWS 公式形式）。
const alarmEventBody = `{"source":"aws.cloudwatch","alarmArn":"arn:aws:cloudwatch:us-east-1:111122223333:alarm:kindle-automation-work-dlq","accountId":"111122223333","time":"2026-08-04T12:36:15.490+0000","region":"us-east-1","alarmData":{"alarmName":"kindle-automation-work-dlq","state":{"value":"ALARM","reason":"DLQ depth","timestamp":"2026-08-04T12:36:15.490+0000"},"previousState":{"value":"OK","reason":"","timestamp":"2026-08-04T12:31:29.595+0000"}}}`

// Alarm イベント（ALARM 遷移）は alarmData.alarmName を取り出して Slack error channel へ通知する。
func TestHandleEvent_AlarmRouteNotifies(t *testing.T) {
	sender := &fakeSender{}
	sched := newScheduler(storage.NewMemStore(), &recordingEnqueuer{}, sender)

	if err := sched.HandleEvent(context.Background(), []byte(alarmEventBody)); err != nil {
		t.Fatalf("HandleEvent alarm: %v", err)
	}
	if !sender.called {
		t.Errorf("error sender must be called for alarm event")
	}
	// 同一 Alarm 状態で通知を増やさない（SPECIFICATION.md 17.2）。1 event = 1 通知。
	if sender.calls != 1 {
		t.Errorf("sender calls = %d, want exactly 1 per alarm event", sender.calls)
	}
	if !strings.Contains(sender.msg, "kindle-automation-work-dlq") {
		t.Errorf("notify message must include alarm name, got %q", sender.msg)
	}
}

// ALARM 未満の状態（OK/INSUFFICIENT_DATA）では通知せず正常終了する（SPECIFICATION.md 17.2）。
func TestHandleEvent_AlarmNonAlarmStateSkipsNotify(t *testing.T) {
	sender := &fakeSender{}
	sched := newScheduler(storage.NewMemStore(), &recordingEnqueuer{}, sender)
	body := `{"source":"aws.cloudwatch","alarmData":{"alarmName":"kindle-automation-work-dlq","state":{"value":"OK"}}}`

	if err := sched.HandleEvent(context.Background(), []byte(body)); err != nil {
		t.Fatalf("non-ALARM state must not error: %v", err)
	}
	if sender.called {
		t.Errorf("error sender must not be called for non-ALARM state")
	}
}

// Alarm payload の decode/validation 失敗は error として伝播する。
func TestHandleEvent_AlarmInvalidInputErrors(t *testing.T) {
	sched := newScheduler(storage.NewMemStore(), &recordingEnqueuer{}, &fakeSender{})
	body := `{"source":"aws.cloudwatch","alarmData":{"state":{"value":"ALARM"}}}`
	if err := sched.HandleEvent(context.Background(), []byte(body)); err == nil {
		t.Fatal("HandleEvent must fail on alarm input missing alarmData.alarmName")
	}
}

// 未知の source は error として伝播する（scheduler/cloudwatch 以外の誤 invoke を表面化）。
func TestHandleEvent_UnknownSourceErrors(t *testing.T) {
	sched := newScheduler(storage.NewMemStore(), &recordingEnqueuer{}, &fakeSender{})
	body := `{"source":"aws.somethingelse"}`
	if err := sched.HandleEvent(context.Background(), []byte(body)); err == nil {
		t.Fatal("HandleEvent must fail on unknown source")
	}
}

// 生イベントが JSON でない場合は error として伝播する。
func TestHandleEvent_BrokenJSONErrors(t *testing.T) {
	sched := newScheduler(storage.NewMemStore(), &recordingEnqueuer{}, &fakeSender{})
	if err := sched.HandleEvent(context.Background(), []byte("not-json")); err == nil {
		t.Fatal("HandleEvent must fail on broken JSON")
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

// 同一 Scheduler（composition root 相当）を再構築せず HandleEvent を2回呼び、間に stub S3 の
// checker 設定を変更すると2回目が新値を読むことを検証する（warm execution environment でも反映）。
func TestHandleEvent_ReadsCheckerConfigPerInvocation(t *testing.T) {
	store := storage.NewMemStore()
	store.Seed("checker_configs.json", `{"SaleChecker":{"Enabled":true,"GistID":"g","GistFilename":"sale.md","SaleThreshold":100,"PointPercent":10,"PriceChangeAmount":50}}`)
	enq := &recordingEnqueuer{}
	sched := &Scheduler{
		Deps: dispatch.Dependencies{
			AsinListReader: asinListReader{store: store},
			AuthorReader:   authorReader{store: store},
			ConfigReader:   checkerConfigReader{store: store, key: "checker_configs.json"},
			Enqueuer:       enq,
			UpcomingMerger: fakeMerger{},
			Keys:           dispatch.Keys{Unprocessed: "unprocessed_asins.json", Authors: "authors.json", PaperBooks: "paper_books_asins.json"},
		},
	}
	body := `{"version":1,"source":"scheduler","check_type":"sale","scheduled_at":"2026-08-09T00:00:00Z"}`

	// call1: SaleChecker 有効 → sale_finalize 1件を投入する。
	if err := sched.HandleEvent(context.Background(), []byte(body)); err != nil {
		t.Fatalf("first HandleEvent: %v", err)
	}
	after1 := len(enq.jobs)
	if after1 == 0 {
		t.Fatal("first call should enqueue when SaleChecker is enabled")
	}
	// Scheduler 再構築なしで S3 の checker 設定を無効化する。
	store.Seed("checker_configs.json", `{"SaleChecker":{"Enabled":false}}`)
	// call2: SaleChecker 無効 → 投入しない（新値を読んでいる証拠）。
	if err := sched.HandleEvent(context.Background(), []byte(body)); err != nil {
		t.Fatalf("second HandleEvent: %v", err)
	}
	if len(enq.jobs) != after1 {
		t.Errorf("second call should not enqueue when disabled: before=%d after=%d", after1, len(enq.jobs))
	}
}

// --- config 読込失敗・dispatch error 伝播 ---

// failingConfigStore は checker_configs.json 読込失敗を模倣する ObjectStore stub。
// checkerConfigReader.IsEnabled が S3 一時障害を dispatch へ伝播することを検証するため Get で必ず失敗する。
type failingConfigStore struct{}

func (failingConfigStore) Get(_ context.Context, _ string) (storage.Object, error) {
	return storage.Object{}, errors.New("s3 transient: request timeout")
}

func (failingConfigStore) Put(_ context.Context, _ string, _ []byte, _ storage.PutOptions) error {
	return nil
}

// Checker 設定の読込失敗（S3 Get error）は dispatch.Run へ伝播し HandleEvent の error になる。
// 設定読込成功前に SQS 投入は行わない（SPECIFICATION.md 16/18.3）。
func TestHandleEvent_CheckerConfigLoadFailurePropagates(t *testing.T) {
	enq := &recordingEnqueuer{}
	sched := &Scheduler{
		Deps: dispatch.Dependencies{
			ConfigReader:   checkerConfigReader{store: failingConfigStore{}, key: "checker_configs.json"},
			Enqueuer:       enq,
			UpcomingMerger: fakeMerger{},
			Keys:           dispatch.Keys{Unprocessed: "unprocessed_asins.json", Authors: "authors.json", PaperBooks: "paper_books_asins.json"},
		},
	}
	body := `{"version":1,"source":"scheduler","check_type":"sale","scheduled_at":"2026-08-09T00:00:00Z"}`

	if err := sched.HandleEvent(context.Background(), []byte(body)); err == nil {
		t.Fatal("HandleEvent must propagate checker config load failure")
	}
	if len(enq.jobs) != 0 {
		t.Errorf("must not enqueue on config load failure: %v", enq.jobs)
	}
}

// enqueuer 失敗は dispatch.Run から HandleEvent へ error として伝播する（SPECIFICATION.md 7.3）。
type failingEnqueuer struct{ err error }

func (e *failingEnqueuer) EnqueueBatch(_ context.Context, _ []job.Job) error { return e.err }

func TestHandleEvent_DispatchEnqueueFailurePropagates(t *testing.T) {
	store := storage.NewMemStore()
	sched := &Scheduler{
		Deps: dispatch.Dependencies{
			AsinListReader: asinListReader{store: store},
			AuthorReader:   authorReader{store: store},
			ConfigReader:   enabledConfig{},
			Enqueuer:       &failingEnqueuer{err: errors.New("sqs throttled")},
			UpcomingMerger: fakeMerger{},
			Keys:           dispatch.Keys{Unprocessed: "unprocessed_asins.json", Authors: "authors.json", PaperBooks: "paper_books_asins.json"},
		},
	}
	// sale は対象空でも sale_finalize を投入するため、enqueuer 失敗が必ず発火する。
	body := `{"version":1,"source":"scheduler","check_type":"sale","scheduled_at":"2026-08-09T00:00:00Z"}`

	if err := sched.HandleEvent(context.Background(), []byte(body)); err == nil {
		t.Fatal("HandleEvent must propagate enqueue failure")
	}
}

// Logger 未設定でも decode 失敗時の error 伝播・結果は変わらない（SPECIFICATION.md 18 境界）。
func TestHandleEvent_NilLoggerDoesNotChangeResult(t *testing.T) {
	sched := newScheduler(storage.NewMemStore(), &recordingEnqueuer{}, nil)
	sched.Logger = nil
	body := `{"version":1,"source":"scheduler","check_type":"bogus","scheduled_at":"2026-08-09T00:00:00Z"}`
	if err := sched.HandleEvent(context.Background(), []byte(body)); err == nil {
		t.Fatal("nil logger must not mask invalid schedule input error")
	}
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

// schedule-checks は Slack 通知用の2 key だけを必須とし、他の不要 key が欠けても起動を妨げない。
func TestScheduleChecksSecretKeySet(t *testing.T) {
	want := []string{config.KeySlackBotToken, config.KeySlackErrorChannel}
	if !reflect.DeepEqual(scheduleChecksSecretKeys, want) {
		t.Fatalf("scheduleChecksSecretKeys = %v, want %v", scheduleChecksSecretKeys, want)
	}
	// その2 key だけ存在し、他が全て欠けても LoadSecrets は成功する。
	g := &stubSecretGetter{values: map[string]string{
		"/myapp/secure/" + config.KeySlackBotToken:    "token",
		"/myapp/plain/" + config.KeySlackErrorChannel: "C-err",
	}}
	if _, err := config.LoadSecrets(context.Background(), g, scheduleChecksSecretKeys, nil); err != nil {
		t.Fatalf("LoadSecrets with only schedule-checks keys must succeed: %v", err)
	}
}
