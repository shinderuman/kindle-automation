package checkworker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/events"

	"github.com/shinderuman/kindle-automation/internal/application/execution"
	"github.com/shinderuman/kindle-automation/internal/job"
	"github.com/shinderuman/kindle-automation/internal/logging"
)

func parseLog(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal log %q: %v", string(b), err)
	}
	return m
}

func TestLogJobResult_EmitsFixedFieldsForEachResult(t *testing.T) {
	cases := []struct {
		name        string
		oc          execution.Outcome
		cause       error
		kind        job.Kind
		wantEvent   string
		wantLevel   string
		wantResult  string
		wantErrType string
		wantStatus  any // http_status は文字列。未送信時は空文字列。
		wantError   string
	}{
		{
			name: "completed", oc: execution.Completed(200, 1234), kind: job.KindSaleCheck,
			wantEvent: logging.EventJobCompleted, wantLevel: "INFO",
			wantResult: execution.ResultCompleted, wantStatus: "200",
		},
		{
			name: "terminal", oc: execution.Terminal("not_found", 404, 500), kind: job.KindSaleCheck,
			wantEvent: logging.EventJobTerminal, wantLevel: "WARN",
			wantResult: execution.ResultTerminal, wantErrType: "not_found", wantStatus: "404",
		},
		{
			name: "error", oc: execution.Errored("fetch_retryable", 503, 800), cause: errors.New("boom"), kind: job.KindSaleCheck,
			wantEvent: logging.EventJobError, wantLevel: "ERROR",
			wantResult: execution.ResultError, wantErrType: "fetch_retryable", wantStatus: "503", wantError: "boom",
		},
		{
			name: "gist_error", oc: execution.Errored("gist_update", 0, 0), cause: errors.New("github 5xx"), kind: job.KindGistUpdate,
			wantEvent: logging.EventGistError, wantLevel: "ERROR",
			wantResult: execution.ResultError, wantErrType: "gist_update", wantStatus: "", wantError: "github 5xx",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			w := &Worker{Logger: logging.New(&buf, slog.LevelInfo)}
			record := events.SQSMessage{Attributes: map[string]string{"ApproximateReceiveCount": "3"}}
			j := job.Job{
				JobID: "j1", CycleID: "c1", CheckType: job.CheckSale,
				Kind: tc.kind, Target: job.Target{ASIN: "B0ASIN"},
			}
			w.logJobResult(context.Background(), record, j, tc.oc, tc.cause, 5*time.Millisecond)

			m := parseLog(t, buf.Bytes())
			if m["event"] != tc.wantEvent {
				t.Errorf("event = %v, want %q", m["event"], tc.wantEvent)
			}
			if m["level"] != tc.wantLevel {
				t.Errorf("level = %v, want %q", m["level"], tc.wantLevel)
			}
			if m["result"] != tc.wantResult {
				t.Errorf("result = %v, want %q", m["result"], tc.wantResult)
			}
			if got := m["error_type"]; got != tc.wantErrType {
				t.Errorf("error_type = %v, want %q", got, tc.wantErrType)
			}
			if m["job_id"] != "j1" {
				t.Errorf("job_id = %v, want j1", m["job_id"])
			}
			if m["cycle_id"] != "c1" {
				t.Errorf("cycle_id = %v, want c1", m["cycle_id"])
			}
			if m["check_type"] != "sale" {
				t.Errorf("check_type = %v, want sale", m["check_type"])
			}
			if m["target"] != "B0ASIN" {
				t.Errorf("target = %v, want B0ASIN", m["target"])
			}
			if m["receive_count"] != float64(3) {
				t.Errorf("receive_count = %v, want 3", m["receive_count"])
			}
			if m["http_status"] != tc.wantStatus {
				t.Errorf("http_status = %v, want %v", m["http_status"], tc.wantStatus)
			}
			if m["response_bytes"] != float64(tc.oc.ResponseBytes) {
				t.Errorf("response_bytes = %v, want %d", m["response_bytes"], tc.oc.ResponseBytes)
			}
			if dm, ok := m["duration_ms"].(float64); !ok || dm < 0 {
				t.Errorf("duration_ms = %v, want >=0", m["duration_ms"])
			}
			if m["aws_request_id"] != "" {
				t.Errorf("aws_request_id = %v, want empty", m["aws_request_id"])
			}
			if m["error"] != tc.wantError {
				t.Errorf("error = %v, want %q", m["error"], tc.wantError)
			}
			if _, ok := m["timestamp"].(string); !ok {
				t.Errorf("timestamp missing or not string: %v", m["timestamp"])
			}
		})
	}
}

