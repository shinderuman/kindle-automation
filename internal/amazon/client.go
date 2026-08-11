package amazon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
)

// SPECIFICATION.md 11.1 の共通リクエスト設定。
const (
	requestTimeout = 15 * time.Second
	maxRedirects   = 5
	maxBodyBytes   int64 = 8 * 1024 * 1024

	userAgent      = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0 Safari/537.36"
	acceptLanguage = "ja-JP,ja;q=0.9"
	acceptHeader   = "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"

	amazonBase         = "https://www.amazon.co.jp"
	amazonApex         = "amazon.co.jp"
	amazonDomainSuffix = ".amazon.co.jp"
	dPPrefix           = "https://www.amazon.co.jp/dp/"
	searchPrefix       = "https://www.amazon.co.jp/s?k="
)

var ErrBodyTooLarge = errors.New("amazon response body exceeds 8 MiB")

var ErrTooManyRedirects = errors.New("too many redirects")

var ErrNonAmazonRedirect = errors.New("redirect to non-Amazon host")

// HTTPStatus/ResponseBytes は構造化ログ（SPECIFICATION.md 18.1）へ伝播するHTTP計測値。
// 未送信時は0。Amazon HTML本文は logger/上位層へ出さないため本文は含めない。
type FetchResult struct {
	Category      Category
	Info          ProductInfo
	HTTPStatus    int
	ResponseBytes int
}

// HTTPStatus/ResponseBytes は構造化ログ（SPECIFICATION.md 18.1）へ伝播するHTTP計測値。
type SearchResult struct {
	Category      Category
	Hits          []SearchHit
	HTTPStatus    int
	ResponseBytes int
}

// HTTP 内で再試行せず、retryable は呼び出し側（Lambda error 経由の SQS 再配信）へ委ねる。
type Client struct {
	httpClient *http.Client
	baseURL    string
}

// Cookie・Authorization・Amazonログイン情報は送らない。
func NewClient() *Client {
	return newClientWithHTTPClient(amazonBase, &http.Client{
		Timeout:       requestTimeout,
		CheckRedirect: checkRedirect,
	})
}

func newClientWithHTTPClient(baseURL string, hc *http.Client) *Client {
	return &Client{httpClient: hc, baseURL: baseURL}
}

func (c *Client) FetchProduct(ctx context.Context, asin string) (FetchResult, error) {
	body, status, finalURL, err := c.fetch(ctx, c.productURL(asin))
	if err != nil {
		// body 超過は応答ありの retryable として計測値を保持して結果へ変換する（SPECIFICATION.md 11.3, 18.1）。
		// それ以外（redirect 上限・非Amazon host・通信失敗）は応答がないため status/byte 数は0。
		if errors.Is(err, ErrBodyTooLarge) {
			return FetchResult{Category: CategoryRetryable, HTTPStatus: status, ResponseBytes: len(body)}, nil
		}
		return FetchResult{}, err
	}
	responseBytes := len(body)
	if category := classifyResponse(status, body); category != CategoryOK {
		return FetchResult{Category: category, HTTPStatus: status, ResponseBytes: responseBytes}, nil
	}
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return FetchResult{Category: CategoryRetryable, HTTPStatus: status, ResponseBytes: responseBytes}, nil
	}
	info := ExtractProduct(doc, finalURL)
	if info.Title == "" {
		// 200 でも必須構造（#productTitle）がない場合は取得内容不足として再試行する。
		return FetchResult{Category: CategoryRetryable, HTTPStatus: status, ResponseBytes: responseBytes}, nil
	}
	if info.ASIN == "" {
		// canonical/final URL のいずれからも対象ASINを確認できない200は正常とせず、
		// 必須構造欠落の取得内容不足として再試行する（SPECIFICATION.md 11.3）。
		// 要求ASINを無条件に代入して検証を形骸化しない。
		return FetchResult{Category: CategoryRetryable, HTTPStatus: status, ResponseBytes: responseBytes}, nil
	}
	return FetchResult{Category: CategoryOK, Info: info, HTTPStatus: status, ResponseBytes: responseBytes}, nil
}

