# 運用手順

本書は `SPECIFICATION.md` §17, §20, §26 に基づく本番運用手順の集約である。
業務仕様・閾値は `SPECIFICATION.md` を正とし、本書は操作手順と確認手順を示す。
記載のコマンドは profile・region の明示を前提とする（AGENTS.md §13）。

## 前提

- 本番 Lambda は `schedule-checks` と `check-worker` の2本だけ。
- Amazon HTTP 取得は直列化されており、1 worker 起動で Amazon へ最大1回しかアクセスしない。
- 商品通知（Slack notice / Mastodon）は best-effort。通知 outbox は持たない。
- 運用エラー通知は CloudWatch Alarm の `ALARM` 遷移時だけ Slack error channel へ1件送る。

---

## 1. デプロイ（前後確認）

`scripts/deploy.sh` は AWS SAM 標準フロー（sam build / sam deploy）の薄い wrapper である。
build / deploy を個別に、または `all` で連続実行できる。
deploy は sam deploy 自身の change set 確認プロンプト（`infra/samconfig.toml` の
`confirm_changeset`）で人間が変更内容を確認してから、同じ sam deploy が適用する。
自前の package・change set 管理・state file は持たない。
default で `SchedulersEnabled=false`（Scheduler 無効）を想定する。

### 1.1 ローカル品質確認（デプロイ前に必須）

```bash
make check
go test -race ./...
govulncheck ./...
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o /dev/null ./cmd/schedule-checks
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o /dev/null ./cmd/check-worker
```

`gofmt -l` が空、test / vet / staticcheck / govulncheck の error と warning が0件であることを確認する。

### 1.2 build

```bash
./scripts/deploy.sh --profile <P> --region <R> --stage build
```

- `sam build` で Lambda バイナリ等を build し、build 済み template（`.aws-sam/template.yaml`）へ出力する。deploy はしない。

### 1.3 deploy（確認プロンプト付き適用）

```bash
./scripts/deploy.sh --profile <P> --region <R> --stage deploy
```

- `sam deploy` は build 済み template（`.aws-sam/template.yaml`）を適用する。`sam deploy` は build しないため、単体実行時は先に `--stage build` を実行すること（build 後に連続実行する場合は `--stage all`）。
- `sam deploy` が change set を作成・表示し、確認後に同じ sam deploy が適用する。
  安定設定（stack 名・`capabilities`・`confirm_changeset`）は `infra/samconfig.toml`。
- 成果物 S3 bucket は `--s3-bucket BUCKET`、未指定時は `--resolve-s3`。
- Parameter override は `--parameter-override K=V`（複数指定可）。

### 1.4 デプロイ後確認

- 両 Lambda の Log Group が作成され、保持期間が30日であること。
- Work Queue / Work DLQ / Scheduler DLQ が FIFO / Standard 構成どおりに作成されていること。
- event source mapping の `BatchSize=1` が設定されていること。check-worker は失敗時に Lambda error を返し SQS へ再配信させる契約（partial batch response は使用しない）。
- 3 Scheduler が `SchedulersEnabled` の指定どおりの有効状態であること（初回は無効）。
- 2 Lambda の IAM role が共有されていないこと。
- S3 bucket リソースが stack 削除対象に入っていないこと。

---

## 2. best-effort 通知の制約

商品通知は S3 更新成功後に行う。Slack・Mastodon は各5秒 timeout で、片方の失敗後も他方を実行する。
通知 adapter は各送信結果を個別に構造化ログへ出す（`notification_error`）。

- S3 commit 後かつ通知前に実行環境が停止した場合は通知が欠落する。
- 通知後かつ SQS message 削除前に停止した場合は再実行で重複する余地がある。
- S3 状態の整合性を通知の exactly-once 性より優先する。通知失敗で保存済み価格を巻き戻さない。
- 個別 Amazon リクエストエラーを Slack へ送らない。再試行中のエラーは CloudWatch Logs のみ。

これらは仕様（`SPECIFICATION.md` §17.1）。通知 outbox は追加しない。

