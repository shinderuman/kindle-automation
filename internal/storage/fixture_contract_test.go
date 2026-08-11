package storage

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// test は internal/storage で実行されるため repo root の testdata/s3 へ ../../ で到達する。
const fixtureDir = "../../testdata/s3"

// mustReadFixture は存在前提の契約検証のため fixture 欠落時は fatal とする。
func mustReadFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(fixtureDir, name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

func assertFieldOrder(t *testing.T, encoded []byte, keys []string) {
	t.Helper()
	prev := -1
	for _, k := range keys {
		idx := bytes.Index(encoded, []byte("\""+k+"\""))
		if idx < 0 {
			t.Fatalf("field %q not found in encoded output:\n%s", k, encoded)
		}
		if idx <= prev {
			t.Fatalf("field %q at %d not after previous %d (order broken):\n%s", k, idx, prev, encoded)
		}
		prev = idx
	}
}

// 日付は ASCII 限定のため strconv.Quote で JSON 互換のリテラルが得られる。
func jsonDateLiteral(t time.Time) string {
	return strconv.Quote(t.UTC().Format(time.RFC3339))
}

// extraEqualTest は migrate 側の extraEqual とは別物（テスト専用）。
func extraEqualTest(a, b map[string]json.RawMessage) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		bv, ok := b[k]
		if !ok {
			return false
		}
		if !bytes.Equal(v, bv) {
			return false
		}
	}
	return true
}

func bookRecordsEqual(a, b []BookRecord) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		x, y := a[i].Book, b[i].Book
		if x.ASIN != y.ASIN || x.Title != y.Title || x.URL != y.URL {
			return false
		}
		if !x.ReleaseDate.Equal(y.ReleaseDate) || !x.CreatedAt.Equal(y.CreatedAt) {
			return false
		}
		if x.CurrentPrice.Valid() != y.CurrentPrice.Valid() || x.CurrentPrice.Yen() != y.CurrentPrice.Yen() {
			return false
		}
		if x.MaxPrice.Valid() != y.MaxPrice.Valid() || x.MaxPrice.Yen() != y.MaxPrice.Yen() {
			return false
		}
		if !extraEqualTest(a[i].Extra, b[i].Extra) {
			return false
		}
	}
	return true
}

func assertBookContractDimensions(t *testing.T, name string, unknownField string) {
	t.Helper()
	raw := mustReadFixture(t, name)

	records, err := DecodeBooks(raw)
	if err != nil {
		t.Fatalf("%s: DecodeBooks: %v", name, err)
	}
	if len(records) < 1 {
		t.Fatalf("%s: need >=1 record for contract check, got 0", name)
	}

	// EncodeBooks は入力順を保存するため fixture の降順をそのまま検証（書込時 sort は merge 層で別担保）。
	for i := 1; i < len(records); i++ {
		prev := records[i-1].Book.ReleaseDate
		cur := records[i].Book.ReleaseDate
		if !sameBookDay(prev, cur) && prev.Before(cur) {
			t.Errorf("%s: record %d が %d より古い（降順でない）: %v < %v", name, i-1, i, prev, cur)
		}
	}

	for i, r := range records {
		if r.Book.ASIN == "" {
			t.Errorf("%s: record %d ASIN empty", name, i)
		}
		if r.Book.CurrentPrice.Yen() < 0 {
			t.Errorf("%s: record %d CurrentPrice negative", name, i)
		}
	}

	if unknownField != "" {
		if _, ok := records[0].Extra[unknownField]; !ok {
			t.Errorf("%s: unknown field %q not kept after decode", name, unknownField)
		}
	}

	encoded, err := EncodeBooks(records)
	if err != nil {
		t.Fatalf("%s: EncodeBooks: %v", name, err)
	}

	if !bytes.Contains(encoded, []byte("\n    {")) {
		t.Errorf("%s: array element not 4-space indented:\n%s", name, encoded)
	}
	if !bytes.Contains(encoded, []byte("\n        \"ASIN\"")) {
		t.Errorf("%s: field not 8-space indented:\n%s", name, encoded)
	}

	assertFieldOrder(t, encoded, []string{
		"ASIN", "Title", "ReleaseDate", "CurrentPrice", "MaxPrice", "URL", "CreatedAt",
	})

	wantRelease := jsonDateLiteral(records[0].Book.ReleaseDate)
	if !bytes.Contains(encoded, []byte("\"ReleaseDate\": "+wantRelease)) {
		t.Errorf("%s: ReleaseDate not RFC3339 (%s):\n%s", name, wantRelease, encoded)
	}

	if unknownField != "" && !bytes.Contains(encoded, []byte("\""+unknownField+"\"")) {
		t.Errorf("%s: unknown field %q lost after encode:\n%s", name, unknownField, encoded)
	}

	if bytes.Contains(encoded, []byte("\\u0026")) {
		t.Errorf("%s: & should be unescaped (found \\u0026):\n%s", name, encoded)
	}

	redecoded, err := DecodeBooks(encoded)
	if err != nil {
		t.Fatalf("%s: re-decode: %v", name, err)
	}
	if !bookRecordsEqual(records, redecoded) {
		t.Errorf("%s: round-trip changed records", name)
	}
}

