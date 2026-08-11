package amazon

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/net/html"

	"github.com/shinderuman/kindle-automation/internal/domain/book"
)

var (
	// priceRe はスウォッチ・ポイント専用要素の text から数字列を取り出す（UserScript common.js PRICE）。
	priceRe = regexp.MustCompile(`([\d,]+)`)
	// purchasePriceRe は購入価格文言から ￥/¥ 付き金額を取り出す（UserScript common.js PURCHASE_PRICE）。
	purchasePriceRe = []*regexp.Regexp{
		regexp.MustCompile(`(?:または[、,\s]*|購入価格[：:\s]*)[￥¥]\s*([\d,]+)(?:\s*で購入)?`),
		regexp.MustCompile(`[￥¥]\s*([\d,]+)\s*で購入`),
	}
	// pointsRe はポイント数を取り出す（UserScript common.js POINTS。大小文字区別なし）。
	pointsRe = regexp.MustCompile(`(?i)([\d,]+)\s*(?:pt|ポイント)`)
	// searchPriceRe は検索ページの ￥ 付き金額（UserScript new_release_checker）。
	searchPriceRe = regexp.MustCompile(`￥([\d,]+)`)
	// searchAsinRe は URL path から ASIN を取り出す（SPECIFICATION.md 11.2。dp/gp/product/kindle-dbs/product の3形式を許容）。
	searchAsinRe = regexp.MustCompile(`/(?:dp|gp/product|kindle-dbs/product)/([A-Z0-9]{10})`)
)

type ProductInfo struct {
	Title            string
	ASIN             string
	CurrentPrice     book.Price
	Points           int
	Coupon           bool
	CouponText       string
	PaperPrice       book.Price
	ReleaseDate      time.Time
	HasReleaseDate   bool
	HasKindleSwatch  bool
	HasPaperSwatch   bool
	Contributors     []string
	KindleSwatchASIN string
}

type SearchHit struct {
	ASIN           string
	Title          string
	URL            string
	Price          book.Price
	Contributors   []string
	ReleaseDate    time.Time
	HasReleaseDate bool
	// 検索結果カードのKindle形式表示でKindle版と確認できたか（SPECIFICATION.md 13.4）。
	IsKindle bool
}

// finalURL は redirect 後の最終URL。ASIN は extractASIN が HTML input → canonical URL →
// finalURL の順で fallback して決定し、元の要求ASIN で無条件に代用しない（SPECIFICATION.md 11.2）。
func ExtractProduct(doc *goquery.Document, finalURL string) ProductInfo {
	releaseDate, hasDate := extractReleaseDate(doc)
	coupon, couponText := extractCoupon(doc)
	return ProductInfo{
		Title:            textOf(doc, selectorTitle),
		ASIN:             extractASIN(doc, finalURL),
		CurrentPrice:     extractKindlePrice(doc),
		Points:           extractPoints(doc),
		Coupon:           coupon,
		CouponText:       couponText,
		PaperPrice:       priceFromSelector(doc, selectorPaperPrice),
		ReleaseDate:      releaseDate,
		HasReleaseDate:   hasDate,
		HasKindleSwatch:  doc.Find(selectorKindleSwatch).Length() > 0,
		HasPaperSwatch:   doc.Find(selectorPaperSwatch).Length() > 0,
		Contributors:     extractProductContributors(doc),
		KindleSwatchASIN: extractKindleSwatchASIN(doc),
	}
}

// 役割表記`(著)`は兄弟 span にありリンクテキストには含まれないため、各リンクテキストを
// そのまま1 contributor とする（実HTML fixture product_B0FX3X569X で確認）。
func extractProductContributors(doc *goquery.Document) []string {
	var contributors []string
	doc.Find(selectorBylineAuthors).Each(func(_ int, s *goquery.Selection) {
		if t := strings.TrimSpace(s.Text()); t != "" {
			contributors = append(contributors, t)
		}
	})
	return contributors
}

