// Package checkerconfig は checker_configs.json の一回限り移行 CLI の実処理を担う（SPECIFICATION.md 16, 20.5）。
//
// §16 の旧 field を削除し、NewReleaseChecker.MinPrice が未設定なら 221 を追加する。
// 未知の field・未知の Checker section は出力へ保持する（key 並び順・空白は正規化される）。
// 書込前に移行後本文が DecodeCheckerConfigs + Validate へ通ることを検証する。
// デフォルトは dry-run で、-apply 指定時だけ取得時 ETag の If-Match 条件付きで書き込む（AGENTS.md 13）。
package checkerconfig

import (
	"context"
	"flag"
	"fmt"
	"io"
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

type report struct {
	Key               string
	RemovedTopLevel   int
	RemovedPerChecker int
	MinPriceAdded     bool
}

// Run は CLI のエントリポイント。args を解析し、指定 S3 object の移行を行う。
// -apply 未指定時は dry-run となり S3 へ書き込まず、-bucket と -region は必須。
// AWS profile は CLI flag ではなく AWS_PROFILE 環境変数・shared config 経由で解決する。
func Run(args []string, stdout, stderr io.Writer) error {
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
	store, err := newStore(ctx, *region, *bucket)
	if err != nil {
		return err
	}

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
