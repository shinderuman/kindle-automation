package checkworker

import (
	"context"

	"github.com/shinderuman/kindle-automation/internal/amazon"
	"github.com/shinderuman/kindle-automation/internal/application/newrelease"
	"github.com/shinderuman/kindle-automation/internal/application/papertokindle"
	"github.com/shinderuman/kindle-automation/internal/application/sale"
)

// saleFetcher は amazon.Client を sale.ProductFetcher へ適合させる。
type saleFetcher struct {
	client *amazon.Client
}

// FetchProduct は商品ページを1回取得し sale 用結果へ変換する。1起動で最大1回呼ぶ。
func (f saleFetcher) FetchProduct(ctx context.Context, asin string) (sale.FetchResult, error) {
	r, err := f.client.FetchProduct(ctx, asin)
	if err != nil {
		return sale.FetchResult{}, err
	}
	return toSaleResult(r), nil
}

// newReleaseFetcher は amazon.Client を新刊の SearchFetcher と ProductFetcher の両方へ適合させる。
// partnerTag は候補・商品の保存用 URL 構築に使う。
type newReleaseFetcher struct {
	client      *amazon.Client
	partnerTag  string
	productCall *int // テスト用呼出回数カウンタ（nil 可）
	searchCall  *int // テスト用呼出回数カウンタ（nil 可）
}

// FetchSearch は検索ページを1回取得し新刊用結果へ変換する。
func (f newReleaseFetcher) FetchSearch(ctx context.Context, author string) (newrelease.SearchResult, error) {
	if f.searchCall != nil {
		*f.searchCall++
	}
	r, err := f.client.FetchSearch(ctx, author)
	if err != nil {
		return newrelease.SearchResult{}, err
	}
	return toNewReleaseSearchResult(r, f.partnerTag), nil
}

// FetchProduct は商品ページを1回取得し新刊 detail 用結果へ変換する。
func (f newReleaseFetcher) FetchProduct(ctx context.Context, asin string) (newrelease.ProductResult, error) {
	if f.productCall != nil {
		*f.productCall++
	}
	r, err := f.client.FetchProduct(ctx, asin)
	if err != nil {
		return newrelease.ProductResult{}, err
	}
	return toNewReleaseProductResult(r, f.partnerTag), nil
}

// paperPageFetcher は amazon.Client を papertokindle.PaperPageFetcher へ適合させる。
type paperPageFetcher struct {
	client *amazon.Client
}

// FetchPaperPage は紙書籍ページを1回取得し紙→Kindle check 用結果へ変換する。
func (f paperPageFetcher) FetchPaperPage(ctx context.Context, paperASIN string) (papertokindle.PaperCheckResult, error) {
	r, err := f.client.FetchProduct(ctx, paperASIN)
	if err != nil {
		return papertokindle.PaperCheckResult{}, err
	}
	return toPaperCheckResult(r), nil
}

// kindlePageFetcher は amazon.Client を papertokindle.KindlePageFetcher へ適合させる。
type kindlePageFetcher struct {
	client *amazon.Client
}

// FetchKindlePage は Kindle 商品ページを1回取得し紙→Kindle detail 用結果へ変換する。
func (f kindlePageFetcher) FetchKindlePage(ctx context.Context, kindleASIN string) (papertokindle.KindleDetailResult, error) {
	r, err := f.client.FetchProduct(ctx, kindleASIN)
	if err != nil {
		return papertokindle.KindleDetailResult{}, err
	}
	return toKindleDetailResult(r), nil
}