---

## 3. DLQ / Alarm 対応

Work DLQ・Scheduler DLQ・Work Queue 滞留の3 Alarm が `ALARM` へ遷移したときだけ、
`schedule-checks` が Alarm イベントで起動し Slack error channel へ1件通知する。
同じ Alarm 状態の間にエラー件数分の通知は増やさない。復旧は Alarm の `OK` 遷移で確認する。

### 3.1 Work DLQ（`SPECIFICATION.md` §26.1）

1. Alarm 通知の queue 名と message 数を確認する。
2. DLQ message の `job_id`, `cycle_id`, `kind`, target を確認する。
3. `job_id` で CloudWatch Logs を検索し、最大5回分の `error_type`, `http_status`, `response_bytes` を比較する。
4. 単一 target の問題か、同時刻の複数 target に共通する問題かを判定する。
5. selector・code・対象 data の必要な修正を行い、fixture と自動 test を追加する。
6. 修正版を deploy する。
7. DLQ message を Work Queue へ redrive する。
8. `job_completed` ログ、DLQ 空、Alarm の `OK` 遷移を確認する。

原因確認前に DLQ message を削除しない。404・商品種別不一致などの terminal result は DLQ へ入らない。

#### 3.1.1 設定読込失敗（`error_type=config_load`）

`error_type=config_load` は単一 target ではなく `check-worker` 起動時の可変設定読込失敗である（`SPECIFICATION.md` §9.1）。`checker_configs.json` または `excluded_title_keywords.json` の object 不在・S3 一時障害・JSON 不正・型不正のいずれかで、当該 invocation の全 record が処理前に失敗する。Amazon 取得は始まっていないため `http_status`・`response_bytes` は空・0 になる。

1. 両 object の存在と内容を確認する（存在・有効な JSON・想定する型）。
2. `excluded_title_keywords.json` は文字列配列のみ有効。空配列 `[]` は正常だが、object 不在・`null`・配列以外は失敗原因。
3. S3 一時障害の可能性がある場合は直近の S3 / Lambda エラーを確認し、再試行で復旧したかを見る。
4. 内容を修正・復元したあと DLQ message を Work Queue へ redrive する。

設定読込失敗を単一 target の Amazon エラーと誤認しない。同一時刻に複数 target が一斉に失敗している場合は設定読込を疑う。

### 3.2 Queue 滞留（`SPECIFICATION.md` §26.2）

`ApproximateAgeOfOldestMessage` が2時間（7200秒）を超えた場合は次を確認する。

- 同じ Amazon job が再試行を繰り返していないか。
- 403, 429, CAPTCHA, 短い200本文が複数 target で発生していないか。
- 1件の平均 `duration_ms` が増加していないか。
- 直前の周回が次のセール周回までに終了しているか。

原因を確認せずに MessageGroupId を分割しない。分割すると Amazon 同時リクエスト数が増える。
構造化ログで直列処理が周期内に収まらないことを確認した場合だけ変更する。

### 3.3 Scheduler DLQ

EventBridge Scheduler の再試行上限を超えた event が Scheduler DLQ へ入る。
DLQ message の Scheduler 入力 JSON（`SPECIFICATION.md` §6 形式）から対象 check 種別と時刻を確認し、
`schedule-checks` の該当時刻のログで投入成否（`cycle_dispatched` / `cycle_disabled`）を確認する。

---

## 4. ロールバック（`SPECIFICATION.md` §20.4）

rollback は §5.0 で記録した VersionId を基準とする。切り替え後に発生した手動編集を
盲目的に上書きしないため、配列全体の無条件上書きは行わず object ごとに復元要否を判断する。

1. 新 Scheduler 3つと SQS event source mapping を無効にする。
2. 旧3 Checker（既存新刊・セール・紙書籍）の EventBridge trigger を再有効化する。
3. 対象 object ごとに、記録した VersionId の内容と現行データを比較する。
   該当 version の本文は `aws s3api get-object --version-id <V>` で読み出せる。
