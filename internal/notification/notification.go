// Package notification は商品通知の Slack・Mastodon adapter を提供する（SPECIFICATION.md 17.1）。
//
// Notifier.Notify は Slack と Mastodon の両方へ送信する。片方の失敗で他方を中止せず、
// 各送信結果を個別に構造化ログへ出す。失敗が1件でもあれば error を返し、呼び出し側で
// 保存済み状態を巻き戻さず ERROR ログへ記録できるようにする（SPECIFICATION.md 17.1/12.6）。
//
// 外部依存を増やさないため Slack・Mastodon の専用 client library は使わず net/http で直接 API を呼ぶ。
// 各 HTTP client の timeout は5秒（SPECIFICATION.md 17.1/AGENTS.md 9）。
package notification

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/shinderuman/kindle-automation/internal/logging"
)

const (
	slackTimeout    = 5 * time.Second
	mastodonTimeout = 5 * time.Second
	defaultSlackURL = "https://slack.com/api/chat.postMessage"
)

// Sender は1つの通知先への送信。SlackSender・MastodonSender が満たす。
type Sender interface {
	Send(ctx context.Context, message string) error
}

// Notifier は Slack・Mastodon の両方へ best-effort で通知する。各 worker ユースケースの Notifier interface を満たす。
type Notifier struct {
	slack    Sender
	mastodon Sender
	logger   *slog.Logger
}

// NewNotifier は送信先と logger を指定して Notifier を返す。nil の送信先は送信しない。
func NewNotifier(slack, mastodon Sender, logger *slog.Logger) *Notifier {
	return &Notifier{slack: slack, mastodon: mastodon, logger: logger}
}

// Notify は Slack・Mastodon の両方へ送信する。片方の失敗で他方を中止せず、各結果をログへ出す。
// 1件でも失敗があれば error を返す（SPECIFICATION.md 17.1）。
func (n *Notifier) Notify(ctx context.Context, message string) error {
	var errs []error
	for _, target := range []struct {
		name   string
		sender Sender
	}{
		{"slack", n.slack},
		{"mastodon", n.mastodon},
	} {
		if target.sender == nil {
			continue
		}
		if err := target.sender.Send(ctx, message); err != nil {
			n.logFailure(target.name, err)
			errs = append(errs, fmt.Errorf("%s: %w", target.name, err))
			continue
		}
		n.logSuccess(target.name)
	}
	return errors.Join(errs...)
}

func (n *Notifier) logFailure(channel string, err error) {
	if n.logger == nil {
		return
	}
	n.logger.LogAttrs(context.Background(), slog.LevelError, logging.EventNotificationError,
		slog.String("channel", channel),
		slog.String("error", err.Error()),
	)
}

func (n *Notifier) logSuccess(channel string) {
	if n.logger == nil {
		return
	}
	// 通知成功は INFO で記録する（SPECIFICATION.md 18.1 の level は INFO/WARN/ERROR）。
	n.logger.InfoContext(context.Background(), "notification sent",
		slog.String("channel", channel),
	)
}

// SlackSender は Slack chat.postMessage API へ投稿する。
type SlackSender struct {
	token   string
	channel string
	client  *http.Client
	baseURL string
}

// NewSlackSender は Slack notice channel への送信者を返す。
func NewSlackSender(token, channel string) *SlackSender {
	return &SlackSender{
		token:   token,
		channel: channel,
		client:  &http.Client{Timeout: slackTimeout},
		baseURL: defaultSlackURL,
	}
}

// Send は Slack API へ POST し、レスポンスの ok フィールドで成功を判定する。
func (s *SlackSender) Send(ctx context.Context, message string) error {
	body, err := json.Marshal(map[string]string{"channel": s.channel, "text": message})
	if err != nil {
		return fmt.Errorf("marshal slack payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build slack request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("slack request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("slack http %d", resp.StatusCode)
	}
	var result struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decode slack response: %w", err)
	}
	if !result.OK {
		return fmt.Errorf("slack api error: %s", result.Error)
	}
	return nil
}

// MastodonSender は Mastodon の statuses API へ投稿する。
type MastodonSender struct {
	server      string
	accessToken string
	client      *http.Client
}

// NewMastodonSender は Mastodon server への送信者を返す。server は末尾スラッシュなしの URL。
func NewMastodonSender(server, accessToken string) *MastodonSender {
	return &MastodonSender{
		server:      strings.TrimRight(server, "/"),
		accessToken: accessToken,
		client:      &http.Client{Timeout: mastodonTimeout},
	}
}

// Send は {server}/api/v1/statuses へ public toot として POST する。
func (s *MastodonSender) Send(ctx context.Context, message string) error {
	form := url.Values{}
	form.Set("status", message)
	form.Set("visibility", "public")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.server+"/api/v1/statuses", strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("build mastodon request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.accessToken)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("mastodon request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("mastodon http %d", resp.StatusCode)
	}
	return nil
}
