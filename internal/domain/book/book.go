// Package book は書籍と作者の業務型、および外部サービスへ依存しない業務判定を提供する。
// JSON encode 時の PascalCase フィールド名や未知フィールド保持は codec 層が担い、
// この package では純粋な業務表現と判定だけを扱う。
package book

import (
	"net/url"
	"sort"
	"time"
)

// Price は取得済みの円価格を表す。
// Valid が false のときは未取得を示し、Kindle Unlimited 等の 0円とは区別する。
// Amazon HTML から正の金額を取得できなかった場合は未取得として扱う（SPECIFICATION.md 11.2）。
type Price struct {
	yen   float64
	valid bool
}

// NewPrice は正の金額から Price を作る。0 または負の値は未取得扱いとする。
func NewPrice(yen float64) Price {
	if yen <= 0 {
		return Price{}
	}
	return Price{yen: yen, valid: true}
}

// UnknownPrice は未取得の Price を返す。
func UnknownPrice() Price { return Price{} }

// Yen は円価格を返す。Valid が false の場合は 0 を返す。
func (p Price) Yen() float64 { return p.yen }

// Valid は取得済みの正の金額であるかを返す。
func (p Price) Valid() bool { return p.valid }

// KindleBook は paper_books_asins / unprocessed_asins / notified_asins / upcoming_asins
// で共用される書籍レコードの業務型（SPECIFICATION.md 9.2）。
type KindleBook struct {
	ASIN         string
	Title        string
	ReleaseDate  time.Time
	CurrentPrice Price
	MaxPrice     Price
	URL          string
	CreatedAt    time.Time
}

// Author は authors.json の作者レコードの業務型（SPECIFICATION.md 9.3）。
type Author struct {
	Name               string
	URL                string
	LatestReleaseDate  time.Time
	LatestReleaseTitle string
	LatestReleaseURL   string
}

// maxPrice は候補のうち取得済みで最も高い円価格を返す。
// 取得済み候補が1つもない場合は未取得を返す。
func maxPrice(candidates ...Price) Price {
	var best Price
	for _, c := range candidates {
		if !c.Valid() {
			continue
		}
		if !best.Valid() || c.Yen() > best.Yen() {
			best = c
		}
	}
	return best
}

// UpdatePriceHistory は今回取得価格で価格履歴を更新する（SPECIFICATION.md 12.3）。
//
//	new.CurrentPrice = current
//	new.MaxPrice = max(old.MaxPrice, old.CurrentPrice, current)
//	new.CreatedAt = old.CreatedAt
//
// old が新規レコード（CreatedAt ゼロ値）の場合は now を作成時刻にする。
// old.MaxPrice が未取得の場合は今回価格が初回基準になる。
// 既存Go実装にあった「セール成立書籍を保存対象から外す」挙動は引き継がない（SPECIFICATION.md 12.3）。
func UpdatePriceHistory(old KindleBook, current Price, now time.Time) KindleBook {
	updated := old
	updated.CurrentPrice = current
	updated.MaxPrice = maxPrice(old.MaxPrice, old.CurrentPrice, current)
	if old.CreatedAt.IsZero() {
		updated.CreatedAt = now
	}
	return updated
}

// DedupBooks は ASIN で重複排除する。同じ ASIN は最初の出現を優先する。
// SPECIFICATION.md 10 の「重複時は既存 unprocessed 側を優先」は、
// 呼び出し側で append(original, upcoming...) の順序を保証することで実現する。
// ASIN が空のレコードは重複排除の判定対象にできず、そのまま残す。
func DedupBooks(books []KindleBook) []KindleBook {
	seen := make(map[string]struct{})
	out := make([]KindleBook, 0, len(books))
	for _, b := range books {
		if b.ASIN == "" {
			out = append(out, b)
			continue
		}
		if _, exists := seen[b.ASIN]; exists {
			continue
		}
		seen[b.ASIN] = struct{}{}
		out = append(out, b)
	}
	return out
}

// SortBooks は発売日降順、同日の場合はタイトル昇順へ並べる（SPECIFICATION.md 9.2）。
// 入力スライスは変更せず、並び替えた新しいスライスを返す。
func SortBooks(books []KindleBook) []KindleBook {
	sorted := append([]KindleBook(nil), books...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if !sameDay(sorted[i].ReleaseDate, sorted[j].ReleaseDate) {
			return sorted[i].ReleaseDate.After(sorted[j].ReleaseDate)
		}
		return sorted[i].Title < sorted[j].Title
	})
	return sorted
}

// DedupAuthors は Name で重複排除する。同じ Name は最初の出現を優先する。
func DedupAuthors(authors []Author) []Author {
	seen := make(map[string]struct{})
	out := make([]Author, 0, len(authors))
	for _, a := range authors {
		if a.Name == "" {
			out = append(out, a)
			continue
		}
		if _, exists := seen[a.Name]; exists {
			continue
		}
		seen[a.Name] = struct{}{}
		out = append(out, a)
	}
	return out
}

// SortAuthors は最新発売日降順、同日の場合は作者名昇順へ並べる（SPECIFICATION.md 9.3）。
func SortAuthors(authors []Author) []Author {
	sorted := append([]Author(nil), authors...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if !sameDay(sorted[i].LatestReleaseDate, sorted[j].LatestReleaseDate) {
			return sorted[i].LatestReleaseDate.After(sorted[j].LatestReleaseDate)
		}
		return sorted[i].Name < sorted[j].Name
	})
	return sorted
}

// CleanURL は URL の query と fragment を除去する（SPECIFICATION.md 9.3 LatestReleaseURL）。
// 解析できない場合は入力をそのまま返す。
func CleanURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}
