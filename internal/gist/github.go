package gist

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

const (
	githubTimeout  = 10 * time.Second
	defaultGistURL = "https://api.github.com/gists"
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

// NewGitHubClient は token を指定して GitHubClient を返す。
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

// Update は gistID の filename を markdown へ更新する。2xx 以外は error とする。
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
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("gist request: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("close gist response body: %v", err)
		}
	}()
	if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
		return nil
	}
	responseBody, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("gist api http %d: %s", resp.StatusCode, strings.TrimSpace(string(responseBody)))
}
