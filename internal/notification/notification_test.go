package notification

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

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
	// SPECIFICATION.md 18.1: level は INFO/WARN/ERROR。通知成功は INFO で記録する。
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

// TestNotify_FailureLogHasSingleEventKey は notification_error ログが本番の logging 設定
// （replaceAttr が message を event へ map）でも event key を1つだけ持つことを検証する
// （SPECIFICATION.md 18.3）。message 引数と追加 attr で event を重複させない。
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
