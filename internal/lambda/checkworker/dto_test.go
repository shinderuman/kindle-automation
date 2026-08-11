package checkworker

import (
	"testing"
	"time"

	"github.com/shinderuman/kindle-automation/internal/amazon"
	"github.com/shinderuman/kindle-automation/internal/domain/book"
)

func releaseDay() time.Time {
	return time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC)
}

func sampleAmazonInfo() amazon.ProductInfo {
	return amazon.ProductInfo{
		Title:            "テスト書籍",
		ASIN:             "B0FX3X569X",
		CurrentPrice:     book.NewPrice(759),
		Points:           38,
		Coupon:           true,
		CouponText:       "500円OFF",
		PaperPrice:       book.NewPrice(792),
		ReleaseDate:      releaseDay(),
		HasReleaseDate:   true,
		HasKindleSwatch:  true,
		HasPaperSwatch:   true,
		Contributors:     []string{"上原誠", "やきいもほくほく"},
		KindleSwatchASIN: "B0FX3X569X",
	}
}

func TestToSaleResult_MapsFieldsAndCategory(t *testing.T) {
	got := toSaleResult(amazon.FetchResult{Category: amazon.CategoryOK, Info: sampleAmazonInfo()})
	if got.Category != 0 { // sale.CategoryOK == 0
		t.Errorf("Category = %d, want 0 (OK)", got.Category)
	}
	if got.Info.ASIN != "B0FX3X569X" {
		t.Errorf("ASIN = %q", got.Info.ASIN)
	}
	if got.Info.Points != 38 || !got.Info.Coupon || got.Info.CouponText != "500円OFF" {
		t.Errorf("sale info not mapped: %+v", got.Info)
	}
	if !got.Info.HasKindleSwatch {
		t.Errorf("HasKindleSwatch lost")
	}
}

func TestMapSaleCategory(t *testing.T) {
	cases := []struct {
		in   amazon.Category
		want int
	}{
		{amazon.CategoryOK, 0},
		{amazon.CategoryNotFound, 1},
		{amazon.CategoryPermanentClientError, 2},
		{amazon.CategoryRetryable, 3},
	}
	for _, tc := range cases {
		if got := int(mapSaleCategory(tc.in)); got != tc.want {
			t.Errorf("mapSaleCategory(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestToNewReleaseProductResult_BuildsPartnerTagURL(t *testing.T) {
	got := toNewReleaseProductResult(
		amazon.FetchResult{Category: amazon.CategoryOK, Info: sampleAmazonInfo()},
		"kindlebot-22",
	)
	want := "https://www.amazon.co.jp/dp/B0FX3X569X?tag=kindlebot-22"
	if got.Info.URL != want {
		t.Errorf("URL = %q, want %q", got.Info.URL, want)
	}
	if len(got.Info.Contributors) != 2 || got.Info.Contributors[0] != "上原誠" || got.Info.Contributors[1] != "やきいもほくほく" {
		t.Errorf("Contributors = %v, want [上原誠 やきいもほくほく]", got.Info.Contributors)
	}
	if !got.Info.HasReleaseDate || !got.Info.ReleaseDate.Equal(releaseDay()) {
		t.Errorf("ReleaseDate not mapped: %+v", got.Info)
	}
}

func TestToNewReleaseProductResult_EmptyPartnerTagOmitsTag(t *testing.T) {
	got := toNewReleaseProductResult(
		amazon.FetchResult{Category: amazon.CategoryOK, Info: sampleAmazonInfo()},
		"",
	)
	if want := "https://www.amazon.co.jp/dp/B0FX3X569X"; got.Info.URL != want {
		t.Errorf("URL = %q, want %q (no tag)", got.Info.URL, want)
	}
}

func TestToNewReleaseSearchResult_PropagatesIsKindleAndTaggedURL(t *testing.T) {
	r := amazon.SearchResult{
		Category: amazon.CategoryOK,
		Hits: []amazon.SearchHit{
			{ASIN: "B0FX3X569X", Title: "A", Price: book.NewPrice(759), ReleaseDate: releaseDay(), HasReleaseDate: true, Contributors: []string{"著者A"}, IsKindle: true},
			{ASIN: "B0FX3X569Y", Title: "B", Contributors: []string{"著者B"}, IsKindle: false},
		},
	}
	got := toNewReleaseSearchResult(r, "kindlebot-22")
	if len(got.Hits) != 2 {
		t.Fatalf("len(hits) = %d, want 2", len(got.Hits))
	}
	wantKindle := []bool{true, false}
	for i, h := range got.Hits {
		if h.IsKindle != wantKindle[i] {
			t.Errorf("hit[%d] IsKindle = %v, want %v (形式表示をそのまま伝播)", i, h.IsKindle, wantKindle[i])
		}
		if got := h.URL; got != "https://www.amazon.co.jp/dp/"+h.ASIN+"?tag=kindlebot-22" {
			t.Errorf("URL = %q for %q", got, h.ASIN)
		}
	}
	if !got.Hits[0].HasReleaseDate || !got.Hits[0].ReleaseDate.Equal(releaseDay()) {
		t.Errorf("ReleaseDate not mapped on first hit: %+v", got.Hits[0])
	}
	if len(got.Hits[0].Contributors) != 1 || got.Hits[0].Contributors[0] != "著者A" {
		t.Errorf("hit[0] Contributors not mapped: %v", got.Hits[0].Contributors)
	}
}

func TestMapNRSearchCategory(t *testing.T) {
	if mapNRSearchCategory(amazon.CategoryOK) != 0 { // SearchOK
		t.Errorf("OK should map to SearchOK")
	}
	if got := mapNRSearchCategory(amazon.CategorySearchEmpty); got != 1 { // SearchEmpty
		t.Errorf("CategorySearchEmpty should map to SearchEmpty(1), got %d", got)
	}
	for _, c := range []amazon.Category{amazon.CategoryNotFound, amazon.CategoryPermanentClientError, amazon.CategoryRetryable} {
		if mapNRSearchCategory(c) != 2 { // SearchRetryable
			t.Errorf("category %d should map to SearchRetryable", c)
		}
	}
}

func TestToPaperCheckResult_CarriesKindleSwatchASIN(t *testing.T) {
	got := toPaperCheckResult(amazon.FetchResult{Category: amazon.CategoryOK, Info: sampleAmazonInfo()})
	if got.Info.KindleSwatchASIN != "B0FX3X569X" {
		t.Errorf("KindleSwatchASIN = %q", got.Info.KindleSwatchASIN)
	}
	if !got.Info.HasPaperSwatch || !got.Info.HasKindleSwatch {
		t.Errorf("swatch flags lost: %+v", got.Info)
	}
	if !got.Info.PaperPrice.Valid() || got.Info.PaperPrice.Yen() != 792 {
		t.Errorf("PaperPrice not mapped: %+v", got.Info.PaperPrice)
	}
}

func TestToKindleDetailResult_MapsDetailFields(t *testing.T) {
	got := toKindleDetailResult(amazon.FetchResult{Category: amazon.CategoryNotFound, Info: sampleAmazonInfo()})
	if got.Category != 1 { // papertokindle.CategoryNotFound
		t.Errorf("Category = %d, want 1 (NotFound)", got.Category)
	}
	if !got.Info.CurrentPrice.Valid() || got.Info.CurrentPrice.Yen() != 759 {
		t.Errorf("CurrentPrice not mapped: %+v", got.Info.CurrentPrice)
	}
	if !got.Info.HasReleaseDate || !got.Info.ReleaseDate.Equal(releaseDay()) {
		t.Errorf("ReleaseDate not mapped: %+v", got.Info)
	}
}