func TestLogJobResult_HTTPStatusEmptyWhenZero(t *testing.T) {
	var buf bytes.Buffer
	w := &Worker{Logger: logging.New(&buf, slog.LevelInfo)}
	j := job.Job{Kind: job.KindSaleFinalize, CheckType: job.CheckSale}
	w.logJobResult(context.Background(), events.SQSMessage{}, j, execution.Completed(0, 0), nil, time.Millisecond)

	m := parseLog(t, buf.Bytes())
	if m["http_status"] != "" {
		t.Errorf("http_status = %v, want empty when status=0 (SPEC 18.1)", m["http_status"])
	}
	if m["result"] != execution.ResultCompleted {
		t.Errorf("result = %v, want completed", m["result"])
	}
}

func TestLogDecodeFailure_EmitsJobErrorWithDecodeType(t *testing.T) {
	var buf bytes.Buffer
	w := &Worker{Logger: logging.New(&buf, slog.LevelInfo)}
	record := events.SQSMessage{MessageId: "m1", Attributes: map[string]string{"ApproximateReceiveCount": "1"}}
	w.logDecodeFailure(context.Background(), record, errors.New("invalid json"))

	m := parseLog(t, buf.Bytes())
	if m["event"] != logging.EventJobError {
		t.Errorf("event = %v, want %q", m["event"], logging.EventJobError)
	}
	if m["level"] != "ERROR" {
		t.Errorf("level = %v, want ERROR", m["level"])
	}
	if m["result"] != execution.ResultError {
		t.Errorf("result = %v, want error", m["result"])
	}
	if m["error_type"] != "decode" {
		t.Errorf("error_type = %v, want decode", m["error_type"])
	}
	if m["sqs_message_id"] != "m1" {
		t.Errorf("sqs_message_id = %v, want m1", m["sqs_message_id"])
	}
	if m["receive_count"] != float64(1) {
		t.Errorf("receive_count = %v, want 1", m["receive_count"])
	}
	if m["error"] != "invalid json" {
		t.Errorf("error = %v, want invalid json", m["error"])
	}
}

