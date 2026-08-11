package amazon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const validProductHTML = `<html><body>
<span id="productTitle">テスト書籍</span>
<input name="idx.asin" value="B0FX3X569X"/>
<div id="tmm-grid-swatch-KINDLE"><span class="a-button"><span class="a-button-inner"><a class="a-button-text"><span class="slot-price"><span>￥759</span></span></a></span></span></div>
</body></html>`

const searchHitHTML = `<html><body>
<div data-component-type="s-search-result">
  <div class="s-title-instructions-style"><a href="/dp/B0FX3X569X/ref=x"><h2><span>タイトル</span></h2></a></div>
</div>
</body></html>`

func newTestClient(t *testing.T, ts *httptest.Server, timeout time.Duration) *Client {
	t.Helper()
	return newClientWithHTTPClient(ts.URL, &http.Client{
		Timeout:       timeout,
		CheckRedirect: checkRedirect,
	})
}

func TestFetchProduct_Success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(validProductHTML))
	}))
	defer ts.Close()

	result, err := newTestClient(t, ts, 5*time.Second).FetchProduct(context.Background(), "B0FX3X569X")
	if err != nil {
		t.Fatalf("FetchProduct: %v", err)
	}
	if result.Category != CategoryOK {
		t.Errorf("Category = %v, want OK", result.Category)
	}
	if result.Info.Title != "テスト書籍" {
		t.Errorf("Title = %q", result.Info.Title)
	}
	if !result.Info.HasKindleSwatch {
		t.Errorf("HasKindleSwatch = false, want true")
	}
}

func TestFetchProduct_RedirectToNonAmazonHost(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://example.com/", http.StatusFound)
	}))
	defer ts.Close()

	_, err := newTestClient(t, ts, 5*time.Second).FetchProduct(context.Background(), "B0FX3X569X")
	if !errors.Is(err, ErrNonAmazonRedirect) {
		t.Fatalf("err = %v, want ErrNonAmazonRedirect", err)
	}
}

func TestFetchProduct_BodyTooLarge(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		big := make([]byte, maxBodyBytes+1)
		for i := range big {
			big[i] = 'a'
		}
		_, _ = w.Write(big)
	}))
	defer ts.Close()

	// body 超過は応答ありの retryable として計測値を保持した FetchResult へ変換する
	// （SPECIFICATION.md 11.3 body超過=retryable, 18.1 http_status/response_bytes）。
	result, err := newTestClient(t, ts, 30*time.Second).FetchProduct(context.Background(), "B0FX3X569X")
	if err != nil {
		t.Fatalf("FetchProduct: %v", err)
	}
	if result.Category != CategoryRetryable {
		t.Errorf("Category = %v, want Retryable", result.Category)
	}
	if result.HTTPStatus != 200 {
		t.Errorf("HTTPStatus = %d, want 200 (応答あり)", result.HTTPStatus)
	}
	if result.ResponseBytes <= int(maxBodyBytes) {
		t.Errorf("ResponseBytes = %d, want > %d", result.ResponseBytes, maxBodyBytes)
	}
}

func TestFetchProduct_ShortBodyIsRetryable(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html><body>短い</body></html>"))
	}))
	defer ts.Close()

	result, err := newTestClient(t, ts, 5*time.Second).FetchProduct(context.Background(), "B0FX3X569X")
	if err != nil {
		t.Fatalf("FetchProduct: %v", err)
	}
	if result.Category != CategoryRetryable {
		t.Errorf("short 200 without #productTitle must be retryable, got %v", result.Category)
	}
}