4. 新システムだけが変更した部分のみ、記録した旧 version の内容へ選択的に戻す。
   切り替え後に手動で追加・変更・削除されたレコードは保持する。

S3 backup を配列全体で無条件に上書きしてロールバックしない。
手動追加レコードは条件付き書き込みでは検出できないため、version の差分比較と選択的復元で保護する。
Versioning が `Enabled` でない場合は version 指定の復元ができず、切り替え前提（§5.0）を満たさない。

---

## 5. MaxPrice 移行（dry-run / apply）

`SPECIFICATION.md` §20.2 の MaxPrice 初期化パッチ。
UserScript の Option+↑ で生成された既存 MaxPrice には紙書籍価格が入るため、
`MaxPrice = CurrentPrice` へ置き換える。対象は次の3 object。

- `unprocessed_asins.json`
- `upcoming_asins.json`
- `notified_asins.json`

`paper_books_asins.json` はセール価格履歴ではないため対象外。

### 5.0 Versioning 確認と VersionId 記録（dry-run の前必須, `SPECIFICATION.md` §20.3 step2）

対象 bucket の Versioning が `Enabled` であることを前提とする。`Suspended` や未設定の場合は
本番切り替えへ進めない（version 指定での選択的 rollback が成立しないため）。

Versioning=Enabled なら backup prefix への object copy は作らない。代わりに、対象3 object の
現時点 VersionId を記録し rollback 基準にする。

```bash
# Versioning 確認（read-only）。出力の Status が Enabled であること。
aws s3api get-bucket-versioning --bucket <BUCKET> --profile <P> --region <R>

# 各 object の現時点 VersionId を記録する（cutover 直前）。
aws s3api head-object --bucket <BUCKET> --key unprocessed_asins.json --profile <P> --region <R> --query VersionId
aws s3api head-object --bucket <BUCKET> --key upcoming_asins.json    --profile <P> --region <R> --query VersionId
aws s3api head-object --bucket <BUCKET> --key notified_asins.json    --profile <P> --region <R> --query VersionId
```

apply 後の戻しは、記録した VersionId からの選択的復元のみ許容する。配列全体の無条件上書きは禁止する（§4 ロールバック）。

> 注: Versioning 確認には `s3:GetBucketVersioning`、lifecycle 確認には `s3:GetLifecycleConfiguration` 権限が必要で、権限の狭い profile では AccessDenied になる。切り替え前に十分な権限のある profile で `Enabled` を確認すること。

### 5.1 dry-run（既定）

```bash
go run ./scripts/migrate-maxprice -bucket <BUCKET> -region <R>
```

変更を適用せず、変更件数・ASIN 集合・CurrentPrice・他 field の不変を検証する。
`CurrentPrice == 0` のレコードは `MaxPrice` も0のままにする。

### 5.2 検証項目（dry-run / apply 両方）

- ASIN 件数が変化しないこと。
- ASIN 集合が変化しないこと。
- 各レコードの `CurrentPrice` が変化しないこと。
- `MaxPrice` 以外の field と未知 field（Extra）が保持されること。

### 5.3 apply（明示指定時のみ）

```bash
go run ./scripts/migrate-maxprice -bucket <BUCKET> -region <R> -apply
```

`-apply` を明示指定した場合だけ S3 へ書き込む。書き込みは `If-Match` を使う。
apply 後も §5.2 の検証項目を再確認する。

---

## 6. ローカル JSON 編集（`SPECIFICATION.md` §26.3）

- upload 直前に対象 S3 object の最新版を取得する。
- ローカル追加分を最新版へ merge してから upload する。
- 自動処理が先に書き込んだ内容を、古いローカル copy で配列全体上書きしない。
- upload 後に件数と追加 ASIN または作者を確認する。

自動処理側は ETag 競合時に手動変更を読み直して merge する。
手動側が古い copy を後から無条件 upload した場合は S3 の条件付き書き込みで検出できないため、
上記手順で回避する。
