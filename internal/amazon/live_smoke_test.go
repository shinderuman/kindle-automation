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
// 安定した構造だけを検証する（USER_REQUEST）。Amazon 側の block/CAPTCHA が起きた場合は
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

// TestLiveSmoke_Product_B0FX3X569X は固定例のKindle商品ページを取得し、安定構造を検証する。
// 価格は正値を要求するが、ポイントの値は固定 assert しない（USER_REQUEST）。
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
	// ポイント・クーポンは変動するため値・存在を固定 assert しない。観測値をログへ残すだけ。
	t.Logf("B0FX3X569X 構造OK: title=%q kindlePrice=%.0f points=%d coupon=%v couponText=%q "+
		"hasKindleSwatch=%v httpStatus=%d bytes=%d",
		info.Title, info.CurrentPrice.Yen(), info.Points, info.Coupon, info.CouponText,
		info.HasKindleSwatch, result.HTTPStatus, result.ResponseBytes)
}

// TestLiveSmoke_Product_B0CX8CD1XL_CouponObserve はクーポン解析の観測例を取得する。
// クーポン不在でも失敗にしない（USER_REQUEST）。通信成功と要求ASINだけを検証する。
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

// TestLiveSmoke_Search_KindleMarker は検索ページを取得し、検索containerとKindle形式markerを確認する。
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
