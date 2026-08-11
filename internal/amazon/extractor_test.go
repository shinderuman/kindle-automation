package amazon

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/PuerkitoBio/goquery"
)

// 作者表記(#bylineInfo a)と紙書籍ページのKindle候補ASIN(#tmm-grid-swatch-KINDLE a[href])は
// 実Amazon HTML fixture (testdata/amazon) に対して検証する。
// CAPTCHA・検索発売日など自動テストから Amazon へアクセスして実HTMLを取得できないページは、
// SPECIFICATION.md 11.2 の既定セレクタが想定するDOM構造を最小合成HTMLで再現して検証する。
// 合成HTMLは既存セレクタの回帰テストが目的であり、推測で新規セレクタを追加しない。

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

func TestExtractProduct_PurchasePriceWithoutPrefixIsParsed(t *testing.T) {
	const htmlSource = `<html><body>
<div id="tmm-grid-swatch-KINDLE"><span class="a-button"><span class="a-button-inner"><a class="a-button-text"><span class="slot-price"><span>￥0</span></span><span class="slot-extraMessage"><span class="kindleExtraMessage">￥1234で購入</span></span></a></span></span></div>
</body></html>`

	got := ExtractProduct(newDoc(t, htmlSource), "B0FX3X569X")
	if !got.CurrentPrice.Valid() || got.CurrentPrice.Yen() != 1234 {
		t.Fatalf("CurrentPrice = %+v, want 1234", got.CurrentPrice)
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

func TestExtractProduct_PointsAbsentIsZero(t *testing.T) {
	const htmlSource = `<html><body>
<div id="tmm-grid-swatch-KINDLE"><span class="a-button"><span class="a-button-inner"><a class="a-button-text"><span class="slot-price"><span>￥759</span></span></a></span></span></div>
</body></html>`
	got := ExtractProduct(newDoc(t, htmlSource), "B0FX3X569X")
	if got.Points != 0 {
		t.Errorf("Points = %d, want 0 (ポイント要素なし)", got.Points)
	}
}

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
	// 検索発売日セレクタは nth-child 厳密構造で実HTML fixture が必要なため、ここでは ASIN/タイトル/価格/作者表記だけ検証する。
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
	if len(h.Contributors) != 1 || h.Contributors[0] != "海李 (著)" {
		t.Errorf("Contributors = %#v, want [\"海李 (著)\"]", h.Contributors)
	}
	if h.IsKindle {
		t.Errorf("IsKindle = true, want false (形式表示なし)")
	}
}

func TestExtractSearch_Empty(t *testing.T) {
	const htmlSource = `<html><body><div>検索結果なし</div></body></html>`
	hits := ExtractSearch(newDoc(t, htmlSource))
	if len(hits) != 0 {
		t.Fatalf("len(hits) = %d, want 0", len(hits))
	}
}

func TestExtractSearch_RealFixture_KindleFormatAndContributors(t *testing.T) {
	hits := ExtractSearch(loadFixtureDoc(t, "search_digital_text.html"))
	if len(hits) < 1 {
		t.Fatalf("len(hits) = %d, want >=1 from real fixture", len(hits))
	}
	for i, h := range hits {
		if !h.IsKindle {
			t.Errorf("hits[%d].IsKindle = false, want true (実fixtureのカードはKindle版表示)", i)
		}
	}
	first := hits[0]
	if !containsString(first.Contributors, "村上春樹") {
		t.Errorf("first.Contributors = %#v, want 村上春樹 を含む（販売者・日付は除外）", first.Contributors)
	}
	for _, c := range first.Contributors {
		if strings.Contains(c, "販売者") || strings.Contains(c, "2026/") {
			t.Errorf("contributor %q に販売者/日付が混入", c)
		}
	}
}

// 非Kindle・種別不明の最小fixtureは実markupの構造を基に形式markerを差し替え/除去して作る（SPECIFICATION.md 13.4）。
func TestExtractSearch_KindleFormatClassification(t *testing.T) {
	const kindleCard = `<div data-component-type="s-search-result">
  <div class="s-title-instructions-style"><a href="/dp/B0KKKKKK01"><h2><span>K</span></h2></a></div>
  <div class="puis-price-instructions-style"><a class="a-size-base a-link-normal a-text-bold">Kindle版</a></div>
</div>`
	const audibleCard = `<div data-component-type="s-search-result">
  <div class="s-title-instructions-style"><a href="/dp/B0AAAAAA02"><h2><span>A</span></h2></a></div>
  <div class="puis-price-instructions-style"><a class="a-size-base a-link-normal a-text-bold">オーディオブック</a></div>
</div>`
	const unknownCard = `<div data-component-type="s-search-result">
  <div class="s-title-instructions-style"><a href="/dp/B0UUUUUU03"><h2><span>U</span></h2></a></div>
  <div class="puis-price-instructions-style"><span class="a-color-secondary">形式不明</span></div>
</div>`
	doc := newDoc(t, `<html><body>`+kindleCard+audibleCard+unknownCard+`</body></html>`)
	hits := ExtractSearch(doc)
	if len(hits) != 3 {
		t.Fatalf("len(hits) = %d, want 3", len(hits))
	}
	want := map[string]bool{"B0KKKKKK01": true, "B0AAAAAA02": false, "B0UUUUUU03": false}
	for _, h := range hits {
		got, ok := want[h.ASIN]
		if !ok {
			t.Fatalf("unexpected ASIN %q", h.ASIN)
		}
		if h.IsKindle != got {
			t.Errorf("ASIN %q IsKindle = %v, want %v", h.ASIN, h.IsKindle, got)
		}
	}
}

func TestSplitSearchContributors(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{name: "単一contributorと役割", in: "海李 (著)", want: []string{"海李 (著)"}},
		{name: "複数contributorと販売者日付", in: "スコット・フィッツジェラルド、 村上春樹 | 販売者:Amazon Services International LLC  | 2026/8/7", want: []string{"スコット・フィッツジェラルド", "村上春樹"}},
		{name: "空", in: "  ", want: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := splitSearchContributors(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("splitSearchContributors(%q) = %#v, want %#v", tc.in, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("splitSearchContributors(%q)[%d] = %q, want %q", tc.in, i, got[i], tc.want[i])
				}
			}
		})
	}
}

