package notification

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/shinderuman/kindle-automation/internal/logging"
)

func newSlackServer(t *testing.T, ok bool, captured *map[string]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tkn" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var payload map[string]string
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &payload)
		*captured = payload
		w.Header().Set("Content-Type", "application/json")
		if ok {
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":false,"error":"invalid_channel"}`))
	}))
}

func newMastodonServer(t *testing.T, status int, captured *url.Values) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer atoken" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = r.ParseForm()
		*captured = r.PostForm
		w.WriteHeader(status)
	}))
}

func TestNotify_BothChannelsSucceed(t *testing.T) {
	var slackBody map[string]string
	var mastodonForm url.Values
	slackSrv := newSlackServer(t, true, &slackBody)
	defer slackSrv.Close()
	mastoSrv := newMastodonServer(t, http.StatusOK, &mastodonForm)
	defer mastoSrv.Close()

	slack := NewSlackSender("tkn", "C123")
	slack.baseURL = slackSrv.URL
	masto := NewMastodonSender(mastoSrv.URL, "atoken")
	n := NewNotifier(slack, masto, nil)

	if err := n.Notify(context.Background(), "📚 新刊予定"); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if slackBody["channel"] != "C123" || slackBody["text"] != "📚 新刊予定" {
		t.Errorf("slack body = %+v", slackBody)
	}
	if mastodonForm.Get("status") != "📚 新刊予定" || mastodonForm.Get("visibility") != "public" {
		t.Errorf("mastodon form = %+v", mastodonForm)
	}
}

func TestNotify_SlackFailureDoesNotStopMastodon(t *testing.T) {
	var slackBody map[string]string
	var mastodonForm url.Values
	slackSrv := newSlackServer(t, false, &slackBody)
	defer slackSrv.Close()
	mastoSrv := newMastodonServer(t, http.StatusOK, &mastodonForm)
	defer mastoSrv.Close()

	slack := NewSlackSender("tkn", "C123")
	slack.baseURL = slackSrv.URL
	masto := NewMastodonSender(mastoSrv.URL, "atoken")
	n := NewNotifier(slack, masto, nil)

	err := n.Notify(context.Background(), "メッセージ")
	if err == nil {
		t.Fatal("want error when slack fails")
	}
	if !strings.Contains(err.Error(), "slack") {
		t.Errorf("error should mention slack: %v", err)
	}
	if mastodonForm.Get("status") != "メッセージ" {
		t.Error("mastodon must still be called when slack fails")
	}
}

func TestNotify_MastodonHTTPErrorReturnsError(t *testing.T) {
	var slackBody map[string]string
	var mastodonForm url.Values
	slackSrv := newSlackServer(t, true, &slackBody)
	defer slackSrv.Close()
	mastoSrv := newMastodonServer(t, http.StatusServiceUnavailable, &mastodonForm)
	defer mastoSrv.Close()

	slack := NewSlackSender("tkn", "C123")
	slack.baseURL = slackSrv.URL
	masto := NewMastodonSender(mastoSrv.URL, "atoken")
	n := NewNotifier(slack, masto, nil)

	err := n.Notify(context.Background(), "メッセージ")
	if err == nil || !strings.Contains(err.Error(), "mastodon") {
		t.Fatalf("want mastodon error, got %v", err)
	}
	if slackBody["text"] != "メッセージ" {
		t.Error("slack must still be called when mastodon fails")
	}
}

func TestNotify_NilSendersAreSkipped(t *testing.T) {
	n := NewNotifier(nil, nil, nil)
	if err := n.Notify(context.Background(), "x"); err != nil {
		t.Fatalf("Notify with nil senders: %v", err)
	}
}

func TestNotify_LogsSuccessAtInfo(t *testing.T) {
	var slackBody map[string]string
	var mastodonForm url.Values
	slackSrv := newSlackServer(t, true, &slackBody)
	defer slackSrv.Close()
	mastoSrv := newMastodonServer(t, http.StatusOK, &mastodonForm)
	defer mastoSrv.Close()

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	slack := NewSlackSender("tkn", "C123")
	slack.baseURL = slackSrv.URL
	masto := NewMastodonSender(mastoSrv.URL, "atoken")
	n := NewNotifier(slack, masto, logger)

	if err := n.Notify(context.Background(), "📚 新刊予定"); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, `"level":"INFO"`) {
		t.Errorf("success log must be INFO, got: %s", out)
	}
	if strings.Contains(out, `"level":"DEBUG"`) {
		t.Errorf("success log must not be DEBUG: %s", out)
	}
}

func TestNotify_FailureLogHasSingleEventKey(t *testing.T) {
	var slackBody map[string]string
	slackSrv := newSlackServer(t, false, &slackBody)
	defer slackSrv.Close()

	var buf bytes.Buffer
	logger := logging.New(&buf, slog.LevelInfo)
	slack := NewSlackSender("tkn", "C123")
	slack.baseURL = slackSrv.URL
	n := NewNotifier(slack, nil, logger)

	_ = n.Notify(context.Background(), "メッセージ")

	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("unmarshal log %q: %v", buf.String(), err)
	}
	if m["event"] != logging.EventNotificationError {
		t.Errorf("event = %v, want %q", m["event"], logging.EventNotificationError)
	}
	if c := strings.Count(buf.String(), `"event"`); c != 1 {
		t.Errorf("event key count = %d, want 1 (duplicate event key): %s", c, buf.String())
	}
}

