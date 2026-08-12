# 本番開始前チェックリスト（リリースゲート）

本書は現行コードと本番環境に対する本番開始前のチェックリスト（リリースゲート）である。
業務仕様は `SPECIFICATION.md` を正とし、本書は検証項目と実行コマンドを示す。
運用手順の詳細は `docs/operations.md`、本番 Lambda 構成は `AGENTS.md` §1 を参照。

## 1. ローカル品質ゲート（offline）

デプロイ前に次を実行し、全て通過すること（`AGENTS.md` §12、`docs/operations.md` §1.1）。
これらは実 AWS・Amazon・Slack・Mastodon・GitHub へアクセスしない offline 検証である（`AGENTS.md` §11）。

```bash
gofmt -l $(rg --files -g '*.go')
go test ./...
go vet ./...
staticcheck ./...
govulncheck ./...
sam validate --template-file infra/template.yaml --lint
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o /dev/null ./cmd/schedule-checks
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o /dev/null ./cmd/check-worker
```

- `gofmt -l` の差分がないこと。
- test / vet / staticcheck / govulncheck の error と warning が 0 件であること。
- `sam validate --lint` (cfn-lint) が `infra/template.yaml` の schema・property 型・`!GetAtt` 参照整合性を offline で検証する（AWS profile/region 不要）。`infra/template_test.go` は一般 schema を再実装せずプロジェクト固有契約のみを検証するため、この公式検証で補う。`make check` (`make sam-validate`) でも同じ検証が走る。
- 提出前確認では `go test -race ./...` を使う。
- 各 tool が未導入の場合は確認済み扱いせず、未実行理由を明示する（`AGENTS.md` §12）。

## 2. live smoke（明示 opt-in、外部通信）

`go test ./...` には含まれない、実 Amazon.co.jp へ接続する live smoke を置いている。
build tag `livesmoke` を付けたときだけ compile され、通常の offline gate からは完全に除外される
（`internal/amazon/live_smoke_test.go`、`AGENTS.md` §11）。

```bash
go test -tags=livesmoke -run 'TestLiveSmoke' ./internal/amazon/
```

- 役割: 実HTMLに対する selector 有効性・通信到達性・要求 ASIN 一致・Kindle 価格が正値 など、
  変動しない安定構造だけを検証する。変動値（価格絶対値・ポイント・クーポンの有無と値）は固定 assert しない。
- block/CAPTCHA や要件不満足時は分類結果と取得できなかった事実を報告し、assert を黙って弱めて通過させない。
- 第三の本番 Lambda entrypoint は追加せず、production の `internal/amazon` 実装をそのまま通す。
- 外部通信が必要なため CI・offline gate には含めず、任意実行とする。
- 時点固定の観測結果（特定 commit・date での実行結果）を記録する場合は commit/date を明示し、
  恒久的な gate 要件へ混ぜない。

## 3. コード不変条件（仕様カバレッジ）

現行コードが満たすべき不変条件。該当ファイルを実装の正として確認する。

