package amazon

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/PuerkitoBio/goquery"
)

// これらのテストは goquery のセレクタ解釈と抽出ロジックの回帰テストである。
// 作者表記(#bylineInfo a)と紙書籍ページのKindle候補ASIN(#tmm-grid-swatch-KINDLE a[href])は
// 実Amazon HTML fixture (testdata/amazon) に対して検証する。
// CAPTCHA・検索発売日など自動テストから Amazon へアクセスして実HTMLを取得できないページは、
// SPECIFICATION.md 11.2 の既定セレクタが想定するDOM構造を最小合成HTMLで再現して検証する。
// 合成HTMLは既存セレクタの回帰テストが目的であり、推測で新規セレクタを追加しない。
// 実HTML構造の検証は実fixture取得後に別途行う（SPECIFICATION.md 22.2）。

func newDoc(t *testing.T, htmlSource string) *goquery.Document {
	t.Helper()
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(htmlSource))
	if err != nil {
		t.Fatalf("newDoc: %v", err)
	}
	return doc
}

func TestExtractProduct_BasicFields(t *testing.T) {
	const htmlSource = `<html><body>
<span id="productTitle">テスト書籍</span>
<input name="idx.asin" value="B0FX3X569X"/>
<div id="tmm-grid-swatch-KINDLE"><span class="a-button"><span class="a-button-inner"><a class="a-button-text"><span class="slot-price"><span>￥759</span></span></a></span></span></div>
<div id="tmm-grid-swatch-PAPERBACK"><span class="a-button"><span class="a-button-inner"><a class="a-button-text"><span class="slot-price"><span>￥792</span></span></a></span></span></div>
</body></html>`

	got := ExtractProduct(newDoc(t, htmlSource), "B0FX3X569X")
	if got.Title != "テスト書籍" {
		t.Errorf("Title = %q, want テスト書籍", got.Title)
	}
	if got.ASIN != "B0FX3X569X" {
		t.Errorf("ASIN = %q, want B0FX3X569X", got.ASIN)
	}
	if !got.CurrentPrice.Valid() || got.CurrentPrice.Yen() != 759 {
		t.Errorf("CurrentPrice = %+v, want 759", got.CurrentPrice)
	}
	if !got.PaperPrice.Valid() || got.PaperPrice.Yen() != 792 {
		t.Errorf("PaperPrice = %+v, want 792", got.PaperPrice)
	}
	if !got.HasKindleSwatch {
		t.Errorf("HasKindleSwatch = false, want true")
	}
	if !got.HasPaperSwatch {
		t.Errorf("HasPaperSwatch = false, want true")
	}
}

func TestExtractProduct_KindlePriceThreeLayers(t *testing.T) {
	tests := []struct {
		name       string
		htmlSource string
		wantYen    float64
	}{
		{
			name: "第1層のKINDLEスウォッチ価格を採用する",
			htmlSource: `<html><body>
<div id="tmm-grid-swatch-KINDLE"><span class="a-button"><span class="a-button-inner"><a class="a-button-text"><span class="slot-price"><span>￥759</span></span></a></span></span></div>
</body></html>`,
			wantYen: 759,
		},
		{
			// Kindle Unlimited 表示のときKINDLEスウォッチ価格が0円になる（SPECIFICATION.md 11.2, 12.2）。
			name: "KUで第1層が0円のとき第2層の購入価格へフォールバックする",
			htmlSource: `<html><body>
<div id="tmm-grid-swatch-KINDLE"><span class="a-button"><span class="a-button-inner"><a class="a-button-text"><span class="slot-price"><span>￥0</span></span><span class="slot-extraMessage"><span class="kindleExtraMessage">または ￥759 で購入</span></span></a></span></span></div>
</body></html>`,
			wantYen: 759,
		},
		{
			name: "第1・第2層で取れないとき第3層の候補を採用する",
			htmlSource: `<html><body>
<span id="kindle-price">￥800</span>
</body></html>`,
			wantYen: 800,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractProduct(newDoc(t, tc.htmlSource), "B0FX3X569X")
			if !got.CurrentPrice.Valid() || got.CurrentPrice.Yen() != tc.wantYen {
				t.Fatalf("CurrentPrice = %+v, want %v", got.CurrentPrice, tc.wantYen)
			}
		})
	}
}

