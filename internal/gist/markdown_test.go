package gist

import (
	"strings"
	"testing"
	"time"

	"github.com/shinderuman/kindle-automation/internal/domain/book"
)

func TestBooksMarkdown_FormatAndMonsterSuffix(t *testing.T) {
	books := []book.KindleBook{
		{Title: "通常作品", ReleaseDate: time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC), CurrentPrice: book.NewPrice(759), URL: "https://www.amazon.co.jp/dp/B000000001"},
		{Title: "モンスターコミックス作品", ReleaseDate: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), CurrentPrice: book.NewPrice(500), URL: "https://www.amazon.co.jp/dp/B000000002"},
	}
	got := BooksMarkdown(books)
	if !strings.HasPrefix(got, "## 合計 2冊\n") {
		t.Errorf("header mismatch: %q", got)
	}
	if !strings.Contains(got, "* [[2026-08-28]通常作品 (759円)](https://www.amazon.co.jp/dp/B000000001)") {
		t.Errorf("normal line missing: %q", got)
	}
	if !strings.Contains(got, "モンスターコミックス作品 👹 (500円)") {
		t.Errorf("monster suffix missing: %q", got)
	}
}

func TestBooksMarkdown_Empty(t *testing.T) {
	got := BooksMarkdown(nil)
	if got != "## 合計 0冊\n" {
		t.Errorf("empty = %q", got)
	}
}

func TestAuthorsMarkdown_Table(t *testing.T) {
	authors := []book.Author{
		{Name: "海李", URL: "https://www.amazon.co.jp/dp/B00000000A", LatestReleaseDate: time.Date(2025, 12, 28, 0, 0, 0, 0, time.UTC), LatestReleaseTitle: "最新作", LatestReleaseURL: "https://www.amazon.co.jp/dp/B00000000B"},
	}
	got := AuthorsMarkdown(authors)
	if !strings.HasPrefix(got, "## 合計 1人(最新の単行本発売日降順)\n") {
		t.Errorf("header mismatch: %q", got)
	}
	if !strings.Contains(got, "| 作者 | 最新作 |") || !strings.Contains(got, "|------|--------|") {
		t.Errorf("table header missing: %q", got)
	}
	if !strings.Contains(got, "| [海李](https://www.amazon.co.jp/dp/B00000000A) | [[2025-12-28] 最新作](https://www.amazon.co.jp/dp/B00000000B) |") {
		t.Errorf("author row missing: %q", got)
	}
}

// TestAuthorsMarkdown_Empty は作者0件の空リスト表現を検証する（SPECIFICATION.md 15）。
// ヘッダ・count=0 を維持し、data 行はない。空配列は GitHub API を呼ぶ gist_update ではなく
// reader 側で必須 object 欠落として扱うため、ここへ到達するのは S3 が空配列の正常時のみ。
func TestAuthorsMarkdown_Empty(t *testing.T) {
	got := AuthorsMarkdown(nil)
	if !strings.HasPrefix(got, "## 合計 0人(最新の単行本発売日降順)\n") {
		t.Errorf("header mismatch: %q", got)
	}
	if !strings.Contains(got, "| 作者 | 最新作 |") || !strings.Contains(got, "|------|--------|") {
		t.Errorf("table header missing: %q", got)
	}
	// data 行が1つもないこと。"| [" を含まなければ空リスト。
	if strings.Contains(got, "| [") {
		t.Errorf("empty authors must have no data rows: %q", got)
	}
}
