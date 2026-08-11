package gist

import (
	"context"
	"fmt"

	"github.com/shinderuman/kindle-automation/internal/application/execution"
	"github.com/shinderuman/kindle-automation/internal/domain/book"
)

// gist_update ジョブ結果の error_type（SPECIFICATION.md 18.1）。
const (
	errorTypeGistLoad        = "gist_load"
	errorTypeGistUpdate      = "gist_update"
	errorTypeGistUnknownType = "gist_unknown_type"
)

// gist_type（SPECIFICATION.md 15）。sale/new_release/paper_to_kindle。
const (
	// TypeSale は Sale Gist 再生成を指定する gist_type 値。
	TypeSale = "sale"
	// TypeNewRelease は New Release（作者）Gist 再生成を指定する gist_type 値。
	TypeNewRelease = "new_release"
	// TypePaperToKindle は Paper-to-Kindle Gist 再生成を指定する gist_type 値。
	TypePaperToKindle = "paper_to_kindle"
)

// BookListReader は書籍JSONの全件読込。storage.BookFileStore が満たす。
type BookListReader interface {
	Books(ctx context.Context) ([]book.KindleBook, error)
}

// AuthorListReader は作者JSONの全件読込。storage.AuthorFileStore が満たす。
type AuthorListReader interface {
	Authors(ctx context.Context) ([]book.Author, error)
}

// Target は1つの Gist の更新先（ID と filename）。
type Target struct {
	ID       string
	Filename string
}

// Settings は gist_type ごとの Gist 更新先。
type Settings struct {
	Sale          Target
	NewRelease    Target
	PaperToKindle Target
}

// Dependencies は gist_update 処理の外部依存。
type Dependencies struct {
	SaleBooks  BookListReader   // unprocessed_asins.json
	PaperBooks BookListReader   // paper_books_asins.json
	Authors    AuthorListReader // authors.json
	Updater    GistUpdater
	Settings   Settings
}

// Update は gist_type に応じて S3 全体から Markdown を再生成し Gist を更新する（SPECIFICATION.md 15）。
// gist_update は Amazon アクセスも S3 書き込みも行わない。GitHub API 失敗は error として返し、
// composition root が gist_error（SPECIFICATION.md 18.3）を出す。
func Update(ctx context.Context, deps Dependencies, gistType string) (execution.Outcome, error) {
	switch gistType {
	case TypeSale:
		books, err := deps.SaleBooks.Books(ctx)
		if err != nil {
			return execution.Errored(errorTypeGistLoad, 0, 0), fmt.Errorf("load sale books: %w", err)
		}
		if err := deps.Updater.Update(ctx, deps.Settings.Sale.ID, deps.Settings.Sale.Filename, BooksMarkdown(books)); err != nil {
			return execution.Errored(errorTypeGistUpdate, 0, 0), fmt.Errorf("update sale gist: %w", err)
		}
	case TypePaperToKindle:
		books, err := deps.PaperBooks.Books(ctx)
		if err != nil {
			return execution.Errored(errorTypeGistLoad, 0, 0), fmt.Errorf("load paper books: %w", err)
		}
		if err := deps.Updater.Update(ctx, deps.Settings.PaperToKindle.ID, deps.Settings.PaperToKindle.Filename, BooksMarkdown(books)); err != nil {
			return execution.Errored(errorTypeGistUpdate, 0, 0), fmt.Errorf("update paper-to-kindle gist: %w", err)
		}
	case TypeNewRelease:
		authors, err := deps.Authors.Authors(ctx)
		if err != nil {
			return execution.Errored(errorTypeGistLoad, 0, 0), fmt.Errorf("load authors: %w", err)
		}
		if err := deps.Updater.Update(ctx, deps.Settings.NewRelease.ID, deps.Settings.NewRelease.Filename, AuthorsMarkdown(authors)); err != nil {
			return execution.Errored(errorTypeGistUpdate, 0, 0), fmt.Errorf("update new_release gist: %w", err)
		}
	default:
		return execution.Errored(errorTypeGistUnknownType, 0, 0), fmt.Errorf("unknown gist_type %q", gistType)
	}
	return execution.Completed(0, 0), nil
}