func TestExtractProduct_KindlePriceAbsentIsUnknown(t *testing.T) {
	const htmlSource = `<html><body><span id="productTitle">タイトル</span></body></html>`
	got := ExtractProduct(newDoc(t, htmlSource), "B0FX3X569X")
	if got.CurrentPrice.Valid() {
		t.Fatalf("CurrentPrice = %+v, want unknown", got.CurrentPrice)
	}
}

func TestExtractProduct_PointsFromBuyingPoints(t *testing.T) {
	const htmlSource = `<html><body>
<div id="tmm-grid-swatch-KINDLE"><span class="a-button"><span class="a-button-inner"><a class="a-button-text"><span class="slot-buyingPoints"><span>388pt</span></span></a></span></span></div>
</body></html>`
	got := ExtractProduct(newDoc(t, htmlSource), "B0FX3X569X")
	if got.Points != 388 {
		t.Errorf("Points = %d, want 388", got.Points)
	}
}

func TestExtractProduct_CouponFirstChildExcludesTerms(t *testing.T) {
	const htmlSource = `<html><body>
<i class="a-icon a-icon-addon newCouponBadge">クーポン: 適用済</i>
<div class="couponLabelText">500円OFF <a href="#">規約</a></div>
</body></html>`
	got := ExtractProduct(newDoc(t, htmlSource), "B0FX3X569X")
	if !got.Coupon {
		t.Errorf("Coupon = false, want true")
	}
	if got.CouponText != "500円OFF" {
		t.Errorf("CouponText = %q, want 500円OFF", got.CouponText)
	}
}

func TestExtractProduct_CouponAbsent(t *testing.T) {
	const htmlSource = `<html><body><span id="productTitle">タイトル</span></body></html>`
	got := ExtractProduct(newDoc(t, htmlSource), "B0FX3X569X")
	if got.Coupon {
		t.Errorf("Coupon = true, want false")
	}
	if got.CouponText != "" {
		t.Errorf("CouponText = %q, want empty", got.CouponText)
	}
}

// SPECIFICATION.md 22.2「ポイントなし」。ポイント専用要素がなければ0ポイントになる。
func TestExtractProduct_PointsAbsentIsZero(t *testing.T) {
	const htmlSource = `<html><body>
<div id="tmm-grid-swatch-KINDLE"><span class="a-button"><span class="a-button-inner"><a class="a-button-text"><span class="slot-price"><span>￥759</span></span></a></span></span></div>
</body></html>`
	got := ExtractProduct(newDoc(t, htmlSource), "B0FX3X569X")
	if got.Points != 0 {
		t.Errorf("Points = %d, want 0 (ポイント要素なし)", got.Points)
	}
}

// SPECIFICATION.md 22.2「Kindle版スウォッチなし」。KINDLEスウォッチがなければ HasKindleSwatch=false。
// 商品ページとしての必須構造(#productTitle)はあり、価格は第3層候補から取得できる。
// not_kindle terminal 判定はアプリケーション層(sale/newrelease/papertokindle)のテストで検証する。
func TestExtractProduct_KindleSwatchAbsent(t *testing.T) {
	const htmlSource = `<html><body>
<span id="productTitle">紙書籍のみ</span>
<div id="tmm-grid-swatch-PAPERBACK"><span class="a-button"><span class="a-button-inner"><a class="a-button-text"><span class="slot-price"><span>￥792</span></span></a></span></span></div>
<span id="kindle-price">￥0</span>
</body></html>`
	got := ExtractProduct(newDoc(t, htmlSource), "4434361325")
	if got.HasKindleSwatch {
		t.Errorf("HasKindleSwatch = true, want false")
	}
	if !got.HasPaperSwatch {
		t.Errorf("HasPaperSwatch = false, want true")
	}
}

func TestExtractProduct_ReleaseDate(t *testing.T) {
	const htmlSource = `<html><body>
<div id="rpi-attribute-book_details-publication_date">
  <div class="a-section a-spacing-none a-text-center rpi-attribute-value"><span>2026年8月28日</span></div>
</div>
</body></html>`
	got := ExtractProduct(newDoc(t, htmlSource), "B0FX3X569X")
	want := time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC)
	if !got.HasReleaseDate || !got.ReleaseDate.Equal(want) {
		t.Fatalf("ReleaseDate = %v (has=%v), want %v", got.ReleaseDate, got.HasReleaseDate, want)
	}
}

