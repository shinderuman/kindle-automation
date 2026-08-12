// migrate-maxprice は SPECIFICATION.md 20.2 の MaxPrice 初期化パッチを適用する一回限りの移行 CLI。
//
// UserScript の Option+↑ で生成された既存 MaxPrice には紙書籍価格が入っており、
// 過去最高 Kindle 価格として使えないため、MaxPrice = CurrentPrice へ置き換える。
// 対象は unprocessed_asins.json / upcoming_asins.json / notified_asins.json の3 object。
//
// デフォルトは dry-run。-apply を明示指定した場合だけ S3 へ書き込む（AGENTS.md 13）。
// 件数・ASIN 集合・CurrentPrice・他 field が変わらないことを適用前後に検証する。
//
// 切り替え前の backup は bucket Versioning=Enabled を前提に現 VersionId の記録で行い、
// 本 CLI は backup prefix copy を作らない。rollback は記録した VersionId からの選択的復元とし、
// 配列全体の無条件上書きは行わない（SPECIFICATION.md 20.3/20.4）。
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/shinderuman/kindle-automation/internal/domain/book"
	"github.com/shinderuman/kindle-automation/internal/storage"
)

// defaultObjects は SPECIFICATION.md 20.2 のパッチ対象3 object。
const defaultObjects = "unprocessed_asins.json,upcoming_asins.json,notified_asins.json"

func main() {
	log.SetFlags(0)
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		log.Fatalf("migrate-maxprice: %v", err)
	}
}

// run は AWS 接続を含むため単体テストは migrateObject で行う。
func run(args []string, stdout, stderr *os.File) error {
	fs := flag.NewFlagSet("migrate-maxprice", flag.ContinueOnError)
	fs.SetOutput(stderr)
	bucket := fs.String("bucket", "", "対象 S3 bucket 名（必須）")
	region := fs.String("region", "", "AWS region（必須）")
	objectsCSV := fs.String("objects", defaultObjects, "対象 object key のカンマ区切り")
	apply := fs.Bool("apply", false, "変更を S3 へ適用する。未指定なら dry-run")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *bucket == "" || *region == "" {
		return fmt.Errorf("-bucket と -region は必須")
	}
	keys := splitKeys(*objectsCSV)
	if len(keys) == 0 {
		return fmt.Errorf("-objects が空")
	}

	ctx := context.Background()
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(*region))
	if err != nil {
		return fmt.Errorf("load aws config: %w", err)
	}
	store := storage.NewS3Store(s3.NewFromConfig(cfg), *bucket)

	mode := "dry-run"
	if *apply {
		mode = "apply"
	}
	fmt.Fprintf(stdout, "migrate-maxprice %s: bucket=%s region=%s objects=%v\n", mode, *bucket, *region, keys)

	var failed bool
	for _, key := range keys {
		rep, err := migrateObject(ctx, store, key, *apply)
		if err != nil {
			fmt.Fprintf(stderr, "ERROR %s: %v\n", key, err)
			failed = true
			continue
		}
		fmt.Fprintf(stdout, "%s: total=%d changed=%d current_zero=%d\n",
			rep.Key, rep.Total, rep.Changed, rep.CurrentZero)
	}
	if failed {
		return fmt.Errorf("一部 object の処理に失敗しました")
	}
	return nil
}

type objectReport struct {
	Key         string
	Total       int
	Changed     int
	CurrentZero int
}

func migrateObject(ctx context.Context, store storage.ObjectStore, key string, apply bool) (objectReport, error) {
	obj, err := store.Get(ctx, key)
	if err != nil {
		return objectReport{}, fmt.Errorf("get %s: %w", key, err)
	}
	before, err := storage.DecodeBooks(obj.Body)
	if err != nil {
		return objectReport{}, fmt.Errorf("decode %s: %w", key, err)
	}
	body, changed, currentZero, err := migrateJSON(obj.Body)
	if err != nil {
		return objectReport{}, fmt.Errorf("migrate %s: %w", key, err)
	}
	after, err := storage.DecodeBooks(body)
	if err != nil {
		return objectReport{}, fmt.Errorf("decode migrated %s: %w", key, err)
	}
	if err := validateInvariants(before, after, key); err != nil {
		return objectReport{}, err
	}
	if err := validateRawInvariants(obj.Body, body, key); err != nil {
		return objectReport{}, err
	}
	if apply && changed > 0 {
		if err := store.Put(ctx, key, body, storage.PutOptions{IfMatch: obj.ETag}); err != nil {
			return objectReport{}, fmt.Errorf("put %s: %w", key, err)
		}
	}
	return objectReport{Key: key, Total: len(before), Changed: changed, CurrentZero: currentZero}, nil
}