func TestLogConfigLoadFailure_EmitsSingleJobError(t *testing.T) {
	var buf bytes.Buffer
	w := &Worker{Logger: logging.New(&buf, slog.LevelInfo)}
	body := mustEncode(t, job.Job{
		Version: job.Version, JobID: "job-1", Kind: job.KindSaleCheck,
		CheckType: job.CheckSale, CycleID: "cycle-1", Target: job.Target{ASIN: "B0FX3X569X"},
	})
	event := events.SQSEvent{Records: []events.SQSMessage{{
		MessageId: "m1", Body: body,
		Attributes: map[string]string{"ApproximateReceiveCount": "2"},
	}}}

	w.logConfigLoadFailure(context.Background(), event, errors.New("get checker_configs.json: s3 timeout"))

	if n := bytes.Count(buf.Bytes(), []byte("\n")); n != 1 {
		t.Fatalf("log lines = %d, want 1", n)
	}
	m := parseLog(t, buf.Bytes())
	if m["event"] != logging.EventJobError {
		t.Errorf("event = %v, want %q", m["event"], logging.EventJobError)
	}
	if m["level"] != "ERROR" {
		t.Errorf("level = %v, want ERROR", m["level"])
	}
	if m["result"] != execution.ResultError {
		t.Errorf("result = %v, want error", m["result"])
	}
	if m["error_type"] != "config_load" {
		t.Errorf("error_type = %v, want config_load", m["error_type"])
	}
	if m["job_id"] != "job-1" {
		t.Errorf("job_id = %v, want job-1", m["job_id"])
	}
	if m["cycle_id"] != "cycle-1" {
		t.Errorf("cycle_id = %v, want cycle-1", m["cycle_id"])
	}
	if m["check_type"] != "sale" {
		t.Errorf("check_type = %v, want sale", m["check_type"])
	}
	if m["target"] != "B0FX3X569X" {
		t.Errorf("target = %v, want B0FX3X569X", m["target"])
	}
	if m["receive_count"] != float64(2) {
		t.Errorf("receive_count = %v, want 2", m["receive_count"])
	}
	if m["http_status"] != "" {
		t.Errorf("http_status = %v, want empty", m["http_status"])
	}
	if m["duration_ms"] != float64(0) {
		t.Errorf("duration_ms = %v, want 0", m["duration_ms"])
	}
	if m["response_bytes"] != float64(0) {
		t.Errorf("response_bytes = %v, want 0", m["response_bytes"])
	}
	if m["error"] != "get checker_configs.json: s3 timeout" {
		t.Errorf("error = %v, want s3 timeout message", m["error"])
	}
}

func TestLogConfigLoadFailure_UndecodableMessageStillLogs(t *testing.T) {
	var buf bytes.Buffer
	w := &Worker{Logger: logging.New(&buf, slog.LevelInfo)}
	event := events.SQSEvent{Records: []events.SQSMessage{{
		MessageId: "m1", Body: "not-json",
		Attributes: map[string]string{"ApproximateReceiveCount": "4"},
	}}}

	w.logConfigLoadFailure(context.Background(), event, errors.New("load variable config"))

	if n := bytes.Count(buf.Bytes(), []byte("\n")); n != 1 {
		t.Fatalf("log lines = %d, want 1 (ログ自体を失わない)", n)
	}
	m := parseLog(t, buf.Bytes())
	if m["event"] != logging.EventJobError {
		t.Errorf("event = %v, want %q", m["event"], logging.EventJobError)
	}
	if m["level"] != "ERROR" {
		t.Errorf("level = %v, want ERROR", m["level"])
	}
	if m["job_id"] != "" || m["cycle_id"] != "" || m["check_type"] != "" || m["target"] != "" {
		t.Errorf("decoded identifiers must be empty for undecodable body: %+v", m)
	}
	if m["receive_count"] != float64(4) {
		t.Errorf("receive_count = %v, want 4", m["receive_count"])
	}
	if m["error_type"] != "config_load" {
		t.Errorf("error_type = %v, want config_load", m["error_type"])
	}
}

func TestLogConfigLoadFailure_OmitsRawBodyAndSecrets(t *testing.T) {
	var buf bytes.Buffer
	w := &Worker{Logger: logging.New(&buf, slog.LevelInfo)}
	secretBody := `{"version":1,"job_id":"leak","kind":"sale_check","check_type":"sale","cycle_id":"leak-cycle","target":{"asin":"SECRET-TOKEN-XYZ"}}`
	event := events.SQSEvent{Records: []events.SQSMessage{{
		MessageId: "m1", Body: secretBody,
	}}}

	w.logConfigLoadFailure(context.Background(), event, errors.New("get checker_configs.json: connection reset"))

	out := buf.String()
	for _, secret := range []string{"SECRET-TOKEN-XYZ", "leak-cycle"} {
		if strings.Contains(out, secret) {
			t.Errorf("log must not contain raw body value %q: %s", secret, out)
		}
	}
}

