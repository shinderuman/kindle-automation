// Package storage は S3 JSON の codec と ETag 付き条件付き更新を提供する。
// 既存 kindle_bot のデータ契約（PascalCase・4スペース indent・& のみ復元し < > は Unicode escape 残存・並び順）へ合わせ、
// 未知 field 保持と日付読込互換性を追加する（SPECIFICATION.md 9.2/9.4, AGENTS.md 7）。
package storage


import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shinderuman/kindle-automation/internal/domain/book"
)

// 書籍JSONの1レコード。Book は既知業務 field、Extra は未知 field を保持する。
// 既知 field のうち解釈できなかった値（不正日付等）も Extra へ保持し、再保存時の黙ったゼロ値化を防ぐ。
type BookRecord struct {
	Book  book.KindleBook
	Extra map[string]json.RawMessage
}

type AuthorRecord struct {
	Author book.Author
	Extra  map[string]json.RawMessage
}

type marshalField struct {
	Key   string
	Value any
}

// 既存 kindle_bot の entity.Date（goark/pa-api v0.17.1）と同じ日付受容形式。書込は常に time.RFC3339（SPECIFICATION.md 9.2）。
var dateTemplates = []string{
	time.RFC3339,
	"2006-01T",
	"2006-01-02",
	"2006-01",
	"2006/01/02",
	"2006/01",
	"2006",
	"2006T",
}

// 既知 field を struct 順に、未知 field を key 昇順で出力する。Extra に既知 field と同名 key があれば
// （解釈できなかった日付等）そちらを優先し値を壊さない。HTML escape は encoding/json 標準（< > & を Unicode escape）
// へ従い、& の復元は EncodeBooks で行う。
func (r BookRecord) MarshalJSON() ([]byte, error) {
	fields := []marshalField{
		{"ASIN", r.Book.ASIN},
		{"Title", r.Book.Title},
		{"ReleaseDate", r.Book.ReleaseDate},
		{"CurrentPrice", r.Book.CurrentPrice.Yen()},
		{"MaxPrice", r.Book.MaxPrice.Yen()},
		{"URL", r.Book.URL},
		{"CreatedAt", r.Book.CreatedAt},
	}
	return marshalRecord(omitFieldsInExtra(fields, r.Extra), r.Extra)
}

// JSON object から既知 field を Book へ、残りを Extra へ復号する。日付が entity.Date 受容形式なら time.Time へ、
// 解釈できない場合は元値を Extra へ保持する。
func (r *BookRecord) UnmarshalJSON(data []byte) error {
	m, err := decodeObject(data)
	if err != nil {
		return err
	}
	r.Extra = pickExtra(m, knownBookKeys())
	r.Book.ASIN = pickString(m["ASIN"])
	r.Book.Title = pickString(m["Title"])
	r.Book.ReleaseDate, r.Extra = pickDateOrExtra(m["ReleaseDate"], "ReleaseDate", r.Extra)
	r.Book.CurrentPrice = book.NewPrice(pickFloat(m["CurrentPrice"]))
	r.Book.MaxPrice = book.NewPrice(pickFloat(m["MaxPrice"]))
	r.Book.URL = pickString(m["URL"])
	r.Book.CreatedAt, r.Extra = pickDateOrExtra(m["CreatedAt"], "CreatedAt", r.Extra)
	return nil
}

func (r AuthorRecord) MarshalJSON() ([]byte, error) {
	fields := []marshalField{
		{"Name", r.Author.Name},
		{"URL", r.Author.URL},
		{"LatestReleaseDate", r.Author.LatestReleaseDate},
		{"LatestReleaseTitle", r.Author.LatestReleaseTitle},
		{"LatestReleaseURL", r.Author.LatestReleaseURL},
	}
	return marshalRecord(omitFieldsInExtra(fields, r.Extra), r.Extra)
}

func (r *AuthorRecord) UnmarshalJSON(data []byte) error {
	m, err := decodeObject(data)
	if err != nil {
		return err
	}
	r.Extra = pickExtra(m, knownAuthorKeys())
	r.Author.Name = pickString(m["Name"])
	r.Author.URL = pickString(m["URL"])
	r.Author.LatestReleaseDate, r.Extra = pickDateOrExtra(m["LatestReleaseDate"], "LatestReleaseDate", r.Extra)
	r.Author.LatestReleaseTitle = pickString(m["LatestReleaseTitle"])
	r.Author.LatestReleaseURL = pickString(m["LatestReleaseURL"])
	return nil
}

// SPECIFICATION.md 9.2: 4 スペース indent・& 復元済みの JSON 配列へ符号化する。
// < > は Unicode escape のまま残し、既存 kindle_bot の出力と一致させる。
func EncodeBooks(records []BookRecord) ([]byte, error) {
	encoders := make([]bookRecordEncoder, len(records))
	for i, r := range records {
		encoders[i] = r
	}
	return encodeRecords(encoders)
}

func EncodeAuthors(records []AuthorRecord) ([]byte, error) {
	encoders := make([]bookRecordEncoder, len(records))
	for i, r := range records {
		encoders[i] = r
	}
	return encodeRecords(encoders)
}