| SPEC 範囲 | 確認内容 | 主な確認先 |
|---|---|---|
| §5.3 / §11.1 | User-Agent, Accept, Accept-Language, 15秒 timeout, redirect 上限5, host 検証, 8 MiB body 上限, Cookie/Authorization 非送信 | `internal/amazon/client.go` |
| §6 | EventBridge Scheduler 入力 JSON 形式, cron, `cycle_id` 生成 | `infra/template.yaml`, `internal/domain/scheduling`, `internal/lambda/schedulechecks/dto.go` |
| §7 | MessageGroupId(amazon-requests/external-updates), MessageDeduplicationId=SHA-256 hex, BatchSize=1, SendMessageBatch 10件単位, sale_finalize の最終単独送信 | `internal/job/job.go`, `internal/queue/queue.go`, `internal/application/dispatch/dispatch.go` |
| §9.2 / §9.3 | 4スペース indent, `&` 非変換, 発売日降順/同日タイトル昇順, 未知 field 保持 | `internal/storage/codec.go` |
| §9.5 / §10 | If-Match 条件付き書き込み, 412 時最大3回再 merge, Upcoming ETag 不変時のみ空配列化 | `internal/storage/merge.go`, `internal/storage/highlevel.go` |
| §11.3 | 404/asin_mismatch/not_kindle/permanent 4xx=terminal, 403/429/5xx/timeout/CAPTCHA/body超過/構造欠落/解析失敗=retryable | `internal/amazon/client.go`, `internal/amazon/response.go`, 各 application |
| §12.3 / §12.4 / §12.5 | セール4条件の独立性, 紙書籍価格不使用, セール成立時も MaxPrice/CurrentPrice 更新, 価格変動通知との排他 | `internal/domain/sale/sale.go`, `internal/domain/book/book.go`, `internal/application/sale/sale.go` |
| §13.3 | 対象作者名と contributor 表記を正規化した完全名同士で完全一致比較する。空白トークン部分一致は行わず、姓だけ同一の別人を誤検出しない | `internal/application/newrelease/newrelease.go` |
| §13 | 検索→result/detail の2段階, 検索 job は S3 保存・通知しない | `internal/application/newrelease/newrelease.go` |
| §7.2 / §13.4 | `new_release_result` の `product.item_type` は正規値 `kindle` のみ許可。空文字・未知値は `ErrInvalidItemType` で Amazon 到達前に拒否し、推測値・空文字を入れない | `internal/job/job.go` |
| §15 | S3 全体から Gist 再生成, gist_type ごとの決定 ID | `internal/gist/gist.go`, `internal/gist/markdown.go` |
| §17.2 | Alarm 3種が ALARM 遷移時のみ schedule-checks 起動, OKActions なし | `infra/template.yaml`, `internal/lambda/schedulechecks/handler.go` |
| §18.1 | 共通ログ field 一式, ErrorCount metric filter | `internal/lambda/checkworker/handler.go`, `infra/template.yaml` |
| §19 | SSM secure→plain fallback, GetParametersByPath 不使用, IAM 関数別 | `internal/config/ssm.go`, `infra/template.yaml` |

上記に違反する実装がないことを確認する。

## 4. 既知の未決定・未検証項目

| 項目 | SPEC | 現状と検証条件 |
|---|---|---|
| CAPTCHA / 404 / 必須要素欠落 の実HTML fixture | §22.2 | Kindle商品ページ(`testdata/amazon/product_B0FX3X569X.html`)・紙商品ページ(`testdata/amazon/paper_4434361325.html`)・検索ページ(`testdata/amazon/search_digital_text.html`)は実HTMLを保存済みで `internal/amazon/extractor_test.go` が使用中。仍未取得なのは SPEC §22.2 の CAPTCHA/アクセス拒否/短い200本文・404商品不存在・必須価格・タイトル欠落。自動テストから Amazon へアクセスできないため、これらの構造検証は live smoke(§2) と実fixture 追加が条件。 |

## 5. AWS 環境でのみ検証する項目

コード単体では完結しない項目。`SPECIFICATION.md` §20.3 切り替え手順・§24 step12 に沿って実施する。

### 5.1 SAM デプロイ・CFn 検証
- [ ] template schema・property 型・`!GetAtt` 参照整合性は offline ゲートの `sam validate --template-file infra/template.yaml --lint` (cfn-lint) で検証済み（§1 / `make sam-validate`）。AWS 環境では sam build / sam deploy が通ることで live 妥当性を確認する。template は `infra/template.yaml` にあるため手動 validate 時は `--template-file` 必須（省略時の default `template.yaml` は repo root に不存在）。
- [ ] `./scripts/deploy.sh --stage build`（`--profile`/`--region` 必須）で sam build が成功すること。
- [ ] `--stage deploy` が sam deploy の change set 確認プロンプトを表示し、確認後に同じ sam deploy で適用すること（`scripts/deploy_test.sh` で stub 検証済み）。
- [ ] `--stage all` が sam build 後に sam deploy を実行すること（`scripts/deploy_test.sh` で stub 検証済み）。
- [ ] SAM BuildMethod: makefile が両 Lambda の `bootstrap` を生成すること（ローカル Makefile target で生成済み、sam build での連結は AWS 側で確認）。
- [ ] deploy 後、2 Log Group(保持30日), Work/DLQ FIFO, Scheduler DLQ Standard, MetricFilter→ErrorCount, Alarm 3種, Role 3種が作成されること。

