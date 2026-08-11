package checkworker

import (
	"github.com/shinderuman/kindle-automation/internal/amazon"
	"github.com/shinderuman/kindle-automation/internal/application/newrelease"
	"github.com/shinderuman/kindle-automation/internal/application/papertokindle"
	"github.com/shinderuman/kindle-automation/internal/application/sale"
)

// toSaleResult は HTTP計測値（SPECIFICATION.md 18.1）も伝播する。
func toSaleResult(r amazon.FetchResult) sale.FetchResult {
	return sale.FetchResult{
		Category: mapSaleCategory(r.Category),
		Info: sale.ProductInfo{
			ASIN:            r.Info.ASIN,
			Title:           r.Info.Title,
			CurrentPrice:    r.Info.CurrentPrice,
			Points:          r.Info.Points,
			Coupon:          r.Info.Coupon,
			CouponText:      r.Info.CouponText,
			HasKindleSwatch: r.Info.HasKindleSwatch,
		},
		HTTPStatus:    r.HTTPStatus,
		ResponseBytes: r.ResponseBytes,
	}
}

func mapSaleCategory(c amazon.Category) sale.Category {
	switch c {
	case amazon.CategoryOK:
		return sale.CategoryOK
	case amazon.CategoryNotFound:
		return sale.CategoryNotFound
	case amazon.CategoryPermanentClientError:
		return sale.CategoryPermanentClientError
	case amazon.CategoryRetryable:
		return sale.CategoryRetryable
	default:
		return sale.CategoryRetryable
	}
}

// toNewReleaseProductResult は Affiliate Tag 付きの保存用 URL を構築する（SPECIFICATION.md 9.2）。
func toNewReleaseProductResult(r amazon.FetchResult, partnerTag string) newrelease.ProductResult {
	info := r.Info
	return newrelease.ProductResult{
		Category: mapNRProductCategory(r.Category),
		Info: newrelease.ProductInfo{
			ASIN:            info.ASIN,
			Title:           info.Title,
			URL:             amazon.ProductURL(info.ASIN, partnerTag),
			CurrentPrice:    info.CurrentPrice,
			ReleaseDate:     info.ReleaseDate,
			HasReleaseDate:  info.HasReleaseDate,
			HasKindleSwatch: info.HasKindleSwatch,
			Contributors:    info.Contributors,
		},
		HTTPStatus:    r.HTTPStatus,
		ResponseBytes: r.ResponseBytes,
	}
}

func mapNRProductCategory(c amazon.Category) newrelease.ProductCategory {
	switch c {
	case amazon.CategoryOK:
		return newrelease.ProductOK
	case amazon.CategoryNotFound:
		return newrelease.ProductNotFound
	case amazon.CategoryPermanentClientError:
		return newrelease.ProductPermanentClientError
	case amazon.CategoryRetryable:
		return newrelease.ProductRetryable
	default:
		return newrelease.ProductRetryable
	}
}

// toNewReleaseSearchResult は IsKindle を形式表示でKindle版と確認できた候補だけ真とし（SPECIFICATION.md 13.4）、
// 保存用 URL を構築する。
func toNewReleaseSearchResult(r amazon.SearchResult, partnerTag string) newrelease.SearchResult {
	hits := make([]newrelease.SearchHit, 0, len(r.Hits))
	for _, h := range r.Hits {
		hits = append(hits, newrelease.SearchHit{
			ASIN:           h.ASIN,
			Title:          h.Title,
			URL:            amazon.ProductURL(h.ASIN, partnerTag),
			KindlePrice:    h.Price,
			ReleaseDate:    h.ReleaseDate,
			HasReleaseDate: h.HasReleaseDate,
			Contributors:   h.Contributors,
			IsKindle:       h.IsKindle,
		})
	}
	return newrelease.SearchResult{
		Category:      mapNRSearchCategory(r.Category),
		Hits:          hits,
		HTTPStatus:    r.HTTPStatus,
		ResponseBytes: r.ResponseBytes,
	}
}

// mapNRSearchCategory は CategorySearchEmpty（検索0件）を search_empty へ写像し、呼び出し側で retryable outcome にする。
func mapNRSearchCategory(c amazon.Category) newrelease.SearchCategory {
	switch c {
	case amazon.CategoryOK:
		return newrelease.SearchOK
	case amazon.CategorySearchEmpty:
		return newrelease.SearchEmpty
	default:
		return newrelease.SearchRetryable
	}
}

func toPaperCheckResult(r amazon.FetchResult) papertokindle.PaperCheckResult {
	info := r.Info
	return papertokindle.PaperCheckResult{
		Category: mapPaperCategory(r.Category),
		Info: papertokindle.PaperPageInfo{
			ASIN:             info.ASIN,
			Title:            info.Title,
			PaperPrice:       info.PaperPrice,
			HasPaperSwatch:   info.HasPaperSwatch,
			HasKindleSwatch:  info.HasKindleSwatch,
			KindleSwatchASIN: info.KindleSwatchASIN,
		},
		HTTPStatus:    r.HTTPStatus,
		ResponseBytes: r.ResponseBytes,
	}
}

func toKindleDetailResult(r amazon.FetchResult) papertokindle.KindleDetailResult {
	info := r.Info
	return papertokindle.KindleDetailResult{
		Category: mapPaperCategory(r.Category),
		Info: papertokindle.KindlePageInfo{
			ASIN:            info.ASIN,
			Title:           info.Title,
			CurrentPrice:    info.CurrentPrice,
			ReleaseDate:     info.ReleaseDate,
			HasReleaseDate:  info.HasReleaseDate,
			HasKindleSwatch: info.HasKindleSwatch,
		},
		HTTPStatus:    r.HTTPStatus,
		ResponseBytes: r.ResponseBytes,
	}
}

func mapPaperCategory(c amazon.Category) papertokindle.Category {
	switch c {
	case amazon.CategoryOK:
		return papertokindle.CategoryOK
	case amazon.CategoryNotFound:
		return papertokindle.CategoryNotFound
	case amazon.CategoryPermanentClientError:
		return papertokindle.CategoryPermanentClientError
	case amazon.CategoryRetryable:
		return papertokindle.CategoryRetryable
	default:
		return papertokindle.CategoryRetryable
	}
}