func parseLogMap(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal log %q: %v", string(b), err)
	}
	return m
}

// newRawSlackServer は HTTP status 分類・decode 経路の検証用（newSlackServer は ok/false 固定で用途が違う）。
func newRawSlackServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

func TestNotify_BothChannelsFailReturnsJoinedErrorAndLogsBoth(t *testing.T) {
	var slackBody map[string]string
	var mastodonForm url.Values
	slackSrv := newSlackServer(t, false, &slackBody)
	defer slackSrv.Close()
	mastoSrv := newMastodonServer(t, http.StatusServiceUnavailable, &mastodonForm)
	defer mastoSrv.Close()

	var buf bytes.Buffer
	logger := logging.New(&buf, slog.LevelInfo)
	slack := NewSlackSender("tkn", "C123")
	slack.baseURL = slackSrv.URL
	masto := NewMastodonSender(mastoSrv.URL, "atoken")
	n := NewNotifier(slack, masto, logger)

	err := n.Notify(context.Background(), "メッセージ")
	if err == nil {
		t.Fatal("want error when both channels fail")
	}
	if !strings.Contains(err.Error(), "slack") || !strings.Contains(err.Error(), "mastodon") {
		t.Errorf("joined error must mention both channels: %v", err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 failure logs (one per channel), got %d: %s", len(lines), buf.String())
	}
	seenSlack, seenMastodon := false, false
	for _, line := range lines {
		m := parseLogMap(t, []byte(line))
		if m["event"] != logging.EventNotificationError {
			t.Errorf("event = %v, want %q", m["event"], logging.EventNotificationError)
		}
		if m["level"] != "ERROR" {
			t.Errorf("level = %v, want ERROR", m["level"])
		}
		switch m["channel"] {
		case "slack":
			seenSlack = true
		case "mastodon":
			seenMastodon = true
		}
	}
	if !seenSlack || !seenMastodon {
		t.Errorf("failure logs must cover both channels: %s", buf.String())
	}
}

func TestNotify_FailureLogDoesNotLeakSecrets(t *testing.T) {
	var slackBody map[string]string
	slackSrv := newSlackServer(t, false, &slackBody)
	defer slackSrv.Close()

	var buf bytes.Buffer
	logger := logging.New(&buf, slog.LevelInfo)
	slack := NewSlackSender("SECRET-TOKEN-XYZ", "C-secret-channel")
	slack.baseURL = slackSrv.URL
	n := NewNotifier(slack, nil, logger)

	_ = n.Notify(context.Background(), "msg")
	out := buf.String()
	if strings.Contains(out, "SECRET-TOKEN-XYZ") {
		t.Errorf("failure log must not leak bearer token: %s", out)
	}
	if strings.Contains(out, "C-secret-channel") {
		t.Errorf("failure log must not leak channel id: %s", out)
	}
}

func TestTimeoutsAreFiveSeconds(t *testing.T) {
	if slackTimeout != 5*time.Second {
		t.Errorf("slackTimeout = %v, want 5s", slackTimeout)
	}
	if mastodonTimeout != 5*time.Second {
		t.Errorf("mastodonTimeout = %v, want 5s", mastodonTimeout)
	}
}

func TestNewSenders_ApplyFiveSecondClientTimeout(t *testing.T) {
	if got := NewSlackSender("t", "c").client.Timeout; got != 5*time.Second {
		t.Errorf("slack client timeout = %v, want 5s", got)
	}
	if got := NewMastodonSender("https://m.example", "t").client.Timeout; got != 5*time.Second {
		t.Errorf("mastodon client timeout = %v, want 5s", got)
	}
}

func TestDefaultSlackURL_PointsToRealChatPostMessage(t *testing.T) {
	if defaultSlackURL != "https://slack.com/api/chat.postMessage" {
		t.Errorf("defaultSlackURL = %q, want Slack chat.postMessage endpoint", defaultSlackURL)
	}
}

func TestSlackSender_Non2xxStatusClassifiesHTTPError(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusServiceUnavailable} {
		t.Run(fmt.Sprintf("%d", status), func(t *testing.T) {
			srv := newRawSlackServer(t, status, "")
			defer srv.Close()
			slack := NewSlackSender("tkn", "C123")
			slack.baseURL = srv.URL
			err := slack.Send(context.Background(), "hi")
			if err == nil {
				t.Fatal("want error for non-2xx status")
			}
			want := fmt.Sprintf("slack http %d", status)
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error = %v, want it to classify %q", err, want)
			}
		})
	}
}