func TestContract_BookFixtures(t *testing.T) {
	cases := []struct {
		name         string
		file         string
		unknownField string
	}{
		{name: "paper_books_asins", file: "paper_books_asins.json", unknownField: "ManualFlag"},
		{name: "unprocessed_asins", file: "unprocessed_asins.json", unknownField: "Note"},
		{name: "upcoming_asins", file: "upcoming_asins.json", unknownField: "Source"},
		{name: "notified_asins", file: "notified_asins.json", unknownField: "NotifiedAt"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assertBookContractDimensions(t, c.file, c.unknownField)
		})
	}
}

func TestContract_PaperBooksCurrentPriceZero(t *testing.T) {
	records, err := DecodeBooks(mustReadFixture(t, "paper_books_asins.json"))
	if err != nil {
		t.Fatalf("DecodeBooks: %v", err)
	}
	for i, r := range records {
		if r.Book.CurrentPrice.Valid() {
			t.Errorf("record %d CurrentPrice should be invalid (0=未取得): %+v", i, r.Book.CurrentPrice)
		}
		if r.Book.CurrentPrice.Yen() != 0 {
			t.Errorf("record %d CurrentPrice.Yen = %v, want 0", i, r.Book.CurrentPrice.Yen())
		}
	}
}

func TestContract_UnprocessedAmpInURL(t *testing.T) {
	records, err := DecodeBooks(mustReadFixture(t, "unprocessed_asins.json"))
	if err != nil {
		t.Fatalf("DecodeBooks: %v", err)
	}
	encoded, err := EncodeBooks(records)
	if err != nil {
		t.Fatalf("EncodeBooks: %v", err)
	}
	if !bytes.Contains(encoded, []byte("tag=aff-22&linkCode=x")) {
		t.Errorf("& in URL not preserved:\n%s", encoded)
	}
}

func TestContract_AuthorsFixture(t *testing.T) {
	raw := mustReadFixture(t, "authors.json")
	records, err := DecodeAuthors(raw)
	if err != nil {
		t.Fatalf("DecodeAuthors: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("len = %d, want 2", len(records))
	}

	if records[0].Author.LatestReleaseDate.Before(records[1].Author.LatestReleaseDate) {
		t.Errorf("authors not desc by LatestReleaseDate: %v before %v",
			records[0].Author.LatestReleaseDate, records[1].Author.LatestReleaseDate)
	}

	if records[0].Author.Name != "作者A" {
		t.Errorf("Author.Name = %q", records[0].Author.Name)
	}
	if _, ok := records[0].Extra["Memo"]; !ok {
		t.Errorf("unknown field Memo not kept for 作者A")
	}
	if records[1].Extra != nil {
		t.Errorf("作者B should have no Extra, got %+v", records[1].Extra)
	}

	encoded, err := EncodeAuthors(records)
	if err != nil {
		t.Fatalf("EncodeAuthors: %v", err)
	}

	if !bytes.Contains(encoded, []byte("\n    {")) {
		t.Errorf("array element not 4-space indented:\n%s", encoded)
	}
	if !bytes.Contains(encoded, []byte("\n        \"Name\"")) {
		t.Errorf("field not 8-space indented:\n%s", encoded)
	}
	assertFieldOrder(t, encoded, []string{
		"Name", "URL", "LatestReleaseDate", "LatestReleaseTitle", "LatestReleaseURL",
	})
	wantDate := jsonDateLiteral(records[0].Author.LatestReleaseDate)
	if !bytes.Contains(encoded, []byte("\"LatestReleaseDate\": "+wantDate)) {
		t.Errorf("LatestReleaseDate not RFC3339 (%s):\n%s", wantDate, encoded)
	}
	if bytes.Contains(encoded, []byte("\\u0026")) {
		t.Errorf("& should be unescaped:\n%s", encoded)
	}
	if !bytes.Contains(encoded, []byte("tag=aff-22\"")) {
		t.Errorf("& in author URL not preserved:\n%s", encoded)
	}
}