type rawField struct {
	value []byte
	start int
	end   int
}

func migrateJSON(body []byte) ([]byte, int, int, error) {
	var records []json.RawMessage
	if err := json.Unmarshal(body, &records); err != nil {
		return nil, 0, 0, err
	}
	changed := 0
	currentZero := 0
	for i, record := range records {
		fields, err := scanObject(record)
		if err != nil {
			return nil, 0, 0, fmt.Errorf("record %d: %w", i, err)
		}
		current, ok := fields["CurrentPrice"]
		if !ok {
			return nil, 0, 0, fmt.Errorf("record %d: CurrentPrice missing", i)
		}
		currentPrice, err := rawNumber(current.value)
		if err != nil {
			return nil, 0, 0, fmt.Errorf("record %d CurrentPrice: %w", i, err)
		}
		if currentPrice == 0 {
			currentZero++
		}
		max, exists := fields["MaxPrice"]
		if exists {
			maxPrice, err := rawNumber(max.value)
			if err != nil {
				return nil, 0, 0, fmt.Errorf("record %d MaxPrice: %w", i, err)
			}
			if maxPrice == currentPrice {
				continue
			}
			records[i] = append(append(append([]byte{}, record[:max.start]...), current.value...), record[max.end:]...)
			changed++
			continue
		}
		if currentPrice == 0 {
			continue
		}
		insertAt := len(record) - 1
		for insertAt > 0 && (record[insertAt-1] == ' ' || record[insertAt-1] == '\n' || record[insertAt-1] == '\r' || record[insertAt-1] == '\t') {
			insertAt--
		}
		addition := append([]byte(`,"MaxPrice":`), current.value...)
		records[i] = append(append(append([]byte{}, record[:insertAt]...), addition...), record[insertAt:]...)
		changed++
	}

	var compact bytes.Buffer
	compact.WriteByte('[')
	for i, record := range records {
		if i > 0 {
			compact.WriteByte(',')
		}
		compact.Write(record)
	}
	compact.WriteByte(']')
	var indented bytes.Buffer
	if err := json.Indent(&indented, compact.Bytes(), "", "    "); err != nil {
		return nil, 0, 0, err
	}
	return indented.Bytes(), changed, currentZero, nil
}

func scanObject(record []byte) (map[string]rawField, error) {
	dec := json.NewDecoder(bytes.NewReader(record))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return nil, fmt.Errorf("JSON objectではありません")
	}
	fields := make(map[string]rawField)
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := tok.(string)
		if !ok {
			return nil, fmt.Errorf("field名が文字列ではありません")
		}
		if _, duplicate := fields[key]; duplicate {
			return nil, fmt.Errorf("field %s が重複しています", key)
		}
		pos := int(dec.InputOffset())
		for pos < len(record) && (record[pos] == ' ' || record[pos] == '\n' || record[pos] == '\r' || record[pos] == '\t') {
			pos++
		}
		if pos >= len(record) || record[pos] != ':' {
			return nil, fmt.Errorf("field %s の区切りが不正です", key)
		}
		pos++
		for pos < len(record) && (record[pos] == ' ' || record[pos] == '\n' || record[pos] == '\r' || record[pos] == '\t') {
			pos++
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, fmt.Errorf("field %s: %w", key, err)
		}
		fields[key] = rawField{value: value, start: pos, end: pos + len(value)}
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	return fields, nil
}

func rawNumber(raw []byte) (float64, error) {
	var value float64
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, err
	}
	return value, nil
}

