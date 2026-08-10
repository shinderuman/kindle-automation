package book

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestNewPrice(t *testing.T) {
	tests := []struct {
		name string
		yen  float64
		want Price
	}{
		{name: "正の金額は取得済み扱い", yen: 759, want: Price{yen: 759, valid: true}},
		{name: "0円は未取得扱い", yen: 0, want: Price{}},
		{name: "負の金額は未取得扱い", yen: -1, want: Price{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := NewPrice(tc.yen)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("NewPrice(%v) = %+v, want %+v", tc.yen, got, tc.want)
			}
		})
	}
}

func TestUpdatePriceHistory(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	created := time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name    string
		old     KindleBook
		current Price
		want    KindleBook
	}{
		{
			name:    "新規レコードは今回価格をCurrentPriceとMaxPriceに設定しCreatedAtをnowにする",
			old:     KindleBook{},
			current: NewPrice(759),
			want:    KindleBook{CurrentPrice: NewPrice(759), MaxPrice: NewPrice(759), CreatedAt: now},
		},
		{
			name:    "old MaxPrice未取得のときは今回価格が初回基準になる",
			old:     KindleBook{CurrentPrice: NewPrice(700), MaxPrice: UnknownPrice(), CreatedAt: created},
			current: NewPrice(759),
			want:    KindleBook{CurrentPrice: NewPrice(759), MaxPrice: NewPrice(759), CreatedAt: created},
		},
		{
			name:    "今回価格が下がっても過去最高MaxPriceを保持する",
			old:     KindleBook{CurrentPrice: NewPrice(900), MaxPrice: NewPrice(900), CreatedAt: created},
			current: NewPrice(759),
			want:    KindleBook{CurrentPrice: NewPrice(759), MaxPrice: NewPrice(900), CreatedAt: created},
		},
		{
			name:    "old CurrentPriceがMaxPriceより高い場合はMaxPriceを更新する",
			old:     KindleBook{CurrentPrice: NewPrice(1200), MaxPrice: NewPrice(900), CreatedAt: created},
			current: NewPrice(1000),
			want:    KindleBook{CurrentPrice: NewPrice(1000), MaxPrice: NewPrice(1200), CreatedAt: created},
		},
		{
			name:    "今回価格が過去最高を超える場合はMaxPriceを今回価格へ更新する",
			old:     KindleBook{CurrentPrice: NewPrice(700), MaxPrice: NewPrice(800), CreatedAt: created},
			current: NewPrice(1000),
			want:    KindleBook{CurrentPrice: NewPrice(1000), MaxPrice: NewPrice(1000), CreatedAt: created},
		},
		{
			name:    "今回価格が未取得のときはMaxPriceを過去最高で維持する",
			old:     KindleBook{CurrentPrice: NewPrice(900), MaxPrice: NewPrice(900), CreatedAt: created},
			current: UnknownPrice(),
			want:    KindleBook{CurrentPrice: UnknownPrice(), MaxPrice: NewPrice(900), CreatedAt: created},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := UpdatePriceHistory(tc.old, tc.current, now)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("UpdatePriceHistory = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestDedupBooks(t *testing.T) {
	a := KindleBook{ASIN: "B000000001", Title: "A"}
	aDup := KindleBook{ASIN: "B000000001", Title: "A-dup"}
	b := KindleBook{ASIN: "B000000002", Title: "B"}
	empty := KindleBook{ASIN: "", Title: "Empty"}

	tests := []struct {
		name  string
		books []KindleBook
		want  []KindleBook
	}{
		{
			name:  "同じASINは最初の出現を優先する",
			books: []KindleBook{a, aDup, b},
			want:  []KindleBook{a, b},
		},
		{
			name:  "upcoming結合時にoriginal側が優先される順序を呼び出し側が保証する",
			books: []KindleBook{a, aDup},
			want:  []KindleBook{a},
		},
		{
			name:  "空ASINは重複排除の対象にできずそのまま残る",
			books: []KindleBook{empty, empty, a},
			want:  []KindleBook{empty, empty, a},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := DedupBooks(tc.books)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("DedupBooks = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestSortBooks(t *testing.T) {
	day1 := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name  string
		books []KindleBook
		want  []KindleBook
	}{
		{
			name: "発売日降順に並べる",
			books: []KindleBook{
				{ASIN: "B000000001", Title: "A", ReleaseDate: day1},
				{ASIN: "B000000002", Title: "B", ReleaseDate: day2},
			},
			want: []KindleBook{
				{ASIN: "B000000002", Title: "B", ReleaseDate: day2},
				{ASIN: "B000000001", Title: "A", ReleaseDate: day1},
			},
		},
		{
			name: "同日のときはタイトル昇順に並べる",
			books: []KindleBook{
				{ASIN: "B000000002", Title: "Z-title", ReleaseDate: day2},
				{ASIN: "B000000001", Title: "A-title", ReleaseDate: day2},
			},
			want: []KindleBook{
				{ASIN: "B000000001", Title: "A-title", ReleaseDate: day2},
				{ASIN: "B000000002", Title: "Z-title", ReleaseDate: day2},
			},
		},
		{
			name:  "入力が降順のときは順序を保ち入力スライスを変更しない",
			books: []KindleBook{{ASIN: "B000000002", Title: "B", ReleaseDate: day2}, {ASIN: "B000000001", Title: "A", ReleaseDate: day1}},
			want:  []KindleBook{{ASIN: "B000000002", Title: "B", ReleaseDate: day2}, {ASIN: "B000000001", Title: "A", ReleaseDate: day1}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			originalASINs := make([]string, len(tc.books))
			for i, b := range tc.books {
				originalASINs[i] = b.ASIN
			}
			got := SortBooks(tc.books)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("SortBooks = %+v, want %+v", got, tc.want)
			}
			gotOriginalASINs := make([]string, len(tc.books))
			for i, b := range tc.books {
				gotOriginalASINs[i] = b.ASIN
			}
			if !reflect.DeepEqual(originalASINs, gotOriginalASINs) {
				t.Fatalf("SortBooks changed input: %+v", tc.books)
			}
		})
	}
}

func TestDedupAuthorsAndSortAuthors(t *testing.T) {
	day1 := time.Date(2025, 12, 28, 0, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)

	authors := []Author{
		{Name: "AuthorB", LatestReleaseDate: day1},
		{Name: "AuthorA", LatestReleaseDate: day1},
		{Name: "AuthorC", LatestReleaseDate: day2},
		{Name: "AuthorA", LatestReleaseDate: day1}, // 重複
	}

	deduped := DedupAuthors(authors)
	wantDedup := []Author{
		{Name: "AuthorB", LatestReleaseDate: day1},
		{Name: "AuthorA", LatestReleaseDate: day1},
		{Name: "AuthorC", LatestReleaseDate: day2},
	}
	if !reflect.DeepEqual(deduped, wantDedup) {
		t.Fatalf("DedupAuthors = %+v, want %+v", deduped, wantDedup)
	}

	sorted := SortAuthors(deduped)
	wantSorted := []Author{
		{Name: "AuthorC", LatestReleaseDate: day2},
		{Name: "AuthorA", LatestReleaseDate: day1},
		{Name: "AuthorB", LatestReleaseDate: day1},
	}
	if !reflect.DeepEqual(sorted, wantSorted) {
		t.Fatalf("SortAuthors = %+v, want %+v", sorted, wantSorted)
	}
}

func TestCleanURL(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "queryとfragmentを除去する", raw: "https://www.amazon.co.jp/dp/B000000001?tag=x#frag", want: "https://www.amazon.co.jp/dp/B000000001"},
		{name: "queryだけを除去する", raw: "https://www.amazon.co.jp/dp/B000000001?tag=x", want: "https://www.amazon.co.jp/dp/B000000001"},
		{name: "何も付いていないURLはそのまま返す", raw: "https://www.amazon.co.jp/dp/B000000001", want: "https://www.amazon.co.jp/dp/B000000001"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := CleanURL(tc.raw)
			if got != tc.want {
				t.Fatalf("CleanURL(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestParseReleaseDate(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		want    time.Time
		wantErr error
	}{
		{name: "日本語年月日をUTC00:00:00へ正規化する", text: "2026年8月28日", want: time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC)},
		{name: "スラッシュ区切りをUTC00:00:00へ正規化する", text: "2026/8/28", want: time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC)},
		{name: "前後の文言があっても日付を抽出する", text: "発売予定日は2026年8月28日です。", want: time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC)},
		{name: "ゼロ埋め月日を受け付ける", text: "2026年08月05日", want: time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)},
		{name: "日付形式ではない場合はErrInvalidReleaseDate", text: "発売未定", wantErr: ErrInvalidReleaseDate},
		{name: "空文字はErrInvalidReleaseDate", text: "", wantErr: ErrInvalidReleaseDate},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseReleaseDate(tc.text)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("ParseReleaseDate(%q) err = %v, want %v", tc.text, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseReleaseDate(%q) unexpected err = %v", tc.text, err)
			}
			if !got.Equal(tc.want) {
				t.Fatalf("ParseReleaseDate(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}
