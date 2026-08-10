// Package amazon は Amazon 商品ページ・検索ページの HTML 抽出と取得結果分類を提供する。
// CSS セレクタはこのファイルへ集約する（SPECIFICATION.md 11.2, AGENTS.md 5）。
package amazon

// 商品ページの基本セレクタ（SPECIFICATION.md 11.2, UserScript common.js 準拠）。
const (
	selectorTitle        = "#productTitle"
	selectorKindleSwatch = "#tmm-grid-swatch-KINDLE"
	selectorPaperSwatch  = "[id^='tmm-grid-swatch']:not([id$='KINDLE'])"
	selectorCouponBadge  = "i.a-icon.a-icon-addon.newCouponBadge"
	selectorCouponText   = ".couponLabelText"
)

// selectorASINInputs は商品ページの ASIN 取得候補（value 属性を取る input 群）。
const selectorASINInputs = "#ASIN, input[name='idx.asin'], input[name='ASIN.0'], input[name='titleID']"

// selectorCanonical は商品ページの canonical URL。ASIN 抽出の第2優先（SPECIFICATION.md 11.2）。
const selectorCanonical = "link[rel='canonical']"

// selectorBylineAuthors は商品ページの作者表記リンク（実HTML fixture product_B0FX3X569X で確認）。
const selectorBylineAuthors = "#bylineInfo a"

// selectorReleaseDate は UserScript Option+↑ と同じ direct-child 形式の発売日セレクタ。
const selectorReleaseDate = "#rpi-attribute-book_details-publication_date > div.a-section.a-spacing-none.a-text-center.rpi-attribute-value > span"

// selectorReleaseDateFallback は UserScript 群に存在しない新規 fallback 候補。実HTML fixtureで検証が必要。
const selectorReleaseDateFallback = "#detailBullets_feature_div"

// Kindle価格 第1層: KINDLEスウォッチ価格（KU等で0円になる）。
const selectorKindleSwatchPrice = "#tmm-grid-swatch-KINDLE > span.a-button > span.a-button-inner > a.a-button-text > span.slot-price > span"

// kindlePurchasePriceSelectors は Kindle価格 第2層の購入価格候補（KU 0円時に使う）。
var kindlePurchasePriceSelectors = []string{
	"#tmm-grid-swatch-KINDLE .slot-extraMessage .kindleExtraMessage",
	"#tmm-grid-swatch-KINDLE .slot-extraMessage",
	"#tmm-grid-swatch-OTHER .slot-extraMessage .kindleExtraMessage",
	"#tmm-grid-swatch-OTHER .slot-extraMessage",
}

// kindlePriceFallbackSelectors は Kindle価格 第3層の従来候補（第1・第2層で取れないとき）。
var kindlePriceFallbackSelectors = []string{
	"#tmm-grid-swatch-KINDLE > span.a-button > span.a-button-inner > a.a-button-text > span.slot-price > span",
	"#tmm-grid-swatch-OTHER > span.a-button > span.a-button-inner > a.a-button-text > span.slot-price > span",
	"#kindle-price",
	"#a-autoid-2-announce > span.slot-price > span",
	"#tmm-grid-swatch-KINDLE > span.a-button > span.a-button-inner > a.a-button-text > span.slot-extraMessage .kindleExtraMessage .a-color-price",
}

// selectorPaperPrice は紙書籍価格。
const selectorPaperPrice = "[id^='tmm-grid-swatch']:not([id$='KINDLE']) > span.a-button > span.a-button-inner > a.a-button-text > span.slot-price > span"

// kindlePointsSelectors は Kindleポイント 第1層のポイント専用要素。
var kindlePointsSelectors = []string{
	"#tmm-grid-swatch-KINDLE > span.a-button > span.a-button-inner > a.a-button-text > span.slot-buyingPoints > span",
	"#tmm-grid-swatch-OTHER > span.a-button > span.a-button-inner > a.a-button-text > span.slot-buyingPoints > span",
	"#Ebooks-desktop-KINDLE_ALC-prices-loyaltyPoints",
	"#Ebooks-mobile-KINDLE_ALC-prices-loyaltyPoints",
}

// 検索ページセレクタ（SPECIFICATION.md 11.2, UserScript new_release_checker 準拠）。
const (
	selectorSearchResult     = "[data-component-type='s-search-result']"
	selectorSearchTitle      = ".s-title-instructions-style a h2 span"
	selectorSearchTitleFall  = "h2 span"
	selectorSearchPrice      = "span.a-offscreen"
	selectorSearchPriceFall  = ".a-price .a-offscreen"
	selectorSearchAuthor     = ".a-size-base"
	selectorSearchAuthorFall = ".a-row.a-size-base.a-color-secondary"
	selectorSearchFormat     = ".puis-price-instructions-style a.a-text-bold"
	selectorSearchReleaseDay = ".puis-desktop-list-row .puisg-col-4-of-24 div:nth-child(2) div:nth-child(2) span span"
)

// kindleFormatLabel は検索結果カードのKindle形式表示text（実HTMLで確認）。
// この文字列だけが候補をKindle版と確定させる（SPECIFICATION.md 11.2, 13.4）。
const kindleFormatLabel = "Kindle版"
