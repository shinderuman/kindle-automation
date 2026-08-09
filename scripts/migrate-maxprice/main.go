// migrate-maxprice は SPECIFICATION.md 20.2 の MaxPrice 初期化パッチを適用する一回限りの移行 CLI。
//
// UserScript の Option+↑ で生成された既存 MaxPrice には紙書籍価格が入っており、
// 過去最高 Kindle 価格として使えないため、MaxPrice = CurrentPrice へ置き換える。
// 対象は unprocessed_asins.json / upcoming_asins.json / notified_asins.json の3 object。
//
// デフォルトは dry-run。-apply を明示指定した場合だけ S3 へ書き込む（AGENTS.md 13）。
// 件数・ASIN 集合・CurrentPrice・他 field が変わらないことを適用前後に検証する。
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

// run は CLI 引数を解析し、各 object へ移行を適用する。AWS 接続を含むため単体テストは migrateObject で行う。
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

// objectReport は1 object の移行結果。
type objectReport struct {
	Key         string
	Total       int
	Changed     int
	CurrentZero int
}

// migrateObject は1 object を読み込み、MaxPrice=CurrentPrice へ書き換える。
// apply=false なら検証と差分計算だけ行い S3 へ書き込まない。
// 件数・ASIN 集合・CurrentPrice・他 field の不変を検証し、違反時は error を返す。
func migrateObject(ctx context.Context, store storage.ObjectStore, key string, apply bool) (objectReport, error) {
	obj, err := store.Get(ctx, key)
	if err != nil {
		return objectReport{}, fmt.Errorf("get %s: %w", key, err)
	}
	records, err := storage.DecodeBooks(obj.Body)
	if err != nil {
		return objectReport{}, fmt.Errorf("decode %s: %w", key, err)
	}
	migrated, changed, currentZero := applyMigration(records)
	if err := validateInvariants(records, migrated, key); err != nil {
		return objectReport{}, err
	}
	body, err := storage.EncodeBooks(migrated)
	if err != nil {
		return objectReport{}, fmt.Errorf("encode %s: %w", key, err)
	}
	if apply {
		if err := store.Put(ctx, key, body, storage.PutOptions{IfMatch: obj.ETag}); err != nil {
			return objectReport{}, fmt.Errorf("put %s: %w", key, err)
		}
	}
	return objectReport{Key: key, Total: len(records), Changed: changed, CurrentZero: currentZero}, nil
}

// applyMigration は各レコードの MaxPrice を CurrentPrice へ置き換える（SPECIFICATION.md 20.2）。
// CurrentPrice が未取得（0円）の場合は MaxPrice も未取得となり Yen() は 0 を返す。
// 件数・ASIN・他 field は一切変更せず、新しい slice を返す。
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

// validateInvariants は MaxPrice 以外が変わっていないことと、適用後に MaxPrice==CurrentPrice であることを検証する。
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

// priceEqual は2つの Price が valid と yen で一致するかを返す。
func priceEqual(a, b book.Price) bool {
	return a.Valid() == b.Valid() && a.Yen() == b.Yen()
}

// asinSet は records の ASIN 集合を返す。
func asinSet(records []storage.BookRecord) map[string]struct{} {
	m := make(map[string]struct{}, len(records))
	for _, r := range records {
		m[r.Book.ASIN] = struct{}{}
	}
	return m
}

// setEqual は2つの文字列集合が等しいかを返す。
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

// extraEqual は未知 field の map が等しいかを返す。
func extraEqual(a, b map[string]json.RawMessage) bool {
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

// splitKeys はカンマ区切り文字列を trim 済みの非空 key 一覧へ分割する。
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