func TestFetchProduct_MissingASINIsRetryable(t *testing.T) {
	// #productTitle はあるが ASIN input・canonical link ともになく、最終URL path にも
	// /dp/{ASIN} が無い200。対象ASINを canonical/final URL のいずれからも確認できないため
	// 必須構造欠落の取得内容不足として再試行する（SPECIFICATION.md 11.3）。
	// 要求ASINを無条件に代入して検証を形骸化しない。
	htmlBody := `<html><body><span id="productTitle">タイトル</span></body></html>`
	c := newClientWithHTTPClient(amazonBase, &http.Client{
		Timeout:       5 * time.Second,
		CheckRedirect: checkRedirect,
		Transport:     finalURLNoASINRT{body: []byte(htmlBody)},
	})
	result, err := c.FetchProduct(context.Background(), "B0FX3X569X")
	if err != nil {
		t.Fatalf("FetchProduct: %v", err)
	}
	if result.Category != CategoryRetryable {
		t.Errorf("200 without ASIN source must be retryable, got %v", result.Category)
	}
	if result.HTTPStatus != 200 {
		t.Errorf("HTTPStatus = %d, want 200 (応答あり)", result.HTTPStatus)
	}
}

// finalURLNoASINRT は応答をそのまま返しつつ、最終URL path を ASIN を含まない /selected へ
// 替える Transport。Amazon が商品URLを /dp/{ASIN} を含まない path へ誘導した状況を再現する。
type finalURLNoASINRT struct{ body []byte }

func (rt finalURLNoASINRT) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Path = "/selected"
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(rt.body)),
		Request:    req,
		Header:     make(http.Header),
	}, nil
}

func TestFetchProduct_CaptchaIsRetryable(t *testing.T) {
	// CAPTCHAページ全体像は実HTML fixtureがtest環境にないため、lambda-pocで観測された
	// CAPTCHA文言を含む最小合成bodyで分類を検証する（SPECIFICATION.md 11.3, 22.2）。
	// markerの拡充は実HTML取得後に行い、ここでは推測で文言を追加しない。
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html><body>画像に表示されている文字を入力してください</body></html>"))
	}))
	defer ts.Close()

	result, err := newTestClient(t, ts, 5*time.Second).FetchProduct(context.Background(), "B0FX3X569X")
	if err != nil {
		t.Fatalf("FetchProduct: %v", err)
	}
	if result.Category != CategoryRetryable {
		t.Errorf("CAPTCHA page must be retryable, got %v", result.Category)
	}
}

func TestFetchProduct_Timeout(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		time.Sleep(1 * time.Second)
	}))
	defer ts.Close()

	_, err := newTestClient(t, ts, 50*time.Millisecond).FetchProduct(context.Background(), "B0FX3X569X")
	if err == nil {
		t.Fatal("timeout must return error")
	}
}

func TestFetchProduct_StatusClassification(t *testing.T) {
	tests := []struct {
		name   string
		status int
		want   Category
	}{
		{name: "404 NotFound", status: http.StatusNotFound, want: CategoryNotFound},
		{name: "400 PermanentClientError", status: http.StatusBadRequest, want: CategoryPermanentClientError},
		{name: "429 Retryable", status: http.StatusTooManyRequests, want: CategoryRetryable},
		{name: "403 Retryable", status: http.StatusForbidden, want: CategoryRetryable},
		{name: "500 Retryable", status: http.StatusInternalServerError, want: CategoryRetryable},
		{name: "503 Retryable", status: http.StatusServiceUnavailable, want: CategoryRetryable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
			}))
			defer ts.Close()

			result, err := newTestClient(t, ts, 5*time.Second).FetchProduct(context.Background(), "B0FX3X569X")
			if err != nil {
				t.Fatalf("FetchProduct: %v", err)
			}
			if result.Category != tc.want {
				t.Errorf("status %d: Category = %v, want %v", tc.status, result.Category, tc.want)
			}
		})
	}
}