func TestExtractSearch_BasicFields(t *testing.T) {
	// 検索発売日セレクタは nth-child を含む厳密構造で実HTML fixture による検証が必要なため、
	// このテストでは ASIN/タイトル/価格/作者表記だけを検証する。
	const htmlSource = `<html><body>
<div data-component-type="s-search-result">
  <div class="s-title-instructions-style"><a href="/dp/B0FX3X569X/ref=..."><h2><span>テスト書籍</span></h2></a></div>
  <span class="a-offscreen">￥759</span>
  <div class="a-size-base">海李 (著)</div>
</div>
</body></html>`

	hits := ExtractSearch(newDoc(t, htmlSource))
	if len(hits) != 1 {
		t.Fatalf("len(hits) = %d, want 1", len(hits))
	}
	h := hits[0]
	if h.ASIN != "B0FX3X569X" {
		t.Errorf("ASIN = %q, want B0FX3X569X", h.ASIN)
	}
	if h.Title != "テスト書籍" {
		t.Errorf("Title = %q, want テスト書籍", h.Title)
	}
	if !h.Price.Valid() || h.Price.Yen() != 759 {
		t.Errorf("Price = %+v, want 759", h.Price)
	}
	if h.AuthorLabel != "海李 (著)" {
		t.Errorf("AuthorLabel = %q, want 海李 (著)", h.AuthorLabel)
	}
}

func TestExtractSearch_Empty(t *testing.T) {
	const htmlSource = `<html><body><div>検索結果なし</div></body></html>`
	hits := ExtractSearch(newDoc(t, htmlSource))
	if len(hits) != 0 {
		t.Fatalf("len(hits) = %d, want 0", len(hits))
	}
}

// SPECIFICATION.md 22.2「検索結果に発売日あり」。
// 検索発売日セレクタは UserScript new_release_checker 由来の nth-child 構造
// (.puis-desktop-list-row .puisg-col-4-of-24 div:nth-child(2) div:nth-child(2) span span) であり、
// 実検索HTML fixtureがtest環境にないため、同セレクタが想定するDOMを最小合成HTMLで再現して回帰検証する。
// 実HTML構造の検証は実fixture取得後に別途行う。
func TestExtractSearch_ReleaseDatePresent(t *testing.T) {
	const htmlSource = `<html><body>
<div data-component-type="s-search-result">
  <div class="s-title-instructions-style"><a href="/dp/B0FX3X569X/ref=x"><h2><span>テスト書籍</span></h2></a></div>
  <span class="a-offscreen">￥759</span>
  <div class="puis-desktop-list-row">
    <div class="puisg-col-4-of-24">
      <div>種別</div>
      <div>
        <div>形式</div>
        <div><span><span>2026年9月5日</span></span></div>
      </div>
    </div>
  </div>
</div>
</body></html>`

	hits := ExtractSearch(newDoc(t, htmlSource))
	if len(hits) != 1 {
		t.Fatalf("len(hits) = %d, want 1", len(hits))
	}
	h := hits[0]
	if !h.HasReleaseDate {
		t.Fatalf("HasReleaseDate = false, want true")
	}
	want := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	if !h.ReleaseDate.Equal(want) {
		t.Errorf("ReleaseDate = %v, want %v", h.ReleaseDate, want)
	}
}

// SPECIFICATION.md 22.2「検索結果に発売日なし」。発売日要素がなければ HasReleaseDate=false。
func TestExtractSearch_ReleaseDateAbsent(t *testing.T) {
	const htmlSource = `<html><body>
<div data-component-type="s-search-result">
  <div class="s-title-instructions-style"><a href="/dp/B0FX3X569X/ref=x"><h2><span>テスト書籍</span></h2></a></div>
  <span class="a-offscreen">￥759</span>
</div>
</body></html>`

	hits := ExtractSearch(newDoc(t, htmlSource))
	if len(hits) != 1 {
		t.Fatalf("len(hits) = %d, want 1", len(hits))
	}
	if hits[0].HasReleaseDate {
		t.Errorf("HasReleaseDate = true, want false (発売日要素なし)")
	}
}