// 検索結果0件は search_empty 分類とし、呼び出し側で retryable outcome へ写像する。
func (c *Client) FetchSearch(ctx context.Context, author string) (SearchResult, error) {
	body, status, _, err := c.fetch(ctx, c.searchURL(author))
	if err != nil {
		if errors.Is(err, ErrBodyTooLarge) {
			return SearchResult{Category: CategoryRetryable, HTTPStatus: status, ResponseBytes: len(body)}, nil
		}
		return SearchResult{}, err
	}
	responseBytes := len(body)
	if category := classifyResponse(status, body); category != CategoryOK {
		return SearchResult{Category: category, HTTPStatus: status, ResponseBytes: responseBytes}, nil
	}
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return SearchResult{Category: CategoryRetryable, HTTPStatus: status, ResponseBytes: responseBytes}, nil
	}
	hits := ExtractSearch(doc)
	if len(hits) == 0 {
		return SearchResult{Category: CategorySearchEmpty, HTTPStatus: status, ResponseBytes: responseBytes}, nil
	}
	return SearchResult{Category: CategoryOK, Hits: hits, HTTPStatus: status, ResponseBytes: responseBytes}, nil
}

// finalURL は resp.Request.URL から取得し、商品ページの ASIN 抽出フォールバックに使う。
func (c *Client) fetch(ctx context.Context, rawURL string) (body []byte, status int, finalURL string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, 0, "", fmt.Errorf("new request: %w", err)
	}
	setHeaders(req)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, "", err
	}
	defer resp.Body.Close()
	status = resp.StatusCode
	finalURL = resp.Request.URL.String()
	limited := io.LimitReader(resp.Body, maxBodyBytes+1)
	body, err = io.ReadAll(limited)
	if err != nil {
		return nil, status, finalURL, fmt.Errorf("read body: %w", err)
	}
	if int64(len(body)) > maxBodyBytes {
		// 超過時も byte 数を上位へ伝播させるため body を返す。呼び出し側は本文を parse せず
		// retryable 結果へ変換する（SPECIFICATION.md 11.3, 18.1）。
		return body, status, finalURL, ErrBodyTooLarge
	}
	return body, status, finalURL, nil
}

func classifyResponse(status int, body []byte) Category {
	if IsBlockedPage(string(body)) {
		return CategoryRetryable
	}
	return ClassifyHTTPStatus(status)
}

// Cookie・Authorization は送らない。
func setHeaders(req *http.Request) {
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept-Language", acceptLanguage)
	req.Header.Set("Accept", acceptHeader)
}

func checkRedirect(req *http.Request, via []*http.Request) error {
	if err := checkRedirectCount(via); err != nil {
		return err
	}
	if req.URL.Host != "" && !IsAmazonHost(req.URL.Host) {
		return fmt.Errorf("%w: %s", ErrNonAmazonRedirect, req.URL.Host)
	}
	return nil
}

// Go の http.Client は N 件目の redirect を追跡する直前に len(via)==N で呼ぶため、
// maxRedirects 回までは許容し maxRedirects+1 回目を拒否するには len(via) > maxRedirects で弾く。
func checkRedirectCount(via []*http.Request) error {
	if len(via) > maxRedirects {
		return ErrTooManyRedirects
	}
	return nil
}

// "amazon.co.jp.evil.example" のような suffix 偽装は許容しない。
func IsAmazonHost(host string) bool {
	return host == amazonApex || strings.HasSuffix(host, amazonDomainSuffix)
}

func (c *Client) productURL(asin string) string {
	return c.baseURL + "/dp/" + asin
}

// SPECIFICATION.md 13.2 の検索 URL（baseURL 基準）。
func (c *Client) searchURL(author string) string {
	return c.baseURL + "/s?k=" + url.QueryEscape(author) + "&i=digital-text&rh=n%3A2250738051&s=date-desc-rank"
}

// 取得用途（SPECIFICATION.md 11.1）では partnerTag を空にし、保存用途（9.2）では partnerTag を付ける。
func ProductURL(asin string, partnerTag string) string {
	u := dPPrefix + asin
	if partnerTag != "" {
		u += "?tag=" + partnerTag
	}
	return u
}

// SPECIFICATION.md 13.2 の新刊検索 URL。
func SearchURL(author string) string {
	return searchPrefix + url.QueryEscape(author) + "&i=digital-text&rh=n%3A2250738051&s=date-desc-rank"
}
