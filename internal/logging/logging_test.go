package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// logLine は1行目のJSONログを取り出す。
func logLine(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	out := bytes.TrimSpace(buf.Bytes())
	if len(out) == 0 {
		t.Fatalf("no log output")
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal log line: %v\nraw=%q", err, buf.String())
	}
	return got
}

func TestNew_EmitsSpecCommonFields(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf, slog.LevelInfo)

	logger.Info(EventJobCompleted, "job_id", "J1", "cycle_id", "C1", "result", "ok")

	got := logLine(t, &buf)
	if _, ok := got["time"]; ok {
		t.Errorf("should not emit time key: %v", got)
	}
	if _, ok := got["msg"]; ok {
		t.Errorf("should not emit msg key: %v", got)
	}
	if got["timestamp"] == nil {
		t.Errorf("missing timestamp: %v", got)
	}
	if got["level"] != "INFO" {
		t.Errorf("level = %v, want INFO", got["level"])
	}
	if got["event"] != EventJobCompleted {
		t.Errorf("event = %v, want %q", got["event"], EventJobCompleted)
	}
	if got["job_id"] != "J1" || got["cycle_id"] != "C1" || got["result"] != "ok" {
		t.Errorf("attrs not round-tripped: %v", got)
	}
}

func TestNew_TimestampIsUTC(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf, slog.LevelInfo)
	logger.Info("e")

	got := logLine(t, &buf)
	ts, ok := got["timestamp"].(string)
	if !ok {
		t.Fatalf("timestamp not a string: %v", got["timestamp"])
	}
	parsed, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		t.Fatalf("timestamp not RFC3339Nano: %v", err)
	}
	if parsed.Location() != time.UTC {
		t.Errorf("timestamp not UTC: %v", parsed.Location())
	}
}

func TestNew_LevelFiltering(t *testing.T) {
	cases := []struct {
		name        string
		level       slog.Level
		wantEmitted int
	}{
		{"error only", slog.LevelError, 1},
		{"warn and above", slog.LevelWarn, 2},
		{"info and above", slog.LevelInfo, 3},
		{"all", slog.LevelDebug, 4},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger := New(&buf, c.level)
			logger.Debug("d")
			logger.Info("i")
			logger.Warn("w")
			logger.Error("e")
			raw := buf.String()
			got := strings.Count(raw, "\n")
			if got != c.wantEmitted {
				t.Errorf("emitted %d lines, want %d (raw=%q)", got, c.wantEmitted, raw)
			}
		})
	}
}

func TestNew_ErrorLevelStringForMetricFilter(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf, slog.LevelInfo)

	logger.Error(EventNotificationError, "error", "boom")

	got := logLine(t, &buf)
	// SPEC 18.2: CloudWatch Logs metric filter は level=ERROR を集計する。
	if got["level"] != "ERROR" {
		t.Errorf("level = %v, want ERROR string", got["level"])
	}
	if got["event"] != EventNotificationError {
		t.Errorf("event = %v, want %q", got["event"], EventNotificationError)
	}
}

func TestNew_NoAddedFields(t *testing.T) {
	// logger 自体は値を追加しない。渡した attribute だけが出力される（秘密情報漏洩の原因を作らない）。
	var buf bytes.Buffer
	logger := New(&buf, slog.LevelInfo)
	logger.Info("e", "job_id", "J1")

	got := logLine(t, &buf)
	wantKeys := map[string]bool{"timestamp": true, "level": true, "event": true, "job_id": true}
	for k := range got {
		if !wantKeys[k] {
			t.Errorf("unexpected field %q in %v", k, got)
		}
	}
}

func TestParseLevel(t *testing.T) {
	cases := []struct {
		in   string
		want slog.Level
	}{
		{"DEBUG", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{" Warn ", slog.LevelWarn},
		{"ERROR", slog.LevelError},
	}
	for _, c := range cases {
		got, err := ParseLevel(c.in)
		if err != nil {
			t.Fatalf("ParseLevel(%q) error: %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("ParseLevel(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestParseLevel_InvalidReturnsError(t *testing.T) {
	for _, in := range []string{"VERBOSE", "", "  "} {
		if _, err := ParseLevel(in); err == nil {
			t.Errorf("ParseLevel(%q) should reject invalid level", in)
		}
	}
}
