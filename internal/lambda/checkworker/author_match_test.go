package checkworker

import (
	"strings"
	"testing"

	"github.com/PuerkitoBio/goquery"

	"github.com/shinderuman/kindle-automation/internal/amazon"
	"github.com/shinderuman/kindle-automation/internal/application/newrelease"
)

func mustDoc(t *testing.T, htmlSrc string) *goquery.Document {
	t.Helper()
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(htmlSrc))
	if err != nil {
		t.Fatalf("newDoc: %v", err)
	}
	return doc
}

// TestAuthorMatch_SearchExtractionToApplication は検索ページ抽出の AuthorLabel
// （"海李 (著)" のように役割表記を含む）が application の AuthorMatches で正しく照合されることを
// 抽出から検証する（bug1）。役割表記の除去と部分名の誤検出防止を含む。
func TestAuthorMatch_SearchExtractionToApplication(t *testing.T) {
	const searchHTML = `<html><body>
<div data-component-type="s-search-result">
  <div class="s-title-instructions-style"><a href="/dp/B0FX3X569X"><h2><span>タイトル</span></h2></a></div>
  <div class="a-size-base">海李 (著)</div>
</div>
</body></html>`

	hits := amazon.ExtractSearch(mustDoc(t, searchHTML))
	if len(hits) != 1 {
		t.Fatalf("search hits = %d, want 1", len(hits))
	}
	contributors := hits[0].Contributors
	if len(contributors) != 1 || contributors[0] != "海李 (著)" {
		t.Fatalf("search Contributors = %v, want [海李 (著)]", contributors)
	}
	if !newrelease.AuthorMatches("海李", contributors) {
		t.Errorf("AuthorMatches(海李, %v) = false, want true (役割表記を除去して一致)", contributors)
	}
	if newrelease.AuthorMatches("上原誠", contributors) {
		t.Errorf("AuthorMatches(上原誠, %v) = true, want false", contributors)
	}
	// 部分名（"海"）は contributor と完全一致しないため Hit しない。
	if newrelease.AuthorMatches("海", contributors) {
		t.Errorf("partial name 海 must not match %v", contributors)
	}
}

// TestAuthorMatch_ProductExtractionMultipleContributors は商品ページ #bylineInfo a から抽出した
// 複数 contributor 表記（"上原誠 やきいもほくほく"）が application の AuthorMatches で
// 各 contributor に照合することを検証する（bug1）。部分名の誤検出防止を含む。
func TestAuthorMatch_ProductExtractionMultipleContributors(t *testing.T) {
	const productHTML = `<html><body>
<span id="productTitle">タイトル</span>
<div id="bylineInfo"><a href="/foo">上原誠</a><a href="/bar">やきいもほくほく</a></div>
</body></html>`

	info := amazon.ExtractProduct(mustDoc(t, productHTML), "https://www.amazon.co.jp/dp/B0FX3X569X")
	wantContributors := []string{"上原誠", "やきいもほくほく"}
	if len(info.Contributors) != len(wantContributors) || info.Contributors[0] != wantContributors[0] || info.Contributors[1] != wantContributors[1] {
		t.Fatalf("product Contributors = %v, want %v", info.Contributors, wantContributors)
	}
	contributors := info.Contributors
	for _, author := range wantContributors {
		if !newrelease.AuthorMatches(author, contributors) {
			t.Errorf("AuthorMatches(%s, %v) = false, want true (複数contributorのいずれかに一致)", author, contributors)
		}
	}
	for _, partial := range []string{"上原", "誠", "やきいも"} {
		if newrelease.AuthorMatches(partial, contributors) {
			t.Errorf("partial name %q must not match %v", partial, contributors)
		}
	}
}
