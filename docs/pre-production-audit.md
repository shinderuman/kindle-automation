# 本番稼働前監査結果と AWS 疎通 checklist

本番稼働前のローカル完了工程における `SPECIFICATION.md` 全項目監査の結果と、
実 AWS 環境だけで完結する項目の疎通 checklist。業務仕様は `SPECIFICATION.md` を正とする。

## 1. 監査サマリ（§1〜§27）

実装済みかつ問題なしと確認した主な領域（証拠は該当ファイル）:

| SPEC 範囲 | 確認内容 | 主な確認先 |
|---|---|---|
| §5.3 / §11.1 | User-Agent, Accept, Accept-Language, 15秒 timeout, redirect 上限5, host 検証, 8 MiB body 上限, Cookie/Authorization 非送信 | `internal/amazon/client.go` |
| §6 | EventBridge Scheduler 入力 JSON 形式, cron, `cycle_id` 生成 | `infra/template.yaml`, `internal/domain/scheduling`, `internal/lambda/schedulechecks/dto.go` |
| §7 | MessageGroupId(amazon-requests/external-updates), MessageDeduplicationId=SHA-256 hex, BatchSize=1, SendMessageBatch 10件単位, sale_finalize の最終単独送信 | `internal/job/job.go`, `internal/queue/queue.go`, `internal/application/dispatch/dispatch.go` |
| §9.2 / §9.3 | 4スペース indent, `&` 非変換, 発売日降順/同日タイトル昇順, 未知 field 保持 | `internal/storage/codec.go` |
| §9.5 / §10 | If-Match 条件付き書き込み, 412 時最大3回再 merge, Upcoming ETag 不変時のみ空配列化 | `internal/storage/merge.go`, `internal/storage/highlevel.go` |
| §11.3 | 404/asin_mismatch/not_kindle/permanent 4xx=terminal, 403/429/5xx/timeout/CAPTCHA/body超過/構造欠落/解析失敗=retryable | `internal/amazon/client.go`, `internal/amazon/response.go`, 各 application |
| §12.3 / §12.4 / §12.5 | セール4条件の独立性, 紙書籍価格不使用, セール成立時も MaxPrice/CurrentPrice 更新, 価格変動通知との排他 | `internal/domain/sale/sale.go`, `internal/domain/book/book.go`, `internal/application/sale/sale.go` |
| §13 | 検索→result/detail の2段階, 検索 job は S3 保存・通知しない | `internal/application/newrelease/newrelease.go` |
| §15 | S3 全体から Gist 再生成, gist_type ごとの決定 ID | `internal/gist/gist.go`, `internal/gist/markdown.go` |
| §17.2 | Alarm 3種が ALARM 遷移時のみ schedule-checks 起動, OKActions なし | `infra/template.yaml`, `internal/lambda/schedulechecks/handler.go` |
| §18.1 | 共通ログ field 一式, ErrorCount metric filter | `internal/lambda/checkworker/handler.go`, `infra/template.yaml` |
| §19 | SSM secure→plain fallback, GetParametersByPath 不使用, IAM 関数別 | `internal/config/ssm.go`, `infra/template.yaml` |

致命的な仕様不一致は検出されなかった。

## 2. 本パスで修正した項目

| 項目 | SPEC | 修正内容 |
|---|---|---|
| 新刊作者名一致判定 | §13.3 | `AuthorMatches` を「含む」(Contains) 判定へ修正。SPEC と既存Go実装(`isNameMatched`)が一致し、現行実装は完全一致へ逸脱していた。テスト追加。 |
| body 超過時の HTTP 計測値 | §11.3 / §18.1 | body 8 MiB 超過を retryable 結果へ変換し `http_status`/`response_bytes` を保持。従来は空 `FetchResult` + error で計測値が 0 になった。テスト更新。 |
| §22.2 fixture test の不足 | §22.2 | 検索発売日あり/なし, Kindleスウォッチ欠落, ポイントなし, CAPTCHA 由来明示を追加。実HTML不可分は最小合成fixtureで分類を保証。 |
| govulncheck 標準ライブラリ脆弱性 | §12 / §23 | 13件(すべて go1.25.5 標準ライブラリ)を `toolchain go1.25.12` で解消。 |
| `.golangci.yml` の §2 記載 | AGENTS.md §2 / §12 | §12 品質コマンドは golangci-lint を含まずファイルも不存在のため、§2 ツリーの `.golangci.yml` 行を削除し実態へ整合。 |

