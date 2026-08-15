package schedulechecks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/shinderuman/kindle-automation/internal/application/dispatch"
	"github.com/shinderuman/kindle-automation/internal/job"
	"github.com/shinderuman/kindle-automation/internal/logging"
)

func parseLogLine(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal log %q: %v", string(b), err)
	}
	return m
}

func TestLogCycle_DispatchedContainsCounts(t *testing.T) {
	var buf bytes.Buffer
	s := &Scheduler{Logger: logging.New(&buf, slog.LevelInfo)}
	event := dispatch.Event{CheckType: job.CheckSale, ScheduledAt: time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC)}
	result := dispatch.DispatchResult{
		CycleID:          "sale:2026-07-22T23:00:00Z",
		CycleTargetCount: 271,
		TargetCount:      5,
		EnqueuedCount:    6,
		SlotIndex:        23,
		SlotCount:        24,
	}

	s.logCycle(context.Background(), event, result)

	m := parseLogLine(t, buf.Bytes())
	if m["event"] != logging.EventCycleDispatched {
		t.Errorf("event = %v, want %q", m["event"], logging.EventCycleDispatched)
	}
	if m["level"] != "INFO" {
		t.Errorf("level = %v, want INFO", m["level"])
	}
	if m["check_type"] != "sale" {
		t.Errorf("check_type = %v, want sale", m["check_type"])
	}
	if m["cycle_id"] != result.CycleID {
		t.Errorf("cycle_id = %v, want %q", m["cycle_id"], result.CycleID)
	}
	if m["cycle_target_count"] != float64(271) {
		t.Errorf("cycle_target_count = %v, want 271", m["cycle_target_count"])
	}
	if m["target_count"] != float64(5) {
		t.Errorf("target_count = %v, want 5", m["target_count"])
	}
	if m["enqueued_count"] != float64(6) {
		t.Errorf("enqueued_count = %v, want 6", m["enqueued_count"])
	}
	if m["slot_index"] != float64(23) || m["slot_count"] != float64(24) {
		t.Errorf("slot = (%v, %v), want (23, 24)", m["slot_index"], m["slot_count"])
	}
}

func TestLogCycle_DisabledWhenCheckerOff(t *testing.T) {
	var buf bytes.Buffer
	s := &Scheduler{Logger: logging.New(&buf, slog.LevelInfo)}
	event := dispatch.Event{CheckType: job.CheckNewRelease, ScheduledAt: time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC)}
	result := dispatch.DispatchResult{Disabled: true, CycleID: "new_release:2026-07-22T21:00:00Z", SlotIndex: 36, SlotCount: 72}

	s.logCycle(context.Background(), event, result)

	m := parseLogLine(t, buf.Bytes())
	if m["event"] != logging.EventCycleDisabled {
		t.Errorf("event = %v, want %q", m["event"], logging.EventCycleDisabled)
	}
	if m["level"] != "INFO" {
		t.Errorf("level = %v, want INFO", m["level"])
	}
	if m["check_type"] != "new_release" {
		t.Errorf("check_type = %v, want new_release", m["check_type"])
	}
	if m["cycle_id"] != result.CycleID || m["slot_index"] != float64(36) || m["slot_count"] != float64(72) {
		t.Errorf("cycle metadata = %v", m)
	}
	for _, k := range []string{"target_count", "enqueued_count"} {
		if _, ok := m[k]; ok {
			t.Errorf("disabled cycle must not include %s", k)
		}
	}
}

func TestLogAlarm_HasSingleEventKey(t *testing.T) {
	var buf bytes.Buffer
	s := &Scheduler{Logger: logging.New(&buf, slog.LevelInfo)}

	if err := s.HandleAlarm(context.Background(), alarmNotificationInput{AlarmName: "WorkDLQDepth", StateValue: AlarmStateAlarm}); err != nil {
		t.Fatalf("HandleAlarm: %v", err)
	}

	m := parseLogLine(t, buf.Bytes())
	if m["event"] != logging.EventAlarmNotification {
		t.Errorf("event = %v, want %q", m["event"], logging.EventAlarmNotification)
	}
	if c := strings.Count(buf.String(), `"event"`); c != 1 {
		t.Errorf("event key count = %d, want 1 (duplicate event key): %s", c, buf.String())
	}
}

func TestLogTerminal_HasSingleEventKey(t *testing.T) {
	var buf bytes.Buffer
	s := &Scheduler{Logger: logging.New(&buf, slog.LevelInfo)}

	s.logTerminal(context.Background(), "unknown_event_source", "aws.somethingelse", errors.New("boom"))

	m := parseLogLine(t, buf.Bytes())
	if m["event"] != "unknown_event_source" {
		t.Errorf("event = %v, want unknown_event_source", m["event"])
	}
	if c := strings.Count(buf.String(), `"event"`); c != 1 {
		t.Errorf("event key count = %d, want 1 (重複 event key): %s", c, buf.String())
	}
	if m["level"] != "ERROR" {
		t.Errorf("level = %v, want ERROR", m["level"])
	}
	if m["result"] != "terminal" {
		t.Errorf("result = %v, want terminal", m["result"])
	}
	if m["error"] != "boom" {
		t.Errorf("error = %v, want boom", m["error"])
	}
	if m["source"] != "aws.somethingelse" {
		t.Errorf("source = %v, want aws.somethingelse", m["source"])
	}
}

func TestLogTerminal_NoSourceWhenEmpty(t *testing.T) {
	var buf bytes.Buffer
	s := &Scheduler{Logger: logging.New(&buf, slog.LevelInfo)}

	s.logTerminal(context.Background(), "event_decode_failed", "", errors.New("bad json"))

	m := parseLogLine(t, buf.Bytes())
	if _, ok := m["source"]; ok {
		t.Errorf("source must be omitted when eventSource empty: %v", m)
	}
	if c := strings.Count(buf.String(), `"event"`); c != 1 {
		t.Errorf("event key count = %d, want 1: %s", c, buf.String())
	}
}

func TestHandleEvent_NonAlarmLogHasSingleEventKey(t *testing.T) {
	var buf bytes.Buffer
	sched := &Scheduler{
		Logger:      logging.New(&buf, slog.LevelInfo),
		ErrorSender: &fakeSender{},
	}
	body := `{"source":"aws.cloudwatch","alarmData":{"alarmName":"work-dlq","state":{"value":"INSUFFICIENT_DATA"}}}`

	if err := sched.HandleEvent(context.Background(), []byte(body)); err != nil {
		t.Fatalf("non-ALARM state must not error: %v", err)
	}
	if c := strings.Count(buf.String(), `"event"`); c != 1 {
		t.Fatalf("event key count = %d, want 1 (重複 event key): %s", c, buf.String())
	}
	m := parseLogLine(t, buf.Bytes())
	if m["event"] != "alarm_state_ignored" {
		t.Errorf("event = %v, want alarm_state_ignored", m["event"])
	}
	if m["state"] != "INSUFFICIENT_DATA" {
		t.Errorf("state = %v, want INSUFFICIENT_DATA", m["state"])
	}
}

func TestLogCycle_NilLoggerIsNoOp(t *testing.T) {
	s := &Scheduler{Logger: nil}
	event := dispatch.Event{CheckType: job.CheckSale, ScheduledAt: time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC)}
	s.logCycle(context.Background(), event, dispatch.DispatchResult{TargetCount: 1, EnqueuedCount: 1})
}