func TestLogConfigLoadFailure_NilLoggerIsNoOp(t *testing.T) {
	w := &Worker{Logger: nil}
	event := events.SQSEvent{Records: []events.SQSMessage{{MessageId: "m1", Body: "not-json"}}}
	w.logConfigLoadFailure(context.Background(), event, errors.New("load variable config"))
}

func TestBestEffortLogFields(t *testing.T) {
	jobID, cycleID, checkType, target, recv := bestEffortLogFields(events.SQSEvent{})
	if jobID != "" || cycleID != "" || checkType != "" || target != "" || recv != 0 {
		t.Errorf("empty event = (%q,%q,%q,%q,%d), want empties/0", jobID, cycleID, checkType, target, recv)
	}

	body := mustEncode(t, job.Job{
		Version: job.Version, JobID: "j9", Kind: job.KindGistUpdate,
		CheckType: job.CheckSale, CycleID: "c9", Target: job.Target{GistType: "sale"},
	})
	_, _, _, tgt, rc := bestEffortLogFields(events.SQSEvent{Records: []events.SQSMessage{{
		Body: body, Attributes: map[string]string{"ApproximateReceiveCount": "5"},
	}}})
	if tgt != "sale" {
		t.Errorf("target = %q, want sale", tgt)
	}
	if rc != 5 {
		t.Errorf("receive_count = %d, want 5", rc)
	}

	_, _, _, _, rc2 := bestEffortLogFields(events.SQSEvent{Records: []events.SQSMessage{{
		Body: "garbage", Attributes: map[string]string{"ApproximateReceiveCount": "6"},
	}}})
	if rc2 != 6 {
		t.Errorf("receive_count on decode failure = %d, want 6", rc2)
	}
}

func TestReceiveCount_ParsesAttribute(t *testing.T) {
	if got := receiveCount(events.SQSMessage{Attributes: map[string]string{"ApproximateReceiveCount": "7"}}); got != 7 {
		t.Errorf("receiveCount = %d, want 7", got)
	}
	if got := receiveCount(events.SQSMessage{}); got != 0 {
		t.Errorf("receiveCount missing attr = %d, want 0", got)
	}
	if got := receiveCount(events.SQSMessage{Attributes: map[string]string{"ApproximateReceiveCount": "oops"}}); got != 0 {
		t.Errorf("receiveCount invalid = %d, want 0", got)
	}
}

func TestHTTPStatusString(t *testing.T) {
	if got := httpStatusString(0); got != "" {
		t.Errorf("httpStatusString(0) = %q, want empty", got)
	}
	if got := httpStatusString(404); got != "404" {
		t.Errorf("httpStatusString(404) = %q, want 404", got)
	}
}

func TestLogJobResult_NilLoggerIsNoOp(t *testing.T) {
	w := &Worker{Logger: nil}
	j := job.Job{Kind: job.KindSaleCheck, CheckType: job.CheckSale}
	w.logJobResult(context.Background(), events.SQSMessage{}, j, execution.Completed(200, 10), nil, time.Millisecond)
}

func TestLogDecodeFailure_NilLoggerIsNoOp(t *testing.T) {
	w := &Worker{Logger: nil}
	w.logDecodeFailure(context.Background(), events.SQSMessage{MessageId: "m1"}, errors.New("bad"))
}

func TestTargetOf_SelectsFirstNonEmpty(t *testing.T) {
	if got := targetOf(job.Job{Target: job.Target{ASIN: "A"}}); got != "A" {
		t.Errorf("asin target = %q", got)
	}
	if got := targetOf(job.Job{Target: job.Target{AuthorName: "海李"}}); got != "海李" {
		t.Errorf("author target = %q", got)
	}
	if got := targetOf(job.Job{Target: job.Target{GistType: "sale"}}); got != "sale" {
		t.Errorf("gist target = %q", got)
	}
	if got := targetOf(job.Job{}); got != "" {
		t.Errorf("empty target = %q", got)
	}
}