// 検索発売日セレクタは UserScript new_release_checker 由来の nth-child 構造。実検索HTML fixtureがないため、
// 同セレクタが想定するDOMを最小合成HTMLで再現して回帰検証する（SPECIFICATION.md 22.2）。
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

func TestExtractProduct_RealFixture_KindlePageContributors(t *testing.T) {
	got := ExtractProduct(loadFixtureDoc(t, "product_B0FX3X569X.html"), "B0FX3X569X")
	if !containsString(got.Contributors, "上原誠") || !containsString(got.Contributors, "やきいもほくほく") {
		t.Errorf("Contributors = %#v, want 上原誠 と やきいもほくほく を含む", got.Contributors)
	}
	if got.KindleSwatchASIN != "" {
		t.Errorf("KindleSwatchASIN = %q, want empty (current edition is Kindle)", got.KindleSwatchASIN)
	}
	if !got.HasKindleSwatch {
		t.Errorf("HasKindleSwatch = false, want true")
	}
}

func containsString(items []string, want string) bool {
	for _, v := range items {
		if v == want {
			return true
		}
	}
	return false
}

func TestExtractProduct_RealFixture_PaperPageKindleSwatchASIN(t *testing.T) {
	got := ExtractProduct(loadFixtureDoc(t, "paper_4434361325.html"), "4434361325")
	if got.KindleSwatchASIN != "B0FX3X569X" {
		t.Errorf("KindleSwatchASIN = %q, want B0FX3X569X", got.KindleSwatchASIN)
	}
	if !got.HasPaperSwatch || !got.HasKindleSwatch {
		t.Errorf("HasPaperSwatch=%v HasKindleSwatch=%v, want both true", got.HasPaperSwatch, got.HasKindleSwatch)
	}
}

func TestExtractProduct_PointsFromExtraMessage(t *testing.T) {
	const htmlSource = `<html><body>
<div id="tmm-grid-swatch-KINDLE"><span class="a-button"><span class="a-button-inner"><a class="a-button-text"><span class="slot-extraMessage"><span class="kindleExtraMessage">または ￥759 で購入 388pt</span></span></a></span></span></div>
</body></html>`
	got := ExtractProduct(newDoc(t, htmlSource), "B0FX3X569X")
	if got.Points != 388 {
		t.Errorf("Points = %d, want 388 (第2層 slot-extraMessage から抽出)", got.Points)
	}
}

func TestExtractProduct_CouponBadgeWithoutKeyword(t *testing.T) {
	const htmlSource = `<html><body>
<i class="a-icon a-icon-addon newCouponBadge">セール中</i>
<div class="couponLabelText">500円OFF</div>
</body></html>`
	got := ExtractProduct(newDoc(t, htmlSource), "B0FX3X569X")
	if got.Coupon {
		t.Errorf("Coupon = true, want false (バッジに クーポン: なし)")
	}
	if got.CouponText != "" {
		t.Errorf("CouponText = %q, want empty", got.CouponText)
	}
}

func TestExtractProduct_CouponBadgeKeywordButNoLabelText(t *testing.T) {
	const htmlSource = `<html><body>
<i class="a-icon a-icon-addon newCouponBadge">クーポン: 適用済</i>
</body></html>`
	got := ExtractProduct(newDoc(t, htmlSource), "B0FX3X569X")
	if !got.Coupon {
		t.Errorf("Coupon = false, want true (バッジに クーポン: あり)")
	}
	if got.CouponText != "" {
		t.Errorf("CouponText = %q, want empty (.couponLabelText 不在)", got.CouponText)
	}
}