// 現EditionがKindleのときスウォッチリンクは javascript:void(0) になりASINは空になる。
func extractKindleSwatchASIN(doc *goquery.Document) string {
	var asin string
	doc.Find(selectorKindleSwatch + " a[href]").Each(func(_ int, s *goquery.Selection) {
		if asin != "" {
			return
		}
		href, _ := s.Attr("href")
		if a := extractASINFromURL(href); a != "" {
			asin = a
		}
	})
	return asin
}

func ExtractSearch(doc *goquery.Document) []SearchHit {
	var hits []SearchHit
	doc.Find(selectorSearchResult).Each(func(_ int, s *goquery.Selection) {
		hits = append(hits, extractSearchHit(s))
	})
	return hits
}

// extractKindlePrice は UserScript getKindlePrice と同じ3層構造で Kindle 価格を抽出する。
func extractKindlePrice(doc *goquery.Document) book.Price {
	if p := priceFromSelector(doc, selectorKindleSwatchPrice); p.Valid() {
		return p
	}
	for _, sel := range kindlePurchasePriceSelectors {
		if p := parsePurchasePrice(textOf(doc, sel)); p.Valid() {
			return p
		}
	}
	for _, sel := range kindlePriceFallbackSelectors {
		if p := priceFromSelector(doc, sel); p.Valid() {
			return p
		}
	}
	return book.UnknownPrice()
}

// extractPoints は UserScript getKindlePoints と同じ2層構造でポイントを抽出する。
func extractPoints(doc *goquery.Document) int {
	for _, sel := range kindlePointsSelectors {
		if pt := parsePoints(textOf(doc, sel)); pt > 0 {
			return pt
		}
	}
	for _, sel := range kindlePurchasePriceSelectors {
		if pt := parsePoints(textOf(doc, sel)); pt > 0 {
			return pt
		}
	}
	return 0
}

func extractCoupon(doc *goquery.Document) (bool, string) {
	badge := doc.Find(selectorCouponBadge).First()
	if badge.Length() == 0 {
		return false, ""
	}
	if !strings.Contains(badge.Text(), "クーポン:") {
		return false, ""
	}
	return true, firstChildText(doc.Find(selectorCouponText).First())
}

// 観測済み primary selector だけを使い、未検証の fallback は持たない。
func extractReleaseDate(doc *goquery.Document) (time.Time, bool) {
	return parseReleaseDateOptional(textOf(doc, selectorReleaseDate))
}

func extractSearchHit(s *goquery.Selection) SearchHit {
	title := firstNonEmpty(
		strings.TrimSpace(s.Find(selectorSearchTitle).First().Text()),
		strings.TrimSpace(s.Find(selectorSearchTitleFall).First().Text()),
	)

	linkEl := s.Find(selectorSearchTitle).First().Closest("a")
	if linkEl.Length() == 0 {
		linkEl = s.Find("h2 a, .a-link-normal[href*='/dp/']").First()
	}
	url, _ := linkEl.Attr("href")

	price := parseSearchPrice(strings.TrimSpace(s.Find(selectorSearchPrice).First().Text()))
	if !price.Valid() {
		price = parseSearchPrice(strings.TrimSpace(s.Find(selectorSearchPriceFall).First().Text()))
	}

	author := firstNonEmpty(
		strings.TrimSpace(s.Find(selectorSearchAuthor).First().Text()),
		strings.TrimSpace(s.Find(selectorSearchAuthorFall).First().Text()),
	)

	dayText := strings.TrimSpace(s.Find(selectorSearchReleaseDay).First().Text())
	releaseDate, hasDate := parseReleaseDateOptional(dayText)

	return SearchHit{
		ASIN:           extractASINFromURL(url),
		Title:          title,
		URL:            url,
		Price:          price,
		Contributors:   splitSearchContributors(author),
		ReleaseDate:    releaseDate,
		HasReleaseDate: hasDate,
		IsKindle:       isKindleFormat(s.Find(selectorSearchFormat).First().Text()),
	}
}

