package amazon

import "strings"

// Category は HTTP 取得結果の分類（SPECIFICATION.md 11.3）。
// 構造検証（必須要素の有無、CAPTCHA、ASIN不一致、Kindle種別）は呼び出し側が別途行い、
// この型は主に HTTP status に基づく大分類を表す。
type Category int

const (
	// CategoryOK は 200。必須構造の検証は呼び出し側で別途行う。
	CategoryOK Category = iota
	// CategoryNotFound は 404 または明示的な商品不存在。terminal。
	CategoryNotFound
	// CategoryPermanentClientError は 400 等、恒久的 4xx。terminal。
	CategoryPermanentClientError
	// CategoryRetryable は 403/429/5xx。再試行する。
	CategoryRetryable
	// CategorySearchEmpty は検索ページは200だが結果0件。search_empty として再試行する。
	CategorySearchEmpty
)

// ClassifyHTTPStatus は HTTP status code を大分類する（SPECIFICATION.md 11.3）。
// 200/404/403,429/4xx/5xx を分ける。構造検証はこの関数の責務外。
func ClassifyHTTPStatus(status int) Category {
	switch {
	case status == 200:
		return CategoryOK
	case status == 404:
		return CategoryNotFound
	case status == 403 || status == 429:
		return CategoryRetryable
	case status >= 400 && status < 500:
		return CategoryPermanentClientError
	case status >= 500:
		return CategoryRetryable
	default:
		return CategoryRetryable
	}
}

// blockedPageMarkers は CAPTCHA やアクセス拒否ページを示す文言。
// lambda-poc で観測された CAPTCHA 文言を実装の起点とする。実HTML fixtureで追加検証が必要。
var blockedPageMarkers = []string{
	"画像に表示されている文字を入力",
}

// IsBlockedPage は CAPTCHA やアクセス拒否ページかを返す。
// body 全文の一部を渡す。実HTMLの CAPTCHA ページ全体像は fixture 不足のため、
// marker は暫定であり実HTML取得後に拡充する。
func IsBlockedPage(bodyText string) bool {
	for _, marker := range blockedPageMarkers {
		if strings.Contains(bodyText, marker) {
			return true
		}
	}
	return false
}
