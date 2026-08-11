package gist

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shinderuman/kindle-automation/internal/application/execution"
	"github.com/shinderuman/kindle-automation/internal/domain/book"
)

type fakeBooks struct {
	books []book.KindleBook
	err   error
}

func (f *fakeBooks) Books(context.Context) ([]book.KindleBook, error) {
	return f.books, f.err
}

type fakeAuthors struct {
	authors []book.Author
	err     error
}

func (f *fakeAuthors) Authors(context.Context) ([]book.Author, error) {
	return f.authors, f.err
}

type fakeUpdater struct {
	lastID       string
	lastFilename string
	lastMarkdown string
	err          error
}

func (f *fakeUpdater) Update(_ context.Context, gistID, filename, markdown string) error {
	f.lastID = gistID
	f.lastFilename = filename
	f.lastMarkdown = markdown
	return f.err
}

func baseSettings() Settings {
	return Settings{
		Sale:          Target{ID: "sale-id", Filename: "sale.md"},
		NewRelease:    Target{ID: "author-id", Filename: "author.md"},
		PaperToKindle: Target{ID: "paper-id", Filename: "paper.md"},
	}
}

func TestUpdate_Sale_RegeneratesFromUnprocessed(t *testing.T) {
	updater := &fakeUpdater{}
	deps := Dependencies{
		SaleBooks: &fakeBooks{books: []book.KindleBook{{Title: "T", CurrentPrice: book.NewPrice(800), URL: "u", ReleaseDate: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)}}},
		Updater:   updater,
		Settings:  baseSettings(),
	}
	oc, err := Update(context.Background(), deps, TypeSale)
	if err != nil {
		t.Fatalf("Update sale: %v", err)
	}
	if oc.Result != execution.ResultCompleted {
		t.Errorf("outcome result = %q, want completed", oc.Result)
	}
	if updater.lastID != "sale-id" || updater.lastFilename != "sale.md" {
		t.Errorf("target = (%s,%s), want (sale-id,sale.md)", updater.lastID, updater.lastFilename)
	}
	want := BooksMarkdown(deps.SaleBooks.(*fakeBooks).books)
	if updater.lastMarkdown != want {
		t.Errorf("markdown = %q, want %q", updater.lastMarkdown, want)
	}
}

func TestUpdate_NewRelease_RegeneratesFromAuthors(t *testing.T) {
	updater := &fakeUpdater{}
	deps := Dependencies{
		Authors:  &fakeAuthors{authors: []book.Author{{Name: "作者"}}},
		Updater:  updater,
		Settings: baseSettings(),
	}
	oc, err := Update(context.Background(), deps, TypeNewRelease)
	if err != nil {
		t.Fatalf("Update new_release: %v", err)
	}
	if oc.Result != execution.ResultCompleted {
		t.Errorf("outcome result = %q, want completed", oc.Result)
	}
	if updater.lastID != "author-id" || updater.lastFilename != "author.md" {
		t.Errorf("target = (%s,%s), want (author-id,author.md)", updater.lastID, updater.lastFilename)
	}
}

func TestUpdate_PaperToKindle_RegeneratesFromPaperBooks(t *testing.T) {
	updater := &fakeUpdater{}
	deps := Dependencies{
		PaperBooks: &fakeBooks{books: []book.KindleBook{{Title: "紙"}}},
		Updater:    updater,
		Settings:   baseSettings(),
	}
	oc, err := Update(context.Background(), deps, TypePaperToKindle)
	if err != nil {
		t.Fatalf("Update paper_to_kindle: %v", err)
	}
	if oc.Result != execution.ResultCompleted {
		t.Errorf("outcome result = %q, want completed", oc.Result)
	}
	if updater.lastID != "paper-id" {
		t.Errorf("target id = %s, want paper-id", updater.lastID)
	}
}

func TestUpdate_ReaderErrorPropagates(t *testing.T) {
	deps := Dependencies{
		SaleBooks: &fakeBooks{err: errors.New("s3 down")},
		Updater:   &fakeUpdater{},
		Settings:  baseSettings(),
	}
	oc, err := Update(context.Background(), deps, TypeSale)
	if err == nil {
		t.Fatal("want error when reader fails")
	}
	if oc.Result != execution.ResultError || oc.ErrorType != errorTypeGistLoad {
		t.Errorf("outcome = %+v, want result=error error_type=%s", oc, errorTypeGistLoad)
	}
}

