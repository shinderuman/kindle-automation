package gist

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	githubTimeout  = 10 * time.Second
	defaultGistURL = "https://api.github.com/gists"
	// githubUserAgent は GitHub REST API が要求する User-Agent（SPECIFICATION.md 15）。
	// GitHub は User-Agent のない request を 403 で拒否するため必須。
	githubUserAgent = "kindle-automation-gist-updater"
	// githubAccept は Gist API の安定した JSON 表現を要求する（SPECIFICATION.md 15）。
	githubAccept = "application/vnd.github+json"
	// errorBodyLimit は Gist API の error 応答本文の読込・error 文への取り込み上限。
	// GitHub の JSON error は小さいため 4 KiB で十分であり、異常時に巨大な応答で OOM したり
	// error 文が構造化ログへ長大に貼られるのを防ぐ。
	errorBodyLimit = 4 << 10 // 4 KiB
)

// GistUpdater は Gist への更新。GitHubClient が満たし、テストでは stub へ差し替える。
type GistUpdater interface {
	Update(ctx context.Context, gistID, filename, markdown string) error
}

// GitHubClient は GitHub Gist API へ PATCH でファイル内容を更新する（SPECIFICATION.md 15）。
// HTTP timeout は10秒（SPECIFICATION.md 9/AGENTS.md 9）。
type GitHubClient struct {
	token   string
	client  *http.Client
	baseURL string
}

// NewGitHubClient は token 認証の GitHub Gist API クライアントを構築する。
func NewGitHubClient(token string) *GitHubClient {
	return &GitHubClient{
		token:   token,
		client:  &http.Client{Timeout: githubTimeout},
		baseURL: defaultGistURL,
	}
}

type gistFileContent struct {
	Content string `json:"content"`
}

type gistPayload struct {
	Files map[string]gistFileContent `json:"files"`
}

// Update は指定 Gist の filename を markdown で上書きする。
// 2xx 以外は error とする。token は Authorization header のみへ載せ、error 文へは出さない（秘密情報の非漏洩、SPECIFICATION.md 19）。
func (c *GitHubClient) Update(ctx context.Context, gistID, filename, markdown string) error {
	body, err := json.Marshal(gistPayload{
		Files: map[string]gistFileContent{filename: {Content: markdown}},
	})
	if err != nil {
		return fmt.Errorf("marshal gist payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, c.baseURL+"/"+gistID, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build gist request: %w", err)
	}
	req.Header.Set("Authorization", "token "+c.token)
	req.Header.Set("Accept", githubAccept)
	req.Header.Set("User-Agent", githubUserAgent)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("gist request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
		return nil
	}
	// token は応答へ含まれない。
	responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, errorBodyLimit))
	return fmt.Errorf("gist api http %d: %s", resp.StatusCode, strings.TrimSpace(string(responseBody)))
}
