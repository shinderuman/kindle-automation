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

// fixtureDir は testdata/s3 配下の既存JSON契約fixtureへの相対パス。
// testは internal/storage で実行されるため、repo root の testdata/s3 へ ../../ で到達する。
const fixtureDir = "../../testdata/s3"

// mustReadFixture は fixture を読み込む。存在前提の契約検証なので、欠落時は fatal とする。
func mustReadFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(fixtureDir, name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

// assertFieldOrder は encoded JSON 内の既知 field が期待順序で出現するかを検証する。
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

// jsonDateLiteral は RFC3339 日付を JSON 文字列リテラル（前後の " 含む）へ変換する。
// 日付は ASCII 限定のため strconv.Quote で JSON 互換のリテラルが得られる。
func jsonDateLiteral(t time.Time) string {
	return strconv.Quote(t.UTC().Format(time.RFC3339))
}

// extraEqualTest は未知 field の map が等しいかを返す（テスト用。migrate 側の extraEqual とは別物）。
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

// assertBookContractDimensions は1つの書籍fixtureについて、SPECIFICATION.md 9.2 の
// 契約次元（field順/型・未知field保持・UTC RFC3339・4space indent・並び順・&非escape）を検証する。
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

	// 並び順: EncodeBooks は入力順を保存する。fixture は発売日降順で書かれているため、
	// そのまま降順で読めることを検証する（書込時の sort は merge 層の別テストで担保）。
	// 単一件の object（upcoming 等）では並び順検証は意味を持たないため自然に skip される。
	for i := 1; i < len(records); i++ {
		prev := records[i-1].Book.ReleaseDate
		cur := records[i].Book.ReleaseDate
		if !sameBookDay(prev, cur) && prev.Before(cur) {
			t.Errorf("%s: record %d が %d より古い（降順でない）: %v < %v", name, i-1, i, prev, cur)
		}
	}

	// 型: ASIN は空でない文字列、CurrentPrice は負でない。
	for i, r := range records {
		if r.Book.ASIN == "" {
			t.Errorf("%s: record %d ASIN empty", name, i)
		}
		if r.Book.CurrentPrice.Yen() < 0 {
			t.Errorf("%s: record %d CurrentPrice negative", name, i)
		}
	}

	// 未知 field 保持: 先頭 record に未知 field が残る。
	if unknownField != "" {
		if _, ok := records[0].Extra[unknownField]; !ok {
			t.Errorf("%s: unknown field %q not kept after decode", name, unknownField)
		}
	}

	encoded, err := EncodeBooks(records)
	if err != nil {
		t.Fatalf("%s: EncodeBooks: %v", name, err)
	}

	// 4space indent: 配列要素4スペース、要素内 field 8スペース。
	if !bytes.Contains(encoded, []byte("\n    {")) {
		t.Errorf("%s: array element not 4-space indented:\n%s", name, encoded)
	}
	if !bytes.Contains(encoded, []byte("\n        \"ASIN\"")) {
		t.Errorf("%s: field not 8-space indented:\n%s", name, encoded)
	}

	// field 順序: SPECIFICATION.md 9.2 の固定順序。
	assertFieldOrder(t, encoded, []string{
		"ASIN", "Title", "ReleaseDate", "CurrentPrice", "MaxPrice", "URL", "CreatedAt",
	})

	// UTC RFC3339: 発売日は Z suffixed の RFC3339 文字列で書き戻される。
	wantRelease := jsonDateLiteral(records[0].Book.ReleaseDate)
	if !bytes.Contains(encoded, []byte("\"ReleaseDate\": "+wantRelease)) {
		t.Errorf("%s: ReleaseDate not RFC3339 (%s):\n%s", name, wantRelease, encoded)
	}

	// 未知 field が encode 後も残る。
	if unknownField != "" && !bytes.Contains(encoded, []byte("\""+unknownField+"\"")) {
		t.Errorf("%s: unknown field %q lost after encode:\n%s", name, unknownField, encoded)
	}

	// &非escape: & を含む URL が \u0026 へ戻らない（< > の escape 残存は別テスト扱い）。
	if bytes.Contains(encoded, []byte("\\u0026")) {
		t.Errorf("%s: & should be unescaped (found \\u0026):\n%s", name, encoded)
	}

	// 意味的安定: encode → decode が同一 record 集合へ戻る（field 順や空白を問わない値の保存）。
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

// TestContract_PaperBooksCurrentPriceZero は paper_books_asins.json の全件 CurrentPrice=0
// （SPECIFICATION.md 20.1）が未取得（Valid=false）へ正しく読まれることを検証する。
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

// TestContract_UnprocessedAmpInURL は unprocessed_asins.json の & 含み URL が
// そのまま保存されることを検証する（SPECIFICATION.md 9.2）。
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

// TestContract_AuthorsFixture は authors.json の契約次元を検証する（SPECIFICATION.md 9.3）。
func TestContract_AuthorsFixture(t *testing.T) {
	raw := mustReadFixture(t, "authors.json")
	records, err := DecodeAuthors(raw)
	if err != nil {
		t.Fatalf("DecodeAuthors: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("len = %d, want 2", len(records))
	}

	// 並び順: 最新発売日降順。
	if records[0].Author.LatestReleaseDate.Before(records[1].Author.LatestReleaseDate) {
		t.Errorf("authors not desc by LatestReleaseDate: %v before %v",
			records[0].Author.LatestReleaseDate, records[1].Author.LatestReleaseDate)
	}

	// 型・未知 field: 作者A に Memo、作者B にはない。
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

	// 4space indent・field 順序・UTC RFC3339・&非escape。
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
