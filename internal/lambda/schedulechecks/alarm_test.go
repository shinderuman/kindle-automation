package schedulechecks

import (
	"context"
	"strings"
	"testing"

	"github.com/shinderuman/kindle-automation/internal/storage"
)

func alarmEventFor(alarmName, state, reason, timestamp string) []byte {
	return []byte(`{"source":"aws.cloudwatch","alarmArn":"arn:aws:cloudwatch:us-east-1:111122223333:alarm:` + alarmName + `","accountId":"111122223333","region":"us-east-1","alarmData":{"alarmName":"` + alarmName + `","state":{"value":"` + state + `","reason":"` + reason + `","timestamp":"` + timestamp + `"}}}`)
}

func alarmEventMissingField(alarmName, state string) []byte {
	return []byte(`{"source":"aws.cloudwatch","alarmData":{"alarmName":"` + alarmName + `","state":{"value":"` + state + `"}}}`)
}

func assertContains(t *testing.T, msg, want, label string) {
	t.Helper()
	if !strings.Contains(msg, want) {
		t.Errorf("%s must contain %q, got:\n%s", label, want, msg)
	}
}

func assertNotContains(t *testing.T, msg, want, label string) {
	t.Helper()
	if strings.Contains(msg, want) {
		t.Errorf("%s must not contain %q, got:\n%s", label, want, msg)
	}
}

func handleAlarmFor(t *testing.T, body []byte) (string, error) {
	t.Helper()
	sender := &fakeSender{}
	sched := newScheduler(storage.NewMemStore(), &recordingEnqueuer{}, sender)
	err := sched.HandleEvent(context.Background(), body)
	return sender.msg, err
}

func TestAlarmMessage_WorkDLQAlarm(t *testing.T) {
	msg, err := handleAlarmFor(t, alarmEventFor("kindle-automation-work-dlq", "ALARM", "DLQ depth 1", "2026-08-04T12:36:15.490+0000"))
	if err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}
	assertContains(t, msg, "kindle-automation-work-dlq", "message")
	assertContains(t, msg, "状態: ALARM", "message")
	assertContains(t, msg, "Work Queue DLQ にメッセージが溜まりました", "message")
	assertContains(t, msg, "対応必要", "message")
	assertContains(t, msg, "job_error", "message")
	assertContains(t, msg, "redrive", "message")
	assertContains(t, msg, "state reason: DLQ depth 1", "message")
	assertContains(t, msg, "状態更新時刻: 2026-08-04T12:36:15.490+0000", "message")
}

func TestAlarmMessage_SchedulerDLQAlarm(t *testing.T) {
	msg, err := handleAlarmFor(t, alarmEventFor("kindle-automation-scheduler-dlq", "ALARM", "DLQ depth 2", "2026-08-04T12:36:15.490+0000"))
	if err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}
	assertContains(t, msg, "状態: ALARM", "message")
	assertContains(t, msg, "schedule-checks ログ", "message")
	assertContains(t, msg, "対象周期を再実行", "message")
	assertContains(t, msg, "state reason: DLQ depth 2", "message")
}

func TestAlarmMessage_QueueStallAlarm(t *testing.T) {
	msg, err := handleAlarmFor(t, alarmEventFor("kindle-automation-work-queue-stall", "ALARM", "oldest age 7300s", "2026-08-04T12:36:15.490+0000"))
	if err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}
	assertContains(t, msg, "event source mapping", "message")
	assertContains(t, msg, "job_error", "message")
	assertContains(t, msg, "Scheduler を停止", "message")
	assertContains(t, msg, "purge は通常手順にしない", "message")
}

func TestAlarmMessage_DLQAlarmsProhibitPurgeBeforeCauseCheck(t *testing.T) {
	for _, name := range []string{"kindle-automation-work-dlq", "kindle-automation-scheduler-dlq"} {
		msg, err := handleAlarmFor(t, alarmEventFor(name, "ALARM", "r", "2026-08-04T12:36:15.490+0000"))
		if err != nil {
			t.Fatalf("%s HandleEvent: %v", name, err)
		}
		if !strings.Contains(msg, "原因確認前の DLQ purge は禁止") {
			t.Errorf("%s steps must explicitly prohibit purge before cause confirmation, got:\n%s", name, msg)
		}
	}
}

func TestAlarmMessage_OKState(t *testing.T) {
	for _, name := range []string{"kindle-automation-work-dlq", "kindle-automation-scheduler-dlq", "kindle-automation-work-queue-stall"} {
		msg, err := handleAlarmFor(t, alarmEventFor(name, "OK", "ok reason", "2026-08-04T13:00:00.000+0000"))
		if err != nil {
			t.Fatalf("%s OK HandleEvent: %v", name, err)
		}
		assertContains(t, msg, name, "OK message")
		assertContains(t, msg, "復旧済み", "OK message")
		assertContains(t, msg, "追加対応は不要", "OK message")
		assertContains(t, msg, "原因調査", "OK message")
		assertContains(t, msg, "state reason: ok reason", "OK message")
		assertContains(t, msg, "状態更新時刻: 2026-08-04T13:00:00.000+0000", "OK message")
	}
}

func TestAlarmMessage_UnknownAlarmName(t *testing.T) {
	msg, err := handleAlarmFor(t, alarmEventFor("kindle-automation-something-new", "ALARM", "unknown", "2026-08-04T12:36:15.490+0000"))
	if err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}
	assertContains(t, msg, "対応判断不能", "message")
	assertContains(t, msg, "Alarm の詳細を確認", "message")
	for _, knownStep := range []string{"redrive", "job_error", "Scheduler を停止"} {
		assertNotContains(t, msg, knownStep, "unknown alarm message")
	}
}

func TestAlarmMessage_MissingReasonAndTimestampFallsBackToUnknownText(t *testing.T) {
	msg, err := handleAlarmFor(t, alarmEventMissingField("kindle-automation-work-dlq", "ALARM"))
	if err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}
	assertContains(t, msg, "state reason: 不明", "message")
	assertContains(t, msg, "状態更新時刻: 不明", "message")
}

func TestAlarmMessage_MissingRequiredFieldErrors(t *testing.T) {
	cases := map[string]string{
		"missing alarmName": `{"source":"aws.cloudwatch","alarmData":{"state":{"value":"ALARM"}}}`,
		"missing state":     `{"source":"aws.cloudwatch","alarmData":{"alarmName":"kindle-automation-work-dlq"}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			sender := &fakeSender{}
			sched := newScheduler(storage.NewMemStore(), &recordingEnqueuer{}, sender)
			if err := sched.HandleEvent(context.Background(), []byte(body)); err == nil {
				t.Fatal("HandleEvent must fail on missing required alarm field")
			}
			if sender.called {
				t.Error("sender must not be called on invalid alarm input")
			}
		})
	}
}