func validateRawInvariants(beforeBody, afterBody []byte, key string) error {
	var before, after []map[string]json.RawMessage
	if err := json.Unmarshal(beforeBody, &before); err != nil {
		return fmt.Errorf("%s: decode raw before: %w", key, err)
	}
	if err := json.Unmarshal(afterBody, &after); err != nil {
		return fmt.Errorf("%s: decode raw after: %w", key, err)
	}
	if len(before) != len(after) {
		return fmt.Errorf("%s: raw record count changed", key)
	}
	for i := range before {
		for field, value := range before[i] {
			if field == "MaxPrice" {
				continue
			}
			got, ok := after[i][field]
			if !ok || !rawJSONEqual(value, got) {
				return fmt.Errorf("%s: field %s changed at record %d", key, field, i)
			}
		}
		for field := range after[i] {
			if field != "MaxPrice" {
				if _, ok := before[i][field]; !ok {
					return fmt.Errorf("%s: field %s added at record %d", key, field, i)
				}
			}
		}
	}
	return nil
}

// applyMigration は SPECIFICATION.md 20.2。CurrentPrice 未取得（0円）時は MaxPrice も未取り。
// 件数・ASIN・他 field は一切変更せず新しい slice を返す。
func applyMigration(records []storage.BookRecord) ([]storage.BookRecord, int, int) {
	migrated := make([]storage.BookRecord, len(records))
	changed := 0
	currentZero := 0
	for i, r := range records {
		if !priceEqual(r.Book.MaxPrice, r.Book.CurrentPrice) {
			changed++
		}
		if !r.Book.CurrentPrice.Valid() {
			currentZero++
		}
		migrated[i] = r
		migrated[i].Book.MaxPrice = r.Book.CurrentPrice
	}
	return migrated, changed, currentZero
}

func validateInvariants(before, after []storage.BookRecord, key string) error {
	if len(before) != len(after) {
		return fmt.Errorf("%s: record count changed %d -> %d", key, len(before), len(after))
	}
	if !setEqual(asinSet(before), asinSet(after)) {
		return fmt.Errorf("%s: ASIN set changed", key)
	}
	for i := range before {
		b := before[i].Book
		a := after[i].Book
		if b.ASIN != a.ASIN {
			return fmt.Errorf("%s: ASIN order changed at %d", key, i)
		}
		if !priceEqual(b.CurrentPrice, a.CurrentPrice) {
			return fmt.Errorf("%s: CurrentPrice changed for %s", key, b.ASIN)
		}
		if !priceEqual(a.MaxPrice, a.CurrentPrice) {
			return fmt.Errorf("%s: MaxPrice != CurrentPrice for %s after migration", key, b.ASIN)
		}
		if b.Title != a.Title || b.URL != a.URL || !b.ReleaseDate.Equal(a.ReleaseDate) || !b.CreatedAt.Equal(a.CreatedAt) {
			return fmt.Errorf("%s: non-price field changed for %s", key, b.ASIN)
		}
		if !extraEqual(before[i].Extra, after[i].Extra) {
			return fmt.Errorf("%s: Extra changed for %s", key, b.ASIN)
		}
	}
	return nil
}

func priceEqual(a, b book.Price) bool {
	return a.Valid() == b.Valid() && a.Yen() == b.Yen()
}

func asinSet(records []storage.BookRecord) map[string]struct{} {
	m := make(map[string]struct{}, len(records))
	for _, r := range records {
		m[r.Book.ASIN] = struct{}{}
	}
	return m
}

func setEqual(a, b map[string]struct{}) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if _, ok := b[k]; !ok {
			return false
		}
	}
	return true
}

func extraEqual(a, b map[string]json.RawMessage) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		bv, ok := b[k]
		if !ok {
			return false
		}
		if !rawJSONEqual(v, bv) {
			return false
		}
	}
	return true
}

func rawJSONEqual(a, b []byte) bool {
	var compactA, compactB bytes.Buffer
	if json.Compact(&compactA, a) != nil || json.Compact(&compactB, b) != nil {
		return false
	}
	return bytes.Equal(compactA.Bytes(), compactB.Bytes())
}

func splitKeys(csv string) []string {
	parts := strings.Split(csv, ",")
	keys := make([]string, 0, len(parts))
	for _, p := range parts {
		k := strings.TrimSpace(p)
		if k != "" {
			keys = append(keys, k)
		}
	}
	return keys
}
