package storage

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/shinderuman/kindle-automation/internal/domain/book"
)

func TestEncodeBooks_RoundTripPreservesUnknownFields(t *testing.T) {
	src := `[
    {
        "ASIN": "B0FX3X569X",
        "Title": "テスト書籍",
        "ReleaseDate": "2026-08-28T00:00:00Z",
        "CurrentPrice": 759,
        "MaxPrice": 900,
        "URL": "https://www.amazon.co.jp/dp/B0FX3X569X?tag=x&y=z",
        "CreatedAt": "2026-07-17T00:00:00Z",
        "Memo": "手動メモ",
        "ManualFlag": true
    }
]`
	records, err := DecodeBooks([]byte(src))
	if err != nil {
		t.Fatalf("DecodeBooks: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("len(records) = %d, want 1", len(records))
	}
	if _, ok := records[0].Extra["Memo"]; !ok {
		t.Errorf("unknown field Memo not kept after decode")
	}
	if _, ok := records[0].Extra["ManualFlag"]; !ok {
		t.Errorf("unknown field ManualFlag not kept after decode")
	}
	if records[0].Book.URL != "https://www.amazon.co.jp/dp/B0FX3X569X?tag=x&y=z" {
		t.Errorf("URL = %q", records[0].Book.URL)
	}

	encoded, err := EncodeBooks(records)
	if err != nil {
		t.Fatalf("EncodeBooks: %v", err)
	}
	if !bytes.Contains(encoded, []byte("tag=x&y=z")) {
		t.Errorf("& not unescaped in URL: %s", encoded)
	}
	if !bytes.Contains(encoded, []byte("手動メモ")) {
		t.Errorf("unknown field Memo not kept after encode")
	}

	records2, err := DecodeBooks(encoded)
	if err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	if records2[0].Book.ASIN != "B0FX3X569X" {
		t.Errorf("ASIN round-trip failed: %q", records2[0].Book.ASIN)
	}
	if !records2[0].Book.CurrentPrice.Valid() || records2[0].Book.CurrentPrice.Yen() != 759 {
		t.Errorf("CurrentPrice round-trip failed: %+v", records2[0].Book.CurrentPrice)
	}
}

func TestEncodeBooks_UnescapesAmpButKeepsAngleBrackets(t *testing.T) {
	records := []BookRecord{{
		Book: book.KindleBook{
			ASIN:  "B0FX3X569X",
			Title: "a<b>c&d",
		},
	}}
	encoded, err := EncodeBooks(records)
	if err != nil {
		t.Fatalf("EncodeBooks: %v", err)
	}
	// & は & へ復元する。
	if !bytes.Contains(encoded, []byte("c&d")) {
		t.Errorf("& should be unescaped: %s", encoded)
	}
	// < > は Unicode escape のまま残す（既存 kindle_bot 互換）。
	if bytes.Contains(encoded, []byte("a<b>c")) {
		t.Errorf("< > should stay escaped: %s", encoded)
	}
	if !bytes.Contains(encoded, []byte("a\\u003cb\\u003ec")) {
		t.Errorf("< > not unicode-escaped: %s", encoded)
	}
}

func TestEncodeBooks_FourSpaceIndent(t *testing.T) {
	records := []BookRecord{{Book: book.KindleBook{ASIN: "B0FX3X569X"}}}
	encoded, err := EncodeBooks(records)
	if err != nil {
		t.Fatalf("EncodeBooks: %v", err)
	}
	// 配列要素は4スペース、要素内 field は8スペース。
	if !bytes.Contains(encoded, []byte("    {")) {
		t.Errorf("array element not 4-space indented:\n%s", encoded)
	}
	if !bytes.Contains(encoded, []byte("        \"ASIN\"")) {
		t.Errorf("field not 8-space indented:\n%s", encoded)
	}
}

func TestEncodeBooks_EmptyArray(t *testing.T) {
	encoded, err := EncodeBooks(nil)
	if err != nil {
		t.Fatalf("EncodeBooks: %v", err)
	}
	if strings.TrimSpace(string(encoded)) != "[]" {
		t.Errorf("empty array = %s, want []", encoded)
	}
}

func TestEncodeBooks_KeepsFieldOrder(t *testing.T) {
	records := []BookRecord{{Book: book.KindleBook{
		ASIN: "B0FX3X569X", Title: "T", ReleaseDate: time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC),
		CurrentPrice: book.NewPrice(759), MaxPrice: book.NewPrice(900),
		URL: "u", CreatedAt: time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC),
	}}}
	encoded, err := EncodeBooks(records)
	if err != nil {
		t.Fatalf("EncodeBooks: %v", err)
	}
	asinIdx := bytes.Index(encoded, []byte("\"ASIN\""))
	titleIdx := bytes.Index(encoded, []byte("\"Title\""))
	releaseIdx := bytes.Index(encoded, []byte("\"ReleaseDate\""))
	currentIdx := bytes.Index(encoded, []byte("\"CurrentPrice\""))
	maxIdx := bytes.Index(encoded, []byte("\"MaxPrice\""))
	urlIdx := bytes.Index(encoded, []byte("\"URL\""))
	createdIdx := bytes.Index(encoded, []byte("\"CreatedAt\""))
	if !(asinIdx < titleIdx && titleIdx < releaseIdx && releaseIdx < currentIdx && currentIdx < maxIdx && maxIdx < urlIdx && urlIdx < createdIdx) {
		t.Errorf("field order not preserved:\n%s", encoded)
	}
}

