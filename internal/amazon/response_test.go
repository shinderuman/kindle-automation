package amazon

import "testing"

func TestClassifyHTTPStatus(t *testing.T) {
	tests := []struct {
		name   string
		status int
		want   Category
	}{
		{name: "200はOK", status: 200, want: CategoryOK},
		{name: "404はNotFound", status: 404, want: CategoryNotFound},
		{name: "403はRetryable", status: 403, want: CategoryRetryable},
		{name: "429はRetryable", status: 429, want: CategoryRetryable},
		{name: "400はPermanentClientError", status: 400, want: CategoryPermanentClientError},
		{name: "410はPermanentClientError", status: 410, want: CategoryPermanentClientError},
		{name: "500はRetryable", status: 500, want: CategoryRetryable},
		{name: "503はRetryable", status: 503, want: CategoryRetryable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyHTTPStatus(tc.status); got != tc.want {
				t.Fatalf("ClassifyHTTPStatus(%d) = %v, want %v", tc.status, got, tc.want)
			}
		})
	}
}

func TestIsBlockedPage(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{name: "CAPTCHA文言を含むときtrue", text: "画像に表示されている文字を入力してください", want: true},
		{name: "通常ページはfalse", text: "商品のタイトルと価格", want: false},
		{name: "空文字はfalse", text: "", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsBlockedPage(tc.text); got != tc.want {
				t.Fatalf("IsBlockedPage(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}