## 3. 判断待ち（ローカル修正可能だが仕様判断を保留）

| 項目 | SPEC | 現状と保留理由 |
|---|---|---|
| `SearchProduct.ItemType` 未使用 | §7.2 / §13.4 | `job.SearchProduct.ItemType` は定義されるが値の enum が SPEC に未定義で、現状 `IsKindle=true`(digital-text 固定)が同等情報を担う。非機能的影響なし。推測値の設定や schema 削除は仕様判断待ち。 |
| 実HTML fixture の拡充 | §22.2 | 検索ページ/CAPTCHA/404等の実HTMLは test 環境に不存在。自動テストから Amazon へアクセスできないため、最小合成fixture で代用している。実HTML構造の検証は実fixture取得後。 |

## 4. AWS 環境だけで必要な疎通 checklist

コードでは完結しない項目。`SPECIFICATION.md` §20.3 切り替え手順・§24 step12 に沿って実施する。

### 4.1 SAM デプロイ・CFn 検証
- [ ] SAM CLI 導入後、`sam validate --profile <P> --region <R>` で template 妥当性確認（本工程では SAM CLI 未導入のため未実行）。
- [ ] `./scripts/deploy.sh --stage build/package/changeset` で change set を作成し、内容を確認。
- [ ] SAM BuildMethod: makefile が両 Lambda の `bootstrap` を生成すること（ローカル Makefile target で生成済み、sam build での連結は AWS 側で確認）。
- [ ] deploy 後、2 Log Group(保持30日), Work/DLQ FIFO, Scheduler DLQ Standard, MetricFilter→ErrorCount, Alarm 3種, Role 3種が作成されること。

### 4.2 EventBridge Scheduler
- [ ] Scheduler 実行時に `<aws.scheduler.scheduled-time>` が入力 JSON へ展開されること。
- [ ] 同一 Scheduler 再試行で `cycle_id` が同一になること。
- [ ] `SchedulersEnabled` default false で3 Scheduler が無効状態で作成されること。
- [ ] Scheduler RetryPolicy(maxRetry=3, maxEventAge=240), Scheduler DLQ が設定どおりこと。

### 4.3 SQS / S3 / SSM 実動作
- [ ] Work Queue の MessageGroupId `amazon-requests` 排他で Amazon リクエストが直列化されること。
- [ ] maxReceiveCount=5 到達で DLQ へ移行すること。
- [ ] S3 `If-Match` 条件付き書き込みで 412 発生時、最大3回再 merge されること（MemStore テスト済み、実S3で確認）。
- [ ] SSM `/myapp/secure/{KEY}` → `ParameterNotFound` 時 `/myapp/plain/{KEY}` へ fallback すること。SecureString が customer managed KMS の場合は `kms:Decrypt` 追加要否を確認。

### 4.4 Alarm → 通知連携
- [ ] Work DLQ / Scheduler DLQ / Work Queue 滞留(7200秒) の各 Alarm が ALARM 遷移時に `schedule-checks` を起動すること。
- [ ] 同じ Alarm 状態で通知が増殖しないこと。
- [ ] OK 遷移では `schedule-checks` を起動しないこと。

### 4.5 実 HTTP・外部API
- [ ] Lambda 環境から Amazon.co.jp への到達性と、実HTMLに対する各 selector の有効性（検索発売日 `nth-child`, `#detailBullets_feature_div` fallback, CAPTCHA/access-denied marker）。実HTML fixture を取得し `testdata/amazon` へ保存して自動テストを補強する。
- [ ] Slack/Mastodon/GitHub Gist API への実際の送信・更新。

### 4.6 切り替え手順（§20.3）
- [ ] Scheduler 無効状態で deploy。
- [ ] 既存S3対象 object を日時付き backup prefix へコピー。
- [ ] 既存3 Checker の EventBridge trigger を無効化。
- [ ] `migrate-maxprice` dry-run → apply（§20.2, docs/operations.md §5）。
- [ ] 既知HTML fixture で新Lambda 確認。
- [ ] 手動 invoke で SQS から1件ずつ疎通確認。
- [ ] event source mapping 有効化 → 3 Scheduler 有効化。
- [ ] 既存 `release-notifier` が有効であること。