func TestDecodeBooks_AcceptsLegacyDateFormatsAndNormalizesToRFC3339(t *testing.T) {
	// 既存 kindle_bot entity.Date が受容する旧形式（date-only, slash 区切り）を decode で受容する。
	src := `[{"ASIN":"B0FX3X569X","Title":"T","ReleaseDate":"2026-08-28","CurrentPrice":0,"MaxPrice":0,"URL":"","CreatedAt":"2026/01/02"}]`
	records, err := DecodeBooks([]byte(src))
	if err != nil {
		t.Fatalf("DecodeBooks: %v", err)
	}
	wantRelease, _ := time.Parse("2006-01-02", "2026-08-28")
	if !records[0].Book.ReleaseDate.Equal(wantRelease) {
		t.Errorf("ReleaseDate = %v, want %v", records[0].Book.ReleaseDate, wantRelease)
	}
	wantCreated, _ := time.Parse("2006/01/02", "2026/01/02")
	if !records[0].Book.CreatedAt.Equal(wantCreated) {
		t.Errorf("CreatedAt = %v, want %v", records[0].Book.CreatedAt, wantCreated)
	}
	if _, ok := records[0].Extra["ReleaseDate"]; ok {
		t.Errorf("解析成功した ReleaseDate が Extra に入っている")
	}

	// 書込は SPEC どおり UTC RFC3339 へ正規化する。
	encoded, err := EncodeBooks(records)
	if err != nil {
		t.Fatalf("EncodeBooks: %v", err)
	}
	if !bytes.Contains(encoded, []byte(`"ReleaseDate": "2026-08-28T00:00:00Z"`)) {
		t.Errorf("ReleaseDate not RFC3339: %s", encoded)
	}
	if !bytes.Contains(encoded, []byte(`"CreatedAt": "2026-01-02T00:00:00Z"`)) {
		t.Errorf("CreatedAt not RFC3339: %s", encoded)
	}
}

func TestDecodeBooks_PreservesInvalidDateWithoutZeroing(t *testing.T) {
	// 解釈できない日付はゼロ値へ黙って変換せず、元値を Extra へ保持して再保存時も壊さない。
	src := `[{"ASIN":"B0FX3X569X","Title":"T","ReleaseDate":"not-a-date","CurrentPrice":0,"MaxPrice":0,"URL":"","CreatedAt":"2026-01-01T00:00:00Z"}]`
	records, err := DecodeBooks([]byte(src))
	if err != nil {
		t.Fatalf("DecodeBooks: %v", err)
	}
	if _, ok := records[0].Extra["ReleaseDate"]; !ok {
		t.Fatalf("invalid ReleaseDate not kept in Extra")
	}
	encoded, err := EncodeBooks(records)
	if err != nil {
		t.Fatalf("EncodeBooks: %v", err)
	}
	if !bytes.Contains(encoded, []byte(`"not-a-date"`)) {
		t.Errorf("invalid date value lost (zeroed): %s", encoded)
	}
	if bytes.Contains(encoded, []byte(`"ReleaseDate": "0001-01-01T00:00:00Z"`)) {
		t.Errorf("invalid date replaced with zero value: %s", encoded)
	}
}