// TestUpdate_ReaderErrorDoesNotCallUpdater は S3 object 欠落等で reader が error を返した場合、
// 3つの gist_type いずれでも Updater を呼ばない（Gist の空上書き・正本の0件再生成をしない）ことを検証する。
// 必須 object 不存在を空配列へ fallback しない契約（SPECIFICATION.md 9.1）の末端保証。
func TestUpdate_ReaderErrorDoesNotCallUpdater(t *testing.T) {
	cases := []struct {
		name     string
		gistType string
		deps     Dependencies
	}{
		{
			name:     "sale: unprocessed 欠落で Gist 空上書きしない",
			gistType: TypeSale,
			deps: Dependencies{
				SaleBooks: &fakeBooks{err: errors.New("get unprocessed_asins.json: object not found")},
			},
		},
		{
			name:     "new_release: authors 欠落で作者0件の正本を再生しない",
			gistType: TypeNewRelease,
			deps: Dependencies{
				Authors: &fakeAuthors{err: errors.New("get authors.json: object not found")},
			},
		},
		{
			name:     "paper_to_kindle: paper_books 欠落で Gist 空上書きしない",
			gistType: TypePaperToKindle,
			deps: Dependencies{
				PaperBooks: &fakeBooks{err: errors.New("get paper_books_asins.json: object not found")},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			updater := &fakeUpdater{}
			tc.deps.Updater = updater
			tc.deps.Settings = baseSettings()

			oc, err := Update(context.Background(), tc.deps, tc.gistType)
			if err == nil {
				t.Fatal("want error when reader fails (object missing)")
			}
			if oc.Result != execution.ResultError || oc.ErrorType != errorTypeGistLoad {
				t.Errorf("outcome = %+v, want result=error error_type=%s", oc, errorTypeGistLoad)
			}
			if updater.lastID != "" || updater.lastFilename != "" || updater.lastMarkdown != "" {
				t.Errorf("Updater must not be called on reader error: id=%q md=%q", updater.lastID, updater.lastMarkdown)
			}
		})
	}
}

func TestUpdate_UpdaterErrorClassifiedAsGistUpdate(t *testing.T) {
	deps := Dependencies{
		SaleBooks: &fakeBooks{books: []book.KindleBook{{Title: "T"}}},
		Updater:   &fakeUpdater{err: errors.New("github 5xx")},
		Settings:  baseSettings(),
	}
	oc, err := Update(context.Background(), deps, TypeSale)
	if err == nil {
		t.Fatal("want error when updater fails")
	}
	if oc.Result != execution.ResultError || oc.ErrorType != errorTypeGistUpdate {
		t.Errorf("outcome = %+v, want result=error error_type=%s", oc, errorTypeGistUpdate)
	}
}

func TestUpdate_UnknownGistType(t *testing.T) {
	oc, err := Update(context.Background(), Dependencies{}, "bogus")
	if err == nil {
		t.Fatal("want error for unknown gist_type")
	}
	if oc.Result != execution.ResultError || oc.ErrorType != errorTypeGistUnknownType {
		t.Errorf("outcome = %+v, want result=error error_type=%s", oc, errorTypeGistUnknownType)
	}
}

