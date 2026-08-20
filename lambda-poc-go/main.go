package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/aws/aws-lambda-go/lambda"
)

const (
	amazonURL = "https://www.amazon.co.jp/dp/"
	userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0 Safari/537.36"
)

type Request struct {
	ASIN string `json:"asin"`
}

type Response struct {
	ASIN        string `json:"asin"`
	Status      int    `json:"status"`
	Title       string `json:"title,omitempty"`
	KindlePrice string `json:"kindlePrice,omitempty"`
	PaperPrice  string `json:"paperPrice,omitempty"`
	Points      string `json:"points,omitempty"`
	Coupon      string `json:"coupon,omitempty"`
	ValidPage   bool   `json:"validPage"`
	DurationMs  int64  `json:"durationMs"`
}

func main() {
	lambda.Start(handler)
}

func handler(ctx context.Context, request Request) (Response, error) {
	startedAt := time.Now()
	if strings.TrimSpace(request.ASIN) == "" {
		return Response{}, fmt.Errorf("asin is required")
	}
	response, err := fetchProduct(ctx, request.ASIN)
	if err != nil {
		return Response{}, err
	}
	response.DurationMs = time.Since(startedAt).Milliseconds()
	return response, nil
}

func fetchProduct(ctx context.Context, asin string) (Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, amazonURL+asin, nil)
	if err != nil {
		return Response{}, fmt.Errorf("create Amazon request: %w", err)
	}
	request.Header.Set("User-Agent", userAgent)
	request.Header.Set("Accept-Language", "ja-JP,ja;q=0.9")
	client := &http.Client{Timeout: 30 * time.Second}
	httpResponse, err := client.Do(request)
	if err != nil {
		return Response{}, fmt.Errorf("fetch Amazon page: %w", err)
	}
	defer httpResponse.Body.Close()
	return parseProduct(asin, httpResponse)
}

func parseProduct(asin string, httpResponse *http.Response) (Response, error) {
	document, err := goquery.NewDocumentFromReader(httpResponse.Body)
	if err != nil {
		return Response{}, fmt.Errorf("parse Amazon HTML: %w", err)
	}
	result := Response{ASIN: asin, Status: httpResponse.StatusCode}
	result.Title = strings.TrimSpace(document.Find("#productTitle").First().Text())
	result.KindlePrice = findText(document, "#tmm-grid-swatch-KINDLE .slot-price > span, #kindle-price")
	result.PaperPrice = findText(document, "[id^='tmm-grid-swatch']:not([id$='KINDLE']) .slot-price > span")
	result.Points = findText(document, "#tmm-grid-swatch-KINDLE .slot-buyingPoints > span, #tmm-grid-swatch-OTHER .slot-buyingPoints > span")
	result.Coupon = findText(document, ".couponLabelText")
	result.ValidPage = result.Title != "" && document.Find("#productTitle").Length() > 0
	return result, nil
}

func findText(document *goquery.Document, selector string) string {
	return strings.TrimSpace(document.Find(selector).First().Text())
}