// 固定額/率の表現差で抽出を変えない（SPECIFICATION.md 11.2）。
func TestExtractProduct_CouponPercentageText(t *testing.T) {
	const htmlSource = `<html><body>
<i class="a-icon a-icon-addon newCouponBadge">クーポン: 適用済</i>
<div class="couponLabelText">10% OFF <a href="#">規約</a></div>
</body></html>`
	got := ExtractProduct(newDoc(t, htmlSource), "B0FX3X569X")
	if !got.Coupon {
		t.Errorf("Coupon = false, want true")
	}
	if got.CouponText != "10% OFF" {
		t.Errorf("CouponText = %q, want 10%% OFF", got.CouponText)
	}
}

// 数字を含まない価格は0円として保存せず未取得とする（SPECIFICATION.md 11.2, 11.3）。
func TestExtractProduct_MalformedPriceIsUnknown(t *testing.T) {
	const htmlSource = `<html><body>
<span id="productTitle">タイトル</span>
<span id="kindle-price">価格未定</span>
</body></html>`
	got := ExtractProduct(newDoc(t, htmlSource), "B0FX3X569X")
	if got.CurrentPrice.Valid() {
		t.Errorf("CurrentPrice = %+v, want unknown (価格解析失敗)", got.CurrentPrice)
	}
}

// 発売日は "YYYY/M/D" 形式も受け付け UTC 00:00:00 へ正規化する（SPECIFICATION.md 11.2）。
func TestExtractProduct_ReleaseDateSlashForm(t *testing.T) {
	const htmlSource = `<html><body>
<div id="rpi-attribute-book_details-publication_date">
  <div class="a-section a-spacing-none a-text-center rpi-attribute-value"><span>2026/8/28</span></div>
</div>
</body></html>`
	got := ExtractProduct(newDoc(t, htmlSource), "B0FX3X569X")
	want := time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC)
	if !got.HasReleaseDate || !got.ReleaseDate.Equal(want) {
		t.Fatalf("ReleaseDate = %v (has=%v), want %v", got.ReleaseDate, got.HasReleaseDate, want)
	}
}

// 期待値 2026/1/30 は fixture HTML 本文から直接読んだ値（selector 実装由来ではない）。
// 未検証 fallback(#detailBullets_feature_div)へ依存しないことを維持する。
func TestExtractProduct_RealFixture_ReleaseDate(t *testing.T) {
	got := ExtractProduct(loadFixtureDoc(t, "product_B0FX3X569X.html"), "B0FX3X569X")
	want := time.Date(2026, 1, 30, 0, 0, 0, 0, time.UTC)
	if !got.HasReleaseDate || !got.ReleaseDate.Equal(want) {
		t.Fatalf("ReleaseDate = %v (has=%v), want %v via primary selector", got.ReleaseDate, got.HasReleaseDate, want)
	}
}

func TestExtractProduct_MalformedReleaseDate(t *testing.T) {
	const htmlSource = `<html><body>
<div id="rpi-attribute-book_details-publication_date">
  <div class="a-section a-spacing-none a-text-center rpi-attribute-value"><span>発売未定</span></div>
</div>
</body></html>`
	got := ExtractProduct(newDoc(t, htmlSource), "B0FX3X569X")
	if got.HasReleaseDate {
		t.Errorf("HasReleaseDate = true, want false (発売日解析失敗)")
	}
}

func TestParsePoints_ZeroAndAbsent(t *testing.T) {
	cases := []struct {
		name string
		text string
		want int
	}{
		{name: "0ptは0", text: "0pt", want: 0},
		{name: "ポイント表記なしは0", text: "（ポイントなし）", want: 0},
		{name: "日本語ポイント表記", text: "388ポイント", want: 388},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parsePoints(tc.text); got != tc.want {
				t.Fatalf("parsePoints(%q) = %d, want %d", tc.text, got, tc.want)
			}
		})
	}
}

func TestExtractSearch_URLFallback(t *testing.T) {
	const htmlSource = `<html><body>
<div data-component-type="s-search-result">
  <h2><span>タイトル</span></h2>
  <a class="a-link-normal" href="/dp/B0FX3X569X/ref=x">リンク</a>
</div>
</body></html>`
	hits := ExtractSearch(newDoc(t, htmlSource))
	if len(hits) != 1 {
		t.Fatalf("len(hits) = %d, want 1", len(hits))
	}
	if hits[0].ASIN != "B0FX3X569X" {
		t.Errorf("ASIN = %q, want B0FX3X569X (URL fallback)", hits[0].ASIN)
	}
	if hits[0].Title != "タイトル" {
		t.Errorf("Title = %q, want タイトル", hits[0].Title)
	}
}

func TestExtractKindleSwatchASIN_PicksFirstAndSkipsRest(t *testing.T) {
	const htmlSource = `<html><body>
<div id="tmm-grid-swatch-KINDLE">
  <a href="/dp/B0FX3X569X/ref=x">a</a>
  <a href="/dp/B0OTHER012/ref=y">b</a>
</div>
</body></html>`
	got := ExtractProduct(newDoc(t, htmlSource), "4434361325")
	if got.KindleSwatchASIN != "B0FX3X569X" {
		t.Errorf("KindleSwatchASIN = %q, want B0FX3X569X (最初のリンクを採用)", got.KindleSwatchASIN)
	}
}