func TestEncodeAuthors_KeepsFieldOrder(t *testing.T) {
	records := []AuthorRecord{{Author: book.Author{
		Name: "海李", URL: "u",
		LatestReleaseDate:  time.Date(2025, 12, 28, 0, 0, 0, 0, time.UTC),
		LatestReleaseTitle: "最新作",
		LatestReleaseURL:   "latest-url",
	}}}
	encoded, err := EncodeAuthors(records)
	if err != nil {
		t.Fatalf("EncodeAuthors: %v", err)
	}
	nameIdx := bytes.Index(encoded, []byte("\"Name\""))
	urlIdx := bytes.Index(encoded, []byte("\"URL\""))
	dateIdx := bytes.Index(encoded, []byte("\"LatestReleaseDate\""))
	titleIdx := bytes.Index(encoded, []byte("\"LatestReleaseTitle\""))
	latestURLIdx := bytes.Index(encoded, []byte("\"LatestReleaseURL\""))
	if !(nameIdx < urlIdx && urlIdx < dateIdx && dateIdx < titleIdx && titleIdx < latestURLIdx) {
		t.Errorf("author field order not preserved:\n%s", encoded)
	}
}

// TestEncodeAuthors_RoundTripPreservesUnknownFields は作者レコードの未知 field 保持と
// & 復元を検証する（SPECIFICATION.md 9.3/9.4）。book 側の契約検証と同等の保証を author にも適用する。
func TestEncodeAuthors_RoundTripPreservesUnknownFields(t *testing.T) {
	src := `[
    {
        "Name": "海李",
        "URL": "https://www.amazon.co.jp/stores/海李?tag=x&y=z",
        "LatestReleaseDate": "2025-12-28T00:00:00Z",
        "LatestReleaseTitle": "最新作",
        "LatestReleaseURL": "https://www.amazon.co.jp/dp/B0NEW?tag=x&y=z",
        "Memo": "手動メモ"
    }
]`
	records, err := DecodeAuthors([]byte(src))
	if err != nil {
		t.Fatalf("DecodeAuthors: %v", err)
	}
	if _, ok := records[0].Extra["Memo"]; !ok {
		t.Errorf("unknown field Memo not kept after decode")
	}
	if records[0].Author.LatestReleaseURL != "https://www.amazon.co.jp/dp/B0NEW?tag=x&y=z" {
		t.Errorf("LatestReleaseURL = %q", records[0].Author.LatestReleaseURL)
	}
	encoded, err := EncodeAuthors(records)
	if err != nil {
		t.Fatalf("EncodeAuthors: %v", err)
	}
	if !bytes.Contains(encoded, []byte("tag=x&y=z")) {
		t.Errorf("& not unescaped in author URL: %s", encoded)
	}
	if !bytes.Contains(encoded, []byte("手動メモ")) {
		t.Errorf("unknown field Memo not kept after encode")
	}
}

// TestDecodeAuthors_AcceptsLegacyDateFormats は author の LatestReleaseDate が
// 旧形式（date-only/slash）を受容し RFC3339 へ正規化されることを検証する（SPECIFICATION.md 9.3）。
func TestDecodeAuthors_AcceptsLegacyDateFormats(t *testing.T) {
	src := `[{"Name":"海李","URL":"u","LatestReleaseDate":"2025/12/28","LatestReleaseTitle":"作","LatestReleaseURL":"u"}]`
	records, err := DecodeAuthors([]byte(src))
	if err != nil {
		t.Fatalf("DecodeAuthors: %v", err)
	}
	want, _ := time.Parse("2006/01/02", "2025/12/28")
	if !records[0].Author.LatestReleaseDate.Equal(want) {
		t.Errorf("LatestReleaseDate = %v, want %v", records[0].Author.LatestReleaseDate, want)
	}
	encoded, err := EncodeAuthors(records)
	if err != nil {
		t.Fatalf("EncodeAuthors: %v", err)
	}
	if !bytes.Contains(encoded, []byte(`"LatestReleaseDate": "2025-12-28T00:00:00Z"`)) {
		t.Errorf("LatestReleaseDate not RFC3339: %s", encoded)
	}
}

func TestDecodeBooks_NullAndEmptyDateAreZero(t *testing.T) {
	// null / 空文字は entity.Date と同じくゼロ値扱いとし、Extra へは入れない。
	src := `[{"ASIN":"B0FX3X569X","Title":"T","ReleaseDate":null,"CurrentPrice":0,"MaxPrice":0,"URL":"","CreatedAt":""}]`
	records, err := DecodeBooks([]byte(src))
	if err != nil {
		t.Fatalf("DecodeBooks: %v", err)
	}
	if !records[0].Book.ReleaseDate.IsZero() {
		t.Errorf("null ReleaseDate should be zero: %v", records[0].Book.ReleaseDate)
	}
	if !records[0].Book.CreatedAt.IsZero() {
		t.Errorf("empty CreatedAt should be zero: %v", records[0].Book.CreatedAt)
	}
	if _, ok := records[0].Extra["ReleaseDate"]; ok {
		t.Errorf("null ReleaseDate should not be in Extra")
	}
}