func TestFetchSearch_SuccessAndEmpty(t *testing.T) {
	// 結果あり → CategoryOK
	tsOK := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(searchHitHTML))
	}))
	defer tsOK.Close()

	result, err := newTestClient(t, tsOK, 5*time.Second).FetchSearch(context.Background(), "海李")
	if err != nil {
		t.Fatalf("FetchSearch: %v", err)
	}
	if result.Category != CategoryOK || len(result.Hits) != 1 {
		t.Fatalf("Category=%v Hits=%d, want OK/1", result.Category, len(result.Hits))
	}
	if result.Hits[0].ASIN != "B0FX3X569X" {
		t.Errorf("hit ASIN = %q", result.Hits[0].ASIN)
	}

	// 結果0件 → CategorySearchEmpty（search_empty）
	tsEmpty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html><body>結果なし</body></html>"))
	}))
	defer tsEmpty.Close()

	empty, err := newTestClient(t, tsEmpty, 5*time.Second).FetchSearch(context.Background(), "海李")
	if err != nil {
		t.Fatalf("FetchSearch empty: %v", err)
	}
	if empty.Category != CategorySearchEmpty {
		t.Errorf("empty search must be CategorySearchEmpty, got %v", empty.Category)
	}
}

func TestProductURL_WithAndWithoutPartnerTag(t *testing.T) {
	if got := ProductURL("B0FX3X569X", ""); got != "https://www.amazon.co.jp/dp/B0FX3X569X" {
		t.Errorf("ProductURL without tag = %q", got)
	}
	got := ProductURL("B0FX3X569X", "shinderuman03-22")
	if !strings.Contains(got, "tag=shinderuman03-22") {
		t.Errorf("ProductURL with tag = %q", got)
	}
}

func TestSearchURL_EncodesAuthor(t *testing.T) {
	got := SearchURL("海 李")
	if !strings.Contains(got, "k=") || strings.Contains(got, " ") {
		t.Errorf("SearchURL must encode author: %q", got)
	}
}

func TestCheckRedirect_RedirectCountBoundary(t *testing.T) {
	tests := []struct {
		name    string
		viaLen  int
		wantErr error
	}{
		{name: "4 redirects allowed", viaLen: maxRedirects - 1, wantErr: nil},
		{name: "5 redirects allowed", viaLen: maxRedirects, wantErr: nil},
		{name: "6th redirect rejected", viaLen: maxRedirects + 1, wantErr: ErrTooManyRedirects},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			via := make([]*http.Request, tc.viaLen)
			for i := range via {
				via[i] = &http.Request{}
			}
			req := &http.Request{URL: &url.URL{Host: "www.amazon.co.jp"}}
			if err := checkRedirect(req, via); !errors.Is(err, tc.wantErr) {
				t.Errorf("checkRedirect viaLen=%d: err = %v, want %v", tc.viaLen, err, tc.wantErr)
			}
			if err := checkRedirectCount(via); !errors.Is(err, tc.wantErr) {
				t.Errorf("checkRedirectCount viaLen=%d: err = %v, want %v", tc.viaLen, err, tc.wantErr)
			}
		})
	}
}

func TestCheckRedirect_NonAmazonHost(t *testing.T) {
	req := &http.Request{URL: &url.URL{Host: "example.com"}}
	if err := checkRedirect(req, nil); !errors.Is(err, ErrNonAmazonRedirect) {
		t.Errorf("err = %v, want ErrNonAmazonRedirect", err)
	}
}

func redirectChainServer(targetHops int32) (*httptest.Server, *int32) {
	var hops int32
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hops, 1)
		if n <= targetHops {
			http.Redirect(w, r, fmt.Sprintf("/?h=%d", n), http.StatusFound)
			return
		}
		_, _ = w.Write([]byte(validProductHTML))
	})), &hops
}

// newRedirectCountClient は redirect 回数上限（checkRedirectCount）だけを適用する client を返す。
// httptest server が 127.0.0.1 で IsAmazonHost を通らないため、実 redirect chain で回数境界を
// 検証するには回数判定だけを注入する。host 制限は TestFetchProduct_RedirectToNonAmazonHost で担保する。
func newRedirectCountClient(t *testing.T, ts *httptest.Server) *Client {
	t.Helper()
	return newClientWithHTTPClient(ts.URL, &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(_ *http.Request, via []*http.Request) error { return checkRedirectCount(via) },
	})
}