// 実HTMLでは1テキストに `著者A、 著者B | 販売者:... | 日付` のように複数 contributor と
// 販売者・日付が混入するため、先頭の ` | ` までを著者部分とし `、` で分割する（SPECIFICATION.md 11.2）。
func splitSearchContributors(authorText string) []string {
	authorText = strings.TrimSpace(authorText)
	if authorText == "" {
		return nil
	}
	if idx := strings.Index(authorText, " | "); idx >= 0 {
		authorText = authorText[:idx]
	}
	var contributors []string
	for _, name := range strings.Split(authorText, "、") {
		if name = strings.TrimSpace(name); name != "" {
			contributors = append(contributors, name)
		}
	}
	return contributors
}

// SPECIFICATION.md 13.4: 検索結果カードの形式表示がKindle版か。
func isKindleFormat(formatText string) bool {
	return strings.TrimSpace(formatText) == kindleFormatLabel
}

func textOf(doc *goquery.Document, selector string) string {
	return strings.TrimSpace(doc.Find(selector).First().Text())
}

func priceFromSelector(doc *goquery.Document, selector string) book.Price {
	return parsePrice(textOf(doc, selector))
}

func parsePrice(text string) book.Price {
	m := priceRe.FindStringSubmatch(text)
	if m == nil {
		return book.UnknownPrice()
	}
	return yenToPrice(m[1])
}

func parsePurchasePrice(text string) book.Price {
	for _, re := range purchasePriceRe {
		m := re.FindStringSubmatch(text)
		if m == nil {
			continue
		}
		if p := yenToPrice(m[1]); p.Valid() {
			return p
		}
	}
	return book.UnknownPrice()
}

func parseSearchPrice(text string) book.Price {
	m := searchPriceRe.FindStringSubmatch(text)
	if m == nil {
		return book.UnknownPrice()
	}
	return yenToPrice(m[1])
}

func parsePoints(text string) int {
	m := pointsRe.FindStringSubmatch(text)
	if m == nil {
		return 0
	}
	pt, err := strconv.Atoi(strings.ReplaceAll(m[1], ",", ""))
	if err != nil || pt <= 0 {
		return 0
	}
	return pt
}

func yenToPrice(digits string) book.Price {
	yen, err := strconv.Atoi(strings.ReplaceAll(digits, ",", ""))
	if err != nil || yen <= 0 {
		return book.UnknownPrice()
	}
	return book.NewPrice(float64(yen))
}

func parseReleaseDateOptional(text string) (time.Time, bool) {
	if text == "" {
		return time.Time{}, false
	}
	t, err := book.ParseReleaseDate(text)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// 子要素の規約等を混ぜず、最初の直接テキストノードだけを返す。
func firstChildText(sel *goquery.Selection) string {
	var found string
	sel.Contents().Each(func(_ int, s *goquery.Selection) {
		if found != "" {
			return
		}
		for _, n := range s.Nodes {
			if n.Type == html.TextNode {
				found = strings.TrimSpace(n.Data)
				return
			}
		}
	})
	return found
}

// ASIN を優先規則で取り出す（SPECIFICATION.md 11.2）。
// 1. ページ内 ASIN 入力欄の value
// 2. canonical URL の path
// 3. redirect 後の最終URL（呼び出し側が resp.Request.URL から渡す）
// いずれも取れなければ空。元の要求ASIN で無条件に代用しない。
func extractASIN(doc *goquery.Document, finalURL string) string {
	if v, ok := doc.Find(selectorASINInputs).First().Attr("value"); ok && v != "" {
		return v
	}
	if href, ok := doc.Find(selectorCanonical).First().Attr("href"); ok && href != "" {
		if a := extractASINFromURL(href); a != "" {
			return a
		}
	}
	return extractASINFromURL(finalURL)
}

func extractASINFromURL(rawURL string) string {
	m := searchAsinRe.FindStringSubmatch(rawURL)
	if m == nil {
		return ""
	}
	return m[1]
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