### 5.2 EventBridge Scheduler
- [ ] Scheduler 実行時に `<aws.scheduler.scheduled-time>` が入力 JSON へ展開されること。
- [ ] 同一 Scheduler 再試行で `cycle_id` が同一になること。
- [ ] `SchedulersEnabled` default false で3 Scheduler が無効状態で作成されること。
- [ ] Scheduler RetryPolicy(maxRetry=3, maxEventAge=240), Scheduler DLQ が設定どおりこと。

### 5.3 SQS / S3 / SSM 実動作
- [ ] Work Queue の MessageGroupId `amazon-requests` 排他で Amazon リクエストが直列化されること。
- [ ] maxReceiveCount=5 到達で DLQ へ移行すること。
- [ ] S3 `If-Match` 条件付き書き込みで 412 発生時、最大3回再 merge されること（MemStore テスト済み、実S3で確認）。
- [ ] SSM `/myapp/secure/{KEY}` → `ParameterNotFound` 時 `/myapp/plain/{KEY}` へ fallback すること。SecureString が customer managed KMS の場合は `kms:Decrypt` 追加要否を確認。

### 5.4 Alarm → 通知連携
- [ ] Work DLQ / Scheduler DLQ / Work Queue 滞留(7200秒) の各 Alarm が ALARM 遷移時に `schedule-checks` を起動すること。
- [ ] 同じ Alarm 状態で通知が増殖しないこと。
- [ ] OK 遷移では `schedule-checks` を起動しないこと。

### 5.5 実 HTTP・外部API
- [ ] Lambda 環境から Amazon.co.jp への到達性と、実HTMLに対する各 selector の有効性（検索発売日 `nth-child`, CAPTCHA/access-denied marker）。Kindle商品・紙商品・検索ページの selector は実fixture(`testdata/amazon`)で検証済み。CAPTCHA/access-denied marker と 404 構造は live smoke(§2) と §4 の未取得 fixture 追加後に完結する。
- [ ] Slack/Mastodon/GitHub Gist API への実際の送信・更新。

### 5.6 切り替え手順（§20.3）
- [ ] Scheduler 無効状態で deploy。
- [ ] 対象 bucket の Versioning が `Enabled` であることを確認（read-only）。`Suspended`/未設定なら切り替え中止。確認には `s3:GetBucketVersioning` 権限のある profile が必要（権限不足の profile では AccessDenied で確認できない）。
- [ ] Versioning=Enabled を確認したら backup prefix copy は作らず、対象 object の現時点 VersionId を記録（docs/operations.md §5.0）。
- [ ] 既存3 Checker の EventBridge trigger を無効化。
- [ ] `migrate-checker-config` で `checker_configs.json` を dry-run → apply（§20.5）。`go run ./cmd/migrate-checker-config -bucket ... -region ... [-key checker_configs.json]`（dry-run 既定）で `NewReleaseChecker.MinPrice=221` 追加と §16 旧 field 削除を確認後、`-apply` を明示指定して実行（取得時 ETag の `If-Match` 付き）。MaxPrice 初期化・Scheduler 有効化より前に必須（新 Scheduler は `MinPrice` 必須で起動 validation するため）。Versioning で既に保護されているため別途 object 全体 backup は要求しない。
- [ ] `migrate-maxprice` dry-run → apply（§20.2, docs/operations.md §5）。
- [ ] 既知HTML fixture で新Lambda 確認。
- [ ] 手動 invoke で SQS から1件ずつ疎通確認。
- [ ] event source mapping 有効化 → 3 Scheduler 有効化。
- [ ] 既存 `release-notifier` が有効であること。

### 5.7 ISBN紙書籍候補のmanual疎通（SchedulersEnabled=false・WorkerMappingEnabled）

ISBN形式Paper候補のrecent除外（§13.4）を含む「実作者 → `new_release_search` → `new_release_paper_detail` → `paper_books_asins.json`保存」1本を、本番S3へ書き込みつつ段階制御下で手動確認する手順。自動 live smoke（§2）は Amazon への読み取りのみで S3 を書かないため、この経路の S3 書込は本手動手順でのみ確認する。実行そのものは本監査時点では行わず、実行前に下記を読み合わせておく。