func TestCheckRedirect_FiveRedirectChainSucceeds(t *testing.T) {
	ts, hops := redirectChainServer(maxRedirects)
	defer ts.Close()

	result, err := newRedirectCountClient(t, ts).FetchProduct(context.Background(), "B0FX3X569X")
	if err != nil {
		t.Fatalf("FetchProduct after 5 redirects: %v", err)
	}
	if result.Category != CategoryOK {
		t.Errorf("Category = %v, want OK after 5 redirects", result.Category)
	}
	if got := atomic.LoadInt32(hops); got != maxRedirects+1 {
		t.Errorf("server hops = %d, want %d (5 redirects + final 200)", got, maxRedirects+1)
	}
}

func TestCheckRedirect_SixthRedirectChainRejected(t *testing.T) {
	ts, hops := redirectChainServer(maxRedirects + 1)
	defer ts.Close()

	_, err := newRedirectCountClient(t, ts).FetchProduct(context.Background(), "B0FX3X569X")
	if !errors.Is(err, ErrTooManyRedirects) {
		t.Fatalf("err = %v, want ErrTooManyRedirects on 6th redirect", err)
	}
	if got := atomic.LoadInt32(hops); got != maxRedirects+1 {
		t.Errorf("server hops = %d, want %d (6 redirect responses, final 200 never fetched)", got, maxRedirects+1)
	}
}

func TestIsAmazonHost_AcceptsApexAndSubdomainRejectsSpoof(t *testing.T) {
	accept := []string{
		"www.amazon.co.jp",
		"amazon.co.jp",
		"a.amazon.co.jp",
		"sub.domain.amazon.co.jp",
	}
	for _, host := range accept {
		if !IsAmazonHost(host) {
			t.Errorf("IsAmazonHost(%q) = false, want true", host)
		}
	}
	reject := []string{
		"example.com",
		"amazon.co.jp.evil.example",
		"notamazon.co.jp",
		"amazon.com",
		"www.amazon.co.jp.evil.example",
		"",
	}
	for _, host := range reject {
		if IsAmazonHost(host) {
			t.Errorf("IsAmazonHost(%q) = true, want false", host)
		}
	}
}

// NewClient は本番用の依存(timeout・redirect検証)を組み立てた client を返す。
func TestNewClient_ReturnsConfiguredClient(t *testing.T) {
	c := NewClient()
	if c == nil {
		t.Fatal("NewClient = nil, want non-nil production client")
	}
}

// checkRedirect は相対redirect(host 空)を許容する。host が空のときは
// IsAmazonHost 判定へ進まず追随する（SPECIFICATION.md 11.1）。
func TestCheckRedirect_EmptyHostAllowed(t *testing.T) {
	req := &http.Request{URL: &url.URL{Host: "", Path: "/dp/B0FX3X569X"}}
	if err := checkRedirect(req, nil); err != nil {
		t.Errorf("checkRedirect empty host err = %v, want nil", err)
	}
}

// 商品ページ取得でTCP/DNSレベルの通信失敗が起きた場合は応答なしのerrorとして返す
// （SPECIFICATION.md 11.3 timeout、DNS、接続失敗=retryable。HTTP client内で再試行しない）。
func TestFetchProduct_ConnectionFailure(t *testing.T) {
	c := newClientWithHTTPClient("http://127.0.0.1:1", &http.Client{
		Timeout:       2 * time.Second,
		CheckRedirect: checkRedirect,
	})
	_, err := c.FetchProduct(context.Background(), "B0FX3X569X")
	if err == nil {
		t.Fatal("connection failure must return error")
	}
}

// response body の読込失敗も応答ありのerrorとして返す（SPECIFICATION.md 11.3）。
// 応答は Amazon 由来とは限らないため本文は parse せず error を上位へ伝播する。
type readErrBody struct{}

func (readErrBody) Read(_ []byte) (int, error) {
	return 0, errors.New("simulated read error")
}

type readErrRT struct{}

func (readErrRT) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(readErrBody{}),
		Request:    req,
		Header:     make(http.Header),
	}, nil
}

