// Package gist は GitHub Gist の再生成と更新を担う（SPECIFICATION.md 15）。
//
// gist_update ジョブは Amazon アクセスも S3 書き込みも行わず、実行時点の S3 全体から
// Markdown を再生成して GitHub Gist API を呼ぶ。Markdown 生成と GitHub API 呼び出しを分離し、
// それぞれ純粋関数と net/http adapter として実装する。
package gist

import (
	"fmt"
	"strings"

	"github.com/shinderuman/kindle-automation/internal/domain/book"
)

// BooksMarkdown は書籍一覧から Sale・Paper-to-Kindle 共通の Markdown を生成する（SPECIFICATION.md 15）。
// 入力は S3 保存時の並び順（発売日降順・同日タイトル昇順）のまま受け取る。
// タイトルが「モンスターコミックス」を含む場合は既存 kindle_bot と同じく末尾へ 👹 を付ける。
func BooksMarkdown(books []book.KindleBook) string {
	lines := make([]string, 0, len(books))
	for _, b := range books {
		title := b.Title
		if strings.Contains(title, "モンスターコミックス") {
			title += " 👹"
		}
		lines = append(lines, fmt.Sprintf("* [[%s]%s (%.0f円)](%s)", b.ReleaseDate.Format("2006-01-02"), title, b.CurrentPrice.Yen(), b.URL))
	}
	return fmt.Sprintf("## 合計 %d冊\n%s", len(books), strings.Join(lines, "\n"))
}

// AuthorsMarkdown は作者一覧から Author Gist 用 Markdown を生成する（SPECIFICATION.md 15）。
func AuthorsMarkdown(authors []book.Author) string {
	lines := []string{"| 作者 | 最新作 |", "|------|--------|"}
	for _, a := range authors {
		lines = append(lines, fmt.Sprintf("| [%s](%s) | [[%s] %s](%s) |", a.Name, a.URL, a.LatestReleaseDate.Format("2006-01-02"), a.LatestReleaseTitle, a.LatestReleaseURL))
	}
	return fmt.Sprintf("## 合計 %d人(最新の単行本発売日降順)\n%s", len(authors), strings.Join(lines, "\n"))
}