// TestGitHubClient_Update_SuccessContract は成功時の外部契約を検証する（SPECIFICATION.md 15）。
// PATCH /gists/{gistID} の path・method・必須header（Authorization/Accept/User-Agent/Content-Type）・
// request JSON（files.{filename}.content）・2xx で nil を返すこと。
func TestGitHubClient_Update_SuccessContract(t *testing.T) {
	var gotMethod, gotPath, gotAuth, gotAccept, gotUA, gotCT string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		gotUA = r.Header.Get("User-Agent")
		gotCT = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"gist123"}`)
	}))
	defer srv.Close()

	c := NewGitHubClient("ghtoken")
	// srv.URL は "http://127.0.0.1:port" なので本番の /gists 接尾辞を補って path 構成を再現する。
	c.baseURL = srv.URL + "/gists"
	if err := c.Update(context.Background(), "gist123", "sale.md", "## body"); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("method = %s, want PATCH", gotMethod)
	}
	if gotPath != "/gists/gist123" {
		t.Errorf("path = %q, want /gists/gist123", gotPath)
	}
	if gotAuth != "token ghtoken" {
		t.Errorf("Authorization = %q, want token ghtoken", gotAuth)
	}
	if gotAccept != githubAccept {
		t.Errorf("Accept = %q, want %q", gotAccept, githubAccept)
	}
	if gotUA != githubUserAgent {
		t.Errorf("User-Agent = %q, want %q", gotUA, githubUserAgent)
	}
	if gotCT != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotCT)
	}
	// request JSON は files.{filename}.content を持つ（SPECIFICATION.md 15, GitHub Gist API）。
	var payload gistPayload
	if err := json.Unmarshal(gotBody, &payload); err != nil {
		t.Fatalf("decode request body: %v: %s", err, gotBody)
	}
	if len(payload.Files) != 1 {
		t.Fatalf("files len = %d, want 1", len(payload.Files))
	}
	f, ok := payload.Files["sale.md"]
	if !ok {
		t.Fatalf("files missing sale.md: %+v", payload.Files)
	}
	if f.Content != "## body" {
		t.Errorf("content = %q, want ## body", f.Content)
	}
}

// TestGitHubClient_Update_HTTPErrorStatuses は 2xx 以外の status を全て error とする（SPECIFICATION.md 15）。
// 401/403/404/409/429/5xx は retryable 扱いの error へ分類される呼出側への原因として返す。
func TestGitHubClient_Update_HTTPErrorStatuses(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"unauthorized", http.StatusUnauthorized, `{"message":"Bad credentials"}`},
		{"forbidden", http.StatusForbidden, `{"message":"Missing User-Agent"}`},
		{"not_found", http.StatusNotFound, `{"message":"Not Found"}`},
		{"conflict", http.StatusConflict, `{"message":"Version mismatch"}`},
		{"rate_limited", http.StatusTooManyRequests, `{"message":"Rate limit"}`},
		{"server_error", http.StatusInternalServerError, `{"message":"Server Error"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()

			c := NewGitHubClient("ghtoken")
			c.baseURL = srv.URL
			err := c.Update(context.Background(), "gist123", "sale.md", "## body")
			if err == nil {
				t.Fatal("want error on non-2xx")
			}
			// error 文へ status と応答本文が含まれること。token は出ないこと。
			if !strings.Contains(err.Error(), "gist api http") {
				t.Errorf("error must mention http status: %v", err)
			}
			if strings.Contains(err.Error(), "ghtoken") {
				t.Errorf("error must not leak token: %v", err)
			}
		})
	}
}

// TestGitHubClient_Update_TimeoutReturnsRequestError は client timeout 超過が request error になることを検証する。
// gist_update の失敗は job error となり SQS へ再試行される（SPECIFICATION.md 9/15）。
func TestGitHubClient_Update_TimeoutReturnsRequestError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewGitHubClient("ghtoken")
	c.client = &http.Client{Timeout: 50 * time.Millisecond}
	c.baseURL = srv.URL
	err := c.Update(context.Background(), "gist123", "sale.md", "## body")
	if err == nil {
		t.Fatal("want error on client timeout")
	}
}

// TestGitHubClient_Update_ContextCancellationReturnsRequestError は ctx cancel が request error になることを検証する。
func TestGitHubClient_Update_ContextCancellationReturnsRequestError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewGitHubClient("ghtoken")
	c.baseURL = srv.URL
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := c.Update(ctx, "gist123", "sale.md", "## body")
	if err == nil {
		t.Fatal("want error on context cancellation")
	}
}

// TestGitHubClient_Update_ErrorBodyIsLimited は error 応答本文が上限付きで読まれることを検証する。
// 上限を超える応答は切り詰められ、上限以降の内容は error 文へ現れない。巨大な応答で OOM しない。
func TestGitHubClient_Update_ErrorBodyIsLimited(t *testing.T) {
	// errorBodyLimit まで埋めた本文の末尾に、上限外へ追い出される一意の marker を置く。
	body := strings.Repeat("A", errorBodyLimit) + "TRAILING-MARKER-BEYOND-LIMIT"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	c := NewGitHubClient("ghtoken")
	c.baseURL = srv.URL
	err := c.Update(context.Background(), "gist123", "sale.md", "## body")
	if err == nil {
		t.Fatal("want error on non-2xx")
	}
	if !strings.Contains(err.Error(), "gist api http 502") {
		t.Errorf("error must mention http 502: %v", err)
	}
	// 上限以降の marker は切り詰められて error 文へ出ないこと。
	if strings.Contains(err.Error(), "TRAILING-MARKER-BEYOND-LIMIT") {
		t.Errorf("error must not include body beyond errorBodyLimit")
	}
	// token は error 文へ漏れないこと。
	if strings.Contains(err.Error(), "ghtoken") {
		t.Errorf("error must not leak token: %v", err)
	}
}
