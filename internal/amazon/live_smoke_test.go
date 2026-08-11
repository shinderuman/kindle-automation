//go:build livesmoke

// このファイルは明示 opt-in のローカル live smoke である。
// ビルドタグ livesmoke を付けたときだけコンパイルされ、通常の `go test ./...` からは
// 完全に除外される（SPECIFICATION.md 22、AGENTS.md 11「自動テストから実AWS、Amazonへアクセスしない」）。
//
// 実行方法:
//
//	go test -tags=livesmoke -run 'TestLiveSmoke' ./internal/amazon/
//
// production の internal/amazon 実装(NewClient)をそのまま通し、実Amazon Japan へ接続する。
// 第三の本番Lambda entrypoint は追加しない（テストコードであり cmd/main ではない）。
//
// 検証方針: 変動する値（クーポン・ポイントの有無・値・還元率、価格の絶対値）を固定 assert せず、
// 通信成功・CAPTCHA/access denied でない・要求ASIN一致・タイトル取得・Kindle価格が正値、という
// 安定した構造だけを検証する。Amazon 側の block/CAPTCHA が起きた場合は
// 分類結果と取得できなかった事実を報告し、要件を弱めて通過させない。
package amazon

import (
	"context"
	"testing"
	"time"
)

// liveTimeout は live smoke 用の余裕を持った timeout。本番の15秒より長くはしない。
const liveTimeout = 15 * time.Second

// liveSmokeClient は本番 NewClient をそのまま使う。
func liveSmokeClient() *Client {
	return NewClient()
}

func TestLiveSmoke_Product_B0FX3X569X(t *testing.T) {
	const asin = "B0FX3X569X"
	ctx, cancel := context.WithTimeout(context.Background(), liveTimeout)
	defer cancel()

	result, err := liveSmokeClient().FetchProduct(ctx, asin)
	if err != nil {
		t.Fatalf("FetchProduct %s returned transport error (block/timeout/通信失敗): %v", asin, err)
	}
	if result.Category != CategoryOK {
		t.Fatalf("FetchProduct %s Category=%s (HTTPStatus=%d bytes=%d): "+
			"期待はOK(通信成功・CAPTCHA/access deniedでない)。Amazon側block/CAPTCHA/短い本文/要素欠落の可能性",
			asin, categoryName(result.Category), result.HTTPStatus, result.ResponseBytes)
	}
	info := result.Info
	if info.ASIN != asin {
		t.Errorf("要求ASIN不一致: Info.ASIN=%q, want %s", info.ASIN, asin)
	}
	if info.Title == "" {
		t.Errorf("Title が取得できない(必須構造 #productTitle 欠落)")
	}
	if !info.CurrentPrice.Valid() || info.CurrentPrice.Yen() <= 0 {
		t.Errorf("Kindle価格が取得できないか0円: CurrentPrice=%+v (0円として保存してはならない)", info.CurrentPrice)
	}
	if !info.HasKindleSwatch {
		t.Errorf("HasKindleSwatch = false, want true (新刊detail でKindle版スウォッチが必須)")
	}
	if !info.HasReleaseDate || info.ReleaseDate.IsZero() {
		t.Errorf("HasReleaseDate=%v ReleaseDate=%v, want 発売日取得済みかつ非zero (新刊detail 必須)",
			info.HasReleaseDate, info.ReleaseDate)
	}
	if len(info.Contributors) == 0 {
		t.Errorf("Contributors が空 (新刊detail で #bylineInfo a のcontributorが必須)")
	}
	t.Logf("B0FX3X569X 構造OK: title=%q kindlePrice=%.0f points=%d coupon=%v couponText=%q "+
		"hasKindleSwatch=%v hasReleaseDate=%v releaseDate=%v contributors=%d httpStatus=%d bytes=%d",
		info.Title, info.CurrentPrice.Yen(), info.Points, info.Coupon, info.CouponText,
		info.HasKindleSwatch, info.HasReleaseDate, info.ReleaseDate, len(info.Contributors),
		result.HTTPStatus, result.ResponseBytes)
}