// TestExtractASIN_PriorityRules は ASIN を HTML input → canonical URL → redirect 後の
// 最終URL の順で fallback し、いずれもなければ空になることを検証する（SPECIFICATION.md 11.2）。
// 元の要求ASIN で無条件に代用しない。
func TestExtractASIN_PriorityRules(t *testing.T) {
	tests := []struct {
		name     string
		htmlSrc  string
		finalURL string
		want     string
	}{
		{
			name:     "HTML input を優先（canonical/finalURL と不一致でも input 採用）",
			htmlSrc:  `<html><body><input name="idx.asin" value="B0AAAA0001"/></body></html>`,
			finalURL: "https://www.amazon.co.jp/dp/B0CCCC0003",
			want:     "B0AAAA0001",
		},
		{
			name:     "input がなければ canonical URL を採用（finalURL より優先）",
			htmlSrc:  `<html><head><link rel="canonical" href="https://www.amazon.co.jp/dp/B0BBBB0002"/></head><body></body></html>`,
			finalURL: "https://www.amazon.co.jp/dp/B0CCCC0003",
			want:     "B0BBBB0002",
		},
		{
			name:     "input/canonical がなければ redirect 後の finalURL から抽出",
			htmlSrc:  `<html><body></body></html>`,
			finalURL: "https://www.amazon.co.jp/gp/product/B0CCCC0003/ref=x",
			want:     "B0CCCC0003",
		},
		{
			name:     "どこにもASINがなければ空（要求ASINで代用しない）",
			htmlSrc:  `<html><body></body></html>`,
			finalURL: "https://www.amazon.co.jp/some/other/path",
			want:     "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := extractASIN(newDoc(t, tc.htmlSrc), tc.finalURL)
			if got != tc.want {
				t.Fatalf("extractASIN = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestExtractASINFromURLSupportsMultiplePathFormats(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{name: "dp形式", url: "/dp/B0FX3X569X/ref=...", want: "B0FX3X569X"},
		{name: "gp/product形式", url: "/gp/product/B0FX3X569X", want: "B0FX3X569X"},
		{name: "kindle-dbs/product形式", url: "/kindle-dbs/product/B0FX3X569X", want: "B0FX3X569X"},
		{name: "ASINを含まないURLは空", url: "/some/other/path", want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractASINFromURL(tc.url); got != tc.want {
				t.Fatalf("extractASINFromURL(%q) = %q, want %q", tc.url, got, tc.want)
			}
		})
	}
}

// loadFixtureDoc は testdata/amazon の実HTML fixture を goquery.Document へ読み込む。
func loadFixtureDoc(t *testing.T, name string) *goquery.Document {
	t.Helper()
	data, err := os.ReadFile("../../testdata/amazon/" + name)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(data)))
	if err != nil {
		t.Fatalf("parse fixture %s: %v", name, err)
	}
	return doc
}

// 実HTML fixture: Kindle商品ページの作者表記は #bylineInfo a のテキスト群。
// 現EditionがKindleなので KINDLEスウォッチのリンクは javascript:void(0) になり候補ASINは空。
func TestExtractProduct_RealFixture_KindlePageAuthorLabel(t *testing.T) {
	got := ExtractProduct(loadFixtureDoc(t, "product_B0FX3X569X.html"), "B0FX3X569X")
	if !strings.Contains(got.AuthorLabel, "上原誠") || !strings.Contains(got.AuthorLabel, "やきいもほくほく") {
		t.Errorf("AuthorLabel = %q, want 上原誆 と やきいもほくほく を含む", got.AuthorLabel)
	}
	if got.KindleSwatchASIN != "" {
		t.Errorf("KindleSwatchASIN = %q, want empty (current edition is Kindle)", got.KindleSwatchASIN)
	}
	if !got.HasKindleSwatch {
		t.Errorf("HasKindleSwatch = false, want true")
	}
}

// 実HTML fixture: 紙書籍(ISBN)ページの KINDLEスウォッチリンクからKindle版ASINを取り出す。
func TestExtractProduct_RealFixture_PaperPageKindleSwatchASIN(t *testing.T) {
	got := ExtractProduct(loadFixtureDoc(t, "paper_4434361325.html"), "4434361325")
	if got.KindleSwatchASIN != "B0FX3X569X" {
		t.Errorf("KindleSwatchASIN = %q, want B0FX3X569X", got.KindleSwatchASIN)
	}
	if !got.HasPaperSwatch || !got.HasKindleSwatch {
		t.Errorf("HasPaperSwatch=%v HasKindleSwatch=%v, want both true", got.HasPaperSwatch, got.HasKindleSwatch)
	}
}
