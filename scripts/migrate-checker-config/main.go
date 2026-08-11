// migrate-checker-config は checker_configs.json の一回限り移行 CLI（SPECIFICATION.md 16, 20.5）。
// §16 の旧 field を削除し、NewReleaseChecker.MinPrice が未設定なら 221 を追加する。
// 未知の field・未知の Checker section は出力へ保持する（key 並び順・空白は正規化される）。
// デフォルトは dry-run で、-apply 指定時だけ取得時 ETag の If-Match 条件付きで書き込む（AGENTS.md 13）。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/shinderuman/kindle-automation/internal/config"
	"github.com/shinderuman/kindle-automation/internal/storage"
)

// defaultMinPrice は UserScript MIN_PRICE に合わせた新刊候補の最低価格（円）（SPECIFICATION.md 13.3, 16）。
const defaultMinPrice = 221

const defaultKey = "checker_configs.json"

var topLevelRemove = []string{"ReportFailure"}

var perCheckerRemove = map[string][]string{
	"SaleChecker":          {"ExecutionIntervalMinutes", "GetItemsPaapiRetryCount", "GetItemsInitialRetrySeconds"},
	"NewReleaseChecker":    {"CycleDays", "SearchItemsPaapiRetryCount", "SearchItemsInitialRetrySeconds", "GetItemsPaapiRetryCount", "GetItemsInitialRetrySeconds"},
	"PaperToKindleChecker": {"CycleDays", "SearchItemsPaapiRetryCount", "SearchItemsInitialRetrySeconds", "GetItemsPaapiRetryCount", "GetItemsInitialRetrySeconds"},
}

func main() {
	log.SetFlags(0)
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		log.Fatalf("migrate-checker-config: %v", err)
	}
}

func run(args []string, stdout, stderr *os.File) error {
	fs := flag.NewFlagSet("migrate-checker-config", flag.ContinueOnError)
	fs.SetOutput(stderr)
	bucket := fs.String("bucket", "", "対象 S3 bucket 名（必須）")
	region := fs.String("region", "", "AWS region（必須）")
	key := fs.String("key", defaultKey, "対象 object key")
	apply := fs.Bool("apply", false, "変更を S3 へ適用する。未指定なら dry-run")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *bucket == "" || *region == "" {
		return fmt.Errorf("-bucket と -region は必須")
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
	fmt.Fprintf(stdout, "migrate-checker-config %s: bucket=%s region=%s key=%s\n", mode, *bucket, *region, *key)

	rep, err := migrateObject(ctx, store, *key, *apply)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s: removed_top_level=%d removed_per_checker=%d min_price_added=%t\n",
		rep.Key, rep.RemovedTopLevel, rep.RemovedPerChecker, rep.MinPriceAdded)
	return nil
}

type objectReport struct {
	Key               string
	RemovedTopLevel   int
	RemovedPerChecker int
	MinPriceAdded     bool
}

func migrateObject(ctx context.Context, store storage.ObjectStore, key string, apply bool) (objectReport, error) {
	obj, err := store.Get(ctx, key)
	if err != nil {
		return objectReport{}, fmt.Errorf("get %s: %w", key, err)
	}
	migrated, rep, err := applyMigration(obj.Body)
	if err != nil {
		return objectReport{}, err
	}
	// 書込前に checker 契約の decode + Validate を通す（SPECIFICATION.md 20.5）。
	if err := validateMigrated(migrated); err != nil {
		return objectReport{}, fmt.Errorf("%s: %w", key, err)
	}
	if apply {
		if err := store.Put(ctx, key, migrated, storage.PutOptions{IfMatch: obj.ETag}); err != nil {
			return objectReport{}, fmt.Errorf("put %s: %w", key, err)
		}
	}
	rep.Key = key
	return rep, nil
}

func validateMigrated(body []byte) error {
	checker, err := config.DecodeCheckerConfigs(body)
	if err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	if err := checker.Validate(); err != nil {
		return fmt.Errorf("validate: %w", err)
	}
	return nil
}

// applyMigration は top-level を map[string]json.RawMessage で扱い旧 field を削除する。
// RawMessage で扱うことで修正対象の Checker section 以外の未知 field を保持する（SPECIFICATION.md 20.5）。
func applyMigration(body []byte) ([]byte, objectReport, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		return nil, objectReport{}, fmt.Errorf("decode top: %w", err)
	}
	rep := objectReport{}
	for _, field := range topLevelRemove {
		if _, ok := top[field]; ok {
			delete(top, field)
			rep.RemovedTopLevel++
		}
	}
	for checker, fields := range perCheckerRemove {
		raw, ok := top[checker]
		if !ok {
			continue
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, objectReport{}, fmt.Errorf("decode %s: %w", checker, err)
		}
		changed := false
		for _, field := range fields {
			if _, ok := m[field]; ok {
				delete(m, field)
				rep.RemovedPerChecker++
				changed = true
			}
		}
		if checker == "NewReleaseChecker" {
			if added, err := ensureMinPrice(m); err != nil {
				return nil, objectReport{}, err
			} else if added {
				rep.MinPriceAdded = true
				changed = true
			}
		}
		if changed {
			enc, err := json.Marshal(m)
			if err != nil {
				return nil, objectReport{}, fmt.Errorf("encode %s: %w", checker, err)
			}
			top[checker] = enc
		}
	}
	out, err := json.Marshal(top)
	if err != nil {
		return nil, objectReport{}, fmt.Errorf("encode top: %w", err)
	}
	return out, rep, nil
}

func ensureMinPrice(m map[string]json.RawMessage) (bool, error) {
	if _, ok := m["MinPrice"]; ok {
		return false, nil
	}
	enc, err := json.Marshal(defaultMinPrice)
	if err != nil {
		return false, fmt.Errorf("encode MinPrice: %w", err)
	}
	m["MinPrice"] = enc
	return true, nil
}