// 未知 field と解釈不能な既知 field は Extra へ保持する。
func DecodeBooks(data []byte) ([]BookRecord, error) {
	raws, err := decodeArray(data)
	if err != nil {
		return nil, err
	}
	records := make([]BookRecord, 0, len(raws))
	for _, raw := range raws {
		var r BookRecord
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, fmt.Errorf("decode book record: %w", err)
		}
		records = append(records, r)
	}
	return records, nil
}

func DecodeAuthors(data []byte) ([]AuthorRecord, error) {
	raws, err := decodeArray(data)
	if err != nil {
		return nil, err
	}
	records := make([]AuthorRecord, 0, len(raws))
	for _, raw := range raws {
		var r AuthorRecord
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, fmt.Errorf("decode author record: %w", err)
		}
		records = append(records, r)
	}
	return records, nil
}

type bookRecordEncoder interface {
	MarshalJSON() ([]byte, error)
}

func encodeRecords(records []bookRecordEncoder) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('[')
	for i, r := range records {
		if i > 0 {
			buf.WriteByte(',')
		}
		b, err := r.MarshalJSON()
		if err != nil {
			return nil, err
		}
		buf.Write(b)
	}
	buf.WriteByte(']')
	var indented bytes.Buffer
	if err := json.Indent(&indented, buf.Bytes(), "", "    "); err != nil {
		return nil, fmt.Errorf("indent: %w", err)
	}
	// "<" ">"（< >）はそのまま残し、既存 kindle_bot の出力と一致させる。
	return bytes.ReplaceAll(indented.Bytes(), []byte("\\u0026"), []byte("&")), nil
}

func marshalRecord(fields []marshalField, extra map[string]json.RawMessage) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, f := range fields {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.WriteString(strconv.Quote(f.Key))
		buf.WriteByte(':')
		b, err := json.Marshal(f.Value)
		if err != nil {
			return nil, fmt.Errorf("encode field %s: %w", f.Key, err)
		}
		buf.Write(b)
	}
	for _, k := range sortedKeys(extra) {
		if buf.Len() > 1 {
			buf.WriteByte(',')
		}
		buf.WriteString(strconv.Quote(k))
		buf.WriteByte(':')
		buf.Write(extra[k])
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// Extra にも存在する既知 field を出力候補から除く。解釈できなかった日付等の元値を Extra で保持している場合、
// 既知 field 側のゼロ値で上書きしない。
func omitFieldsInExtra(fields []marshalField, extra map[string]json.RawMessage) []marshalField {
	if len(extra) == 0 {
		return fields
	}
	out := make([]marshalField, 0, len(fields))
	for _, f := range fields {
		if _, ok := extra[f.Key]; ok {
			continue
		}
		out = append(out, f)
	}
	return out
}

func decodeObject(data []byte) (map[string]json.RawMessage, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func decodeArray(data []byte) ([][]byte, error) {
	var raws []json.RawMessage
	if err := json.Unmarshal(data, &raws); err != nil {
		return nil, err
	}
	out := make([][]byte, len(raws))
	for i, raw := range raws {
		out[i] = raw
	}
	return out, nil
}

func pickString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

// 日付を entity.Date 受容形式で解釈する。成功すれば time.Time を返す。空/"null" はゼロ値を返す。
// 解釈できない場合は元値を extra へ保持する。
func pickDateOrExtra(raw json.RawMessage, key string, extra map[string]json.RawMessage) (time.Time, map[string]json.RawMessage) {
	if len(raw) == 0 {
		return time.Time{}, extra
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return time.Time{}, withExtra(extra, key, raw)
	}
	if s == "" || strings.EqualFold(s, "null") {
		return time.Time{}, extra
	}
	for _, tmpl := range dateTemplates {
		if tm, err := time.Parse(tmpl, s); err == nil {
			return tm, extra
		}
	}
	return time.Time{}, withExtra(extra, key, raw)
}

func pickFloat(raw json.RawMessage) float64 {
	if len(raw) == 0 {
		return 0
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err != nil {
		return 0
	}
	return f
}

func pickExtra(m map[string]json.RawMessage, known map[string]struct{}) map[string]json.RawMessage {
	extra := make(map[string]json.RawMessage)
	for k, v := range m {
		if _, ok := known[k]; ok {
			continue
		}
		extra[k] = v
	}
	if len(extra) == 0 {
		return nil
	}
	return extra
}

func withExtra(extra map[string]json.RawMessage, key string, raw json.RawMessage) map[string]json.RawMessage {
	if extra == nil {
		extra = make(map[string]json.RawMessage)
	}
	extra[key] = raw
	return extra
}

func knownBookKeys() map[string]struct{} {
	return map[string]struct{}{
		"ASIN":         {},
		"Title":        {},
		"ReleaseDate":  {},
		"CurrentPrice": {},
		"MaxPrice":     {},
		"URL":          {},
		"CreatedAt":    {},
	}
}

func knownAuthorKeys() map[string]struct{} {
	return map[string]struct{}{
		"Name":               {},
		"URL":                {},
		"LatestReleaseDate":  {},
		"LatestReleaseTitle": {},
		"LatestReleaseURL":   {},
	}
}

func sortedKeys(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