前提（安全な段階制御）:
- `SchedulersEnabled=false` で deploy 済み（3 Scheduler は無効。自動周期では動かない）。
- `check-worker` の event source mapping が `WorkerMappingEnabled` の指定どおり（有効化して実施する。`docs/operations.md` §5.1 の `list-event-source-mappings` で `State`/`BatchSize=1` を確認）。
- 対象 bucket の Versioning が `Enabled`（§5.6）。本手順の S3 書込は Versioning で保護され、別 backup copy は作らない。
- `NewReleaseChecker.MinPrice` が apply 済み（§5.6）。`NewReleaseChecker.Enabled=true`。

実行前に read-only で記録する（書き込まない）:
- 対象作者 A と、検索結果に含まれる見込みの ISBN Paper 候補 ASIN（10〜13桁数字）を1件選ぶ。候補が recent窓（JST直近7日）内かつ MinPrice 以上になる作者・ASINを選ぶと保存まで確認できる。窓より古い ASIN を選んだ場合は recent除外のterminal結果を確認する。
- 次の object の現時点 VersionId と size を記録する: `paper_books_asins.json`、`authors.json`、`notified_asins.json`、`upcoming_asins.json`、`unprocessed_asins.json`。
  ```bash
  aws s3api list-object-versions --bucket <BUCKET> --prefix paper_books_asins.json \
    --query 'Versions[0].[VersionId,Size,LastModified]' --profile <P> --region <R>
  # 同様に authors/notified/upcoming/unprocessed に対しても記録
  ```
- `paper_books_asins.json` の本文を read-only で取得し、対象 ASIN の有無・既存レコードの件数と並び順・未知 field の有無を記録しておく。

実行（1本の疎通）:
- scope を1作者へ絞るため、`schedule-checks` を全文一括 dispatch せず、対象作者 A の `new_release_search` 1件だけ SQS へ投入する（job schema は §8、`MessageGroupId=amazon-requests`、`MessageDeduplicationId` = `job_id` の SHA-256 hex）。投入は `aws sqs send-message` で行う。
- event source mapping が有効なら `check-worker` が `new_release_search` を処理し、ISBN候補を `new_release_paper_detail` へ投入し、続いて同 detail を処理して `paper_books_asins.json` を upsert する。
- 一括確認でよい場合は `aws lambda invoke --function-name <ScheduleChecksFunction> ...` で `new_release` 1周期を手動起動し、対象作者 A の経路だけ下記で検証する（他作者の経路は今回の検証対象外）。

実行後に read-only で検証する（assert）:
- `paper_books_asins.json` の VersionId が変化したか（書込発生の有無）。recent・MinPrice 両方を満たす候補なら VersionId が変わり、対象 ASIN が1件 upsert されている。recent外なら VersionId は変化せず対象 ASIN は不存在のまま。
- upsertされた対象レコードの: 作者が A に一致、発売日が JST直近7日内（recent）、`CurrentPrice`/`MaxPrice` が MinPrice 以上または 0（unknown）、`CreatedAt`・4スペース indent・発売日降順の schema（§9.2）が維持されること。
- 記録しておいた他の手動 record（別 ASIN）が件数・内容ともに保持されていること。未知 field が削られていないこと。
- `notified_asins.json`・`upcoming_asins.json`・`unprocessed_asins.json` の VersionId が実行前から不変（ISBN Paper経路はこれらへ触れない、§13.4）。対象 ASIN がこれらに含まれないこと。
- `authors.json` の `LatestRelease` が対象作者 A で更新されていないこと（ISBN Paper経路は `authors.LatestRelease` を更新しない、§13.4）。VersionId も不変が期待される（他作者の Kindle 経路が同時に動かなければ）。
- `paper_to_kindle` の `gist_update` が決定的 job_id（`paper_to_kindle:nr_paper:<paperASIN>`）で投入されたこと（DLQ や SQS heap の read-only 確認、または次の Gist 実行結果）。
- recent外 ASIN を選んだ場合は、当該 job のログ `error_type=paper_recent_excluded`・`result=job_terminal` を確認し、paper_books 保存も Gist 投入も起きていないことを上記と同じ方法で検証する。

recovery:
- 書込結果が想定と異なる場合は、記録した VersionId を使って Versioning から対象 object の旧 version を参照・復元する（§20.4）。別 backup copy は要求しない。