// fixture(testdata/amazon/paper_4434361325.html)由来の ASIN を実HTTPで確認する。
// 商品が使えない（block/状態変化等）場合は事実を報告し、根拠なく別ASINへ差し替えない。
func TestLiveSmoke_Product_4434361325_PaperToKindle(t *testing.T) {
	const paperASIN = "4434361325"
	ctx, cancel := context.WithTimeout(context.Background(), liveTimeout)
	defer cancel()

	result, err := liveSmokeClient().FetchProduct(ctx, paperASIN)
	if err != nil {
		t.Fatalf("FetchProduct %s returned transport error (block/timeout/通信失敗): %v", paperASIN, err)
	}
	if result.Category != CategoryOK {
		t.Fatalf("FetchProduct %s Category=%s (HTTPStatus=%d bytes=%d): block/CAPTCHA/短い本文/要素欠落の可能性",
			paperASIN, categoryName(result.Category), result.HTTPStatus, result.ResponseBytes)
	}
	info := result.Info
	if info.ASIN != paperASIN {
		t.Errorf("要求紙ASIN不一致: Info.ASIN=%q, want %s", info.ASIN, paperASIN)
	}
	if info.Title == "" {
		t.Errorf("Title が取得できない(必須構造 #productTitle 欠落)")
	}
	if !info.HasPaperSwatch {
		t.Errorf("HasPaperSwatch = false, want true (実紙商品ページで紙スウォッチが必須)")
	}
	if !info.HasKindleSwatch {
		t.Errorf("HasKindleSwatch = false, want true (Paper→Kindle候補のKindle版スウォッチが必須)")
	}
	if info.KindleSwatchASIN == "" {
		t.Errorf("KindleSwatchASIN が空 (Paper→KindleでKindle版ASINが必須)")
	}
	if info.KindleSwatchASIN == paperASIN {
		t.Errorf("KindleSwatchASIN=%q が要求紙ASIN %s と一致(不同であること)", info.KindleSwatchASIN, paperASIN)
	}
	t.Logf("4434361325 構造OK: title=%q hasPaperSwatch=%v hasKindleSwatch=%v kindleSwatchASIN=%q "+
		"httpStatus=%d bytes=%d",
		info.Title, info.HasPaperSwatch, info.HasKindleSwatch, info.KindleSwatchASIN,
		result.HTTPStatus, result.ResponseBytes)
}

// クーポン不在でも失敗にしない（観測目的）。通信成功と要求ASINだけを検証する。
func TestLiveSmoke_Product_B0CX8CD1XL_CouponObserve(t *testing.T) {
	const asin = "B0CX8CD1XL"
	ctx, cancel := context.WithTimeout(context.Background(), liveTimeout)
	defer cancel()

	result, err := liveSmokeClient().FetchProduct(ctx, asin)
	if err != nil {
		t.Fatalf("FetchProduct %s returned transport error (block/timeout/通信失敗): %v", asin, err)
	}
	if result.Category != CategoryOK {
		t.Fatalf("FetchProduct %s Category=%s (HTTPStatus=%d bytes=%d): block/CAPTCHA/短い本文/要素欠落の可能性",
			asin, categoryName(result.Category), result.HTTPStatus, result.ResponseBytes)
	}
	if result.Info.ASIN != asin {
		t.Errorf("要求ASIN不一致: Info.ASIN=%q, want %s", result.Info.ASIN, asin)
	}
	// クーポンの存在・文言・値は変動するため固定 assert しない。観測だけ残す。
	t.Logf("B0CX8CD1XL 観測: coupon=%v couponText=%q title=%q httpStatus=%d bytes=%d",
		result.Info.Coupon, result.Info.CouponText, result.Info.Title, result.HTTPStatus, result.ResponseBytes)
}

func TestLiveSmoke_Search_KindleMarker(t *testing.T) {
	const author = "海李"
	ctx, cancel := context.WithTimeout(context.Background(), liveTimeout)
	defer cancel()

	result, err := liveSmokeClient().FetchSearch(ctx, author)
	if err != nil {
		t.Fatalf("FetchSearch %q returned transport error (block/timeout/通信失敗): %v", author, err)
	}
	if result.Category != CategoryOK {
		t.Fatalf("FetchSearch %q Category=%s (HTTPStatus=%d bytes=%d): block/CAPTCHA/空結果/要素欠落の可能性",
			author, categoryName(result.Category), result.HTTPStatus, result.ResponseBytes)
	}
	if len(result.Hits) == 0 {
		t.Fatalf("検索結果containerが空(hits=0)。検索ページ構造変化または結果0件の可能性")
	}
	kindleCount := 0
	for i, h := range result.Hits {
		if h.IsKindle {
			kindleCount++
		}
		t.Logf("  hit[%d] asin=%s isKindle=%v title=%q", i, h.ASIN, h.IsKindle, h.Title)
	}
	if kindleCount == 0 {
		t.Errorf("Kindle形式marker(Kindle版)の候補が1件もない。検索カードの形式表示セレクタ破綻の可能性")
	}
	t.Logf("検索 %q 構造OK: hits=%d kindle=%d httpStatus=%d bytes=%d",
		author, len(result.Hits), kindleCount, result.HTTPStatus, result.ResponseBytes)
}

// categoryName は分類を文字列へ。Category の String 実装がないため smoke 用に手元で名前付けする。
func categoryName(c Category) string {
	switch c {
	case CategoryOK:
		return "OK"
	case CategoryNotFound:
		return "NotFound"
	case CategoryPermanentClientError:
		return "PermanentClientError"
	case CategoryRetryable:
		return "Retryable"
	case CategorySearchEmpty:
		return "SearchEmpty"
	default:
		return "Unknown"
	}
}