func TestSlackSender_EmptyOrInvalidBodyReturnsDecodeError(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"empty", ""},
		{"invalid_json", "not-json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newRawSlackServer(t, http.StatusOK, tc.body)
			defer srv.Close()
			slack := NewSlackSender("tkn", "C123")
			slack.baseURL = srv.URL
			err := slack.Send(context.Background(), "hi")
			if err == nil || !strings.Contains(err.Error(), "decode slack response") {
				t.Fatalf("want decode slack response error, got %v", err)
			}
		})
	}
}

func TestSlackSender_TimeoutReturnsRequestError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	slack := NewSlackSender("tkn", "C123")
	slack.baseURL = srv.URL
	slack.client = &http.Client{Timeout: 50 * time.Millisecond}
	err := slack.Send(context.Background(), "hi")
	if err == nil {
		t.Fatal("want timeout error")
	}
	if !strings.Contains(err.Error(), "slack request") {
		t.Errorf("error must come from slack request path: %v", err)
	}
}

func TestSlackSender_CancelledContextReturnsRequestError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(300 * time.Millisecond)
	}))
	defer srv.Close()

	slack := NewSlackSender("tkn", "C123")
	slack.baseURL = srv.URL
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := slack.Send(ctx, "hi")
	if err == nil {
		t.Fatal("want error on cancelled context")
	}
	if !strings.Contains(err.Error(), "slack request") {
		t.Errorf("error must come from slack request path: %v", err)
	}
}

func TestSlackSender_RequestShape(t *testing.T) {
	var got struct {
		method      string
		auth        string
		contentType string
		body        map[string]string
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.method = r.Method
		got.auth = r.Header.Get("Authorization")
		got.contentType = r.Header.Get("Content-Type")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got.body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	slack := NewSlackSender("secret-token", "C-notice")
	slack.baseURL = srv.URL
	if err := slack.Send(context.Background(), "📚 新刊"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got.method != http.MethodPost {
		t.Errorf("method = %q, want POST", got.method)
	}
	if got.auth != "Bearer secret-token" {
		t.Errorf("Authorization = %q, want Bearer scheme (token must not appear in body)", got.auth)
	}
	if got.contentType != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q, want application/json; charset=utf-8", got.contentType)
	}
	if got.body["channel"] != "C-notice" || got.body["text"] != "📚 新刊" {
		t.Errorf("payload = %+v, want channel+text", got.body)
	}
}

func TestMastodonSender_Non2xxStatusClassifiesHTTPError(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable} {
		t.Run(fmt.Sprintf("%d", status), func(t *testing.T) {
			var form url.Values
			srv := newMastodonServer(t, status, &form)
			defer srv.Close()
			masto := NewMastodonSender(srv.URL, "atoken")
			err := masto.Send(context.Background(), "hi")
			if err == nil {
				t.Fatal("want error for non-2xx status")
			}
			want := fmt.Sprintf("mastodon http %d", status)
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error = %v, want it to classify %q", err, want)
			}
		})
	}
}

func TestMastodonSender_TimeoutReturnsRequestError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	masto := NewMastodonSender(srv.URL, "atoken")
	masto.client = &http.Client{Timeout: 50 * time.Millisecond}
	err := masto.Send(context.Background(), "hi")
	if err == nil {
		t.Fatal("want timeout error")
	}
	if !strings.Contains(err.Error(), "mastodon request") {
		t.Errorf("error must come from mastodon request path: %v", err)
	}
}

func TestMastodonSender_CancelledContextReturnsRequestError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(300 * time.Millisecond)
	}))
	defer srv.Close()

	masto := NewMastodonSender(srv.URL, "atoken")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := masto.Send(ctx, "hi")
	if err == nil {
		t.Fatal("want error on cancelled context")
	}
	if !strings.Contains(err.Error(), "mastodon request") {
		t.Errorf("error must come from mastodon request path: %v", err)
	}
}

func TestMastodonSender_RequestShapeAndPath(t *testing.T) {
	var got struct {
		method      string
		path        string
		auth        string
		contentType string
		form        url.Values
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.method = r.Method
		got.path = r.URL.Path
		got.auth = r.Header.Get("Authorization")
		got.contentType = r.Header.Get("Content-Type")
		_ = r.ParseForm()
		got.form = r.PostForm
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	masto := NewMastodonSender(srv.URL+"/", "secret-access")
	if err := masto.Send(context.Background(), "📚 新刊"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got.method != http.MethodPost {
		t.Errorf("method = %q, want POST", got.method)
	}
	if got.path != "/api/v1/statuses" {
		t.Errorf("path = %q, want /api/v1/statuses (trailing slash on server must be trimmed)", got.path)
	}
	if got.auth != "Bearer secret-access" {
		t.Errorf("Authorization = %q, want Bearer scheme", got.auth)
	}
	if got.contentType != "application/x-www-form-urlencoded" {
		t.Errorf("Content-Type = %q, want application/x-www-form-urlencoded", got.contentType)
	}
	if got.form.Get("status") != "📚 新刊" || got.form.Get("visibility") != "public" {
		t.Errorf("form = %+v, want status+visibility=public", got.form)
	}
}