func TestFetchProduct_BodyReadError(t *testing.T) {
	c := newClientWithHTTPClient(amazonBase, &http.Client{
		Timeout:       5 * time.Second,
		CheckRedirect: checkRedirect,
		Transport:     readErrRT{},
	})
	_, err := c.FetchProduct(context.Background(), "B0FX3X569X")
	if err == nil {
		t.Fatal("body read error must return error")
	}
}

// FetchSearch の body 超過は応答ありの retryable として計測値を保持する（SPECIFICATION.md 11.3, 18.1）。
func TestFetchSearch_BodyTooLarge(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		big := make([]byte, maxBodyBytes+1)
		for i := range big {
			big[i] = 'a'
		}
		_, _ = w.Write(big)
	}))
	defer ts.Close()

	result, err := newTestClient(t, ts, 30*time.Second).FetchSearch(context.Background(), "海李")
	if err != nil {
		t.Fatalf("FetchSearch: %v", err)
	}
	if result.Category != CategoryRetryable {
		t.Errorf("Category = %v, want Retryable", result.Category)
	}
	if result.HTTPStatus != 200 {
		t.Errorf("HTTPStatus = %d, want 200 (応答あり)", result.HTTPStatus)
	}
	if result.ResponseBytes <= int(maxBodyBytes) {
		t.Errorf("ResponseBytes = %d, want > %d", result.ResponseBytes, maxBodyBytes)
	}
}

// FetchSearch の CAPTCHA 本文は retryable に分類する（SPECIFICATION.md 11.3, 22.2）。
func TestFetchSearch_CaptchaIsRetryable(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html><body>画像に表示されている文字を入力してください</body></html>"))
	}))
	defer ts.Close()

	result, err := newTestClient(t, ts, 5*time.Second).FetchSearch(context.Background(), "海李")
	if err != nil {
		t.Fatalf("FetchSearch: %v", err)
	}
	if result.Category != CategoryRetryable {
		t.Errorf("CAPTCHA search page must be retryable, got %v", result.Category)
	}
}

// FetchSearch でもTCP/DNSレベルの通信失敗は応答なしのerrorとして返す（SPECIFICATION.md 11.3）。
func TestFetchSearch_ConnectionFailure(t *testing.T) {
	c := newClientWithHTTPClient("http://127.0.0.1:1", &http.Client{
		Timeout:       2 * time.Second,
		CheckRedirect: checkRedirect,
	})
	_, err := c.FetchSearch(context.Background(), "海李")
	if err == nil {
		t.Fatal("connection failure must return error")
	}
}

// baseURL が不正でリクエストを構築できない場合はerrorとして返す（設定異常）。
func TestFetchProduct_MalformedBaseURL(t *testing.T) {
	c := newClientWithHTTPClient("http://[::1zzz", &http.Client{
		Timeout:       5 * time.Second,
		CheckRedirect: checkRedirect,
	})
	_, err := c.FetchProduct(context.Background(), "B0FX3X569X")
	if err == nil {
		t.Fatal("malformed base URL must return error")
	}
}

// FetchSearch の HTTP status 分類（SPECIFICATION.md 11.3）。
func TestFetchSearch_StatusClassification(t *testing.T) {
	tests := []struct {
		name   string
		status int
		want   Category
	}{
		{name: "403 Retryable", status: http.StatusForbidden, want: CategoryRetryable},
		{name: "500 Retryable", status: http.StatusInternalServerError, want: CategoryRetryable},
		{name: "400 PermanentClientError", status: http.StatusBadRequest, want: CategoryPermanentClientError},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
			}))
			defer ts.Close()

			result, err := newTestClient(t, ts, 5*time.Second).FetchSearch(context.Background(), "海李")
			if err != nil {
				t.Fatalf("FetchSearch: %v", err)
			}
			if result.Category != tc.want {
				t.Errorf("status %d: Category = %v, want %v", tc.status, result.Category, tc.want)
			}
		})
	}
}
