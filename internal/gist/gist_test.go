package gist

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
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

func TestGitHubClient_Update_Success(t *testing.T) {
	var gotMethod, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewGitHubClient("ghtoken")
	c.baseURL = srv.URL
	if err := c.Update(context.Background(), "gist123", "sale.md", "## body"); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("method = %s, want PATCH", gotMethod)
	}
	if gotAuth != "token ghtoken" {
		t.Errorf("auth = %q, want token ghtoken", gotAuth)
	}
}

func TestGitHubClient_Update_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = io.WriteString(w, `{"message":"Validation Failed"}`)
	}))
	defer srv.Close()

	c := NewGitHubClient("ghtoken")
	c.baseURL = srv.URL
	err := c.Update(context.Background(), "gist123", "sale.md", "## body")
	if err == nil {
		t.Fatal("want error on 4xx")
	}
}
