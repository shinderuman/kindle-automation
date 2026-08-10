# Kindle自動チェック統合仕様

- 文書状態: 実装基準
- 最終更新日: 2026-07-23
- 対象リポジトリ: `/Users/shinderuman/src/kindle-automation`
- 参照実装:
  - `/Users/shinderuman/src/kindle_bot`
  - `/Users/shinderuman/src/userscripts/kindle`
  - `/Users/shinderuman/src/userscripts/universal/multi_site_keybind_manager`

## 1. 目的

Amazon Creators APIを使用している既存`kindle_bot`のうち、次の3機能をAmazon商品ページへの生HTTPリクエストへ置き換える。

- 新刊チェック
- セールチェック
- 紙書籍からKindle版への移行チェック

対象リスト、価格履歴、通知履歴、周期、S3更新、Slack・Mastodon通知、GitHub Gist更新は既存Go実装の運用を引き継ぐ。Amazon HTMLの取得項目とセレクタはUserScript実装を基準にする。

## 2. 仕様の優先順位

実装間で挙動が異なる場合は、次の順序で採用する。

1. 本文書に記載された明示的な要件
2. `kindle_bot`の業務判定、S3管理、履歴、通知、並び順
3. UserScriptのAmazon URL、HTMLセレクタ、値の抽出方法

この優先順位により、次を確定事項とする。

- セール価格差は紙書籍価格ではなく、過去最高Kindle価格と現在のKindle価格で判定する
- セール閾値はUserScriptの固定値ではなく、S3の`checker_configs.json`を正とする
- 通知済み新刊の保存期間はUserScriptの7日間ではなく、既存Go実装の将来発売分のみとする
- クーポン判定はUserScriptから追加する
- UserScriptのシリーズ一括購入に関する判定は移行しない

## 3. 対象範囲

| 機能 | 方針 |
|---|---|
| 新刊チェック | 本リポジトリへ移行する |
| セールチェック | 本リポジトリへ移行する |
| 紙書籍・Kindle版チェック | 本リポジトリへ移行する |
| 発売日当日の通知 | 移行せず、既存`kindle_bot`の`release-notifier`を継続利用する |
| Kindleシリーズ一括購入の価格・ポイント・クーポン判定 | 移行しない |
| Campaign Sorter、Deleted Item Checker、本棚巻数フィルター | 移行しない |
| ブラウザのタブを開く操作 | 実装しない。商品URLをSlack・Mastodon通知へ含める |

新システムの稼働開始後も、既存`release-notifier`が同じ`notified_asins.json`と`unprocessed_asins.json`を読み取れる状態を維持する。

## 4. 採用技術

- 実装言語: Go
- Lambdaランタイム: `provided.al2023`
- Amazon取得: `net/http`による生HTTPリクエスト
- HTML解析: `goquery`
- AWSアクセス: AWS SDK for Go v2
- 構造化ログ: `log/slog`のJSONハンドラー
- IaC: AWS SAMテンプレートでLambda、Scheduler、SQS、CloudWatchを定義する
- 配布形式: 静的リンクした`bootstrap`をZIPで配布する

次は採用しない。

- ヘッドレスブラウザ、Chromium、Playwright
- Node.jsの本番ランタイム
- API Gateway
- Fargate
- Step Functions
- DynamoDB
- Lambdaコンテナイメージ、ECR
- Lambda Layer

## 5. 実行構成

```text
EventBridge Scheduler（3スケジュール）
    │
    ▼
schedule-checks Lambda
    │  対象スナップショットをジョブ化
    ▼
kindle-automation-jobs.fifo
    │  BatchSize=1
    │  Amazon jobs: MessageGroupId=amazon-requests
    │  Gist jobs: MessageGroupId=external-updates
    ▼
check-worker Lambda
    ├─ Amazonへ最大1リクエスト
    ├─ S3を条件付き更新
    ├─ 必要時に後続ジョブをSQSへ追加
    └─ Slack・Mastodon・GitHub Gistを更新
```

Lambda関数は2本とする。

### 5.1 `schedule-checks`

責務は次に限定する。

- EventBridge Schedulerイベントの検証
- S3 Checker設定と対象リストの読み取り
- 対象の重複排除
- 周回を識別する`cycle_id`と各`job_id`の生成
- 対象単位のSQSメッセージ投入
- セール周期開始時のUpcoming取り込み
- CloudWatch Alarmイベントを受けた場合の運用Slack通知

Amazonへはアクセスしない。業務判定や商品通知も行わない。

### 5.2 `check-worker`

責務は次とする。

- SQSメッセージの検証
- 1起動につきAmazonへ最大1回のHTTPリクエスト
- HTML抽出と業務判定
- S3の対象レコード更新
- 必要な後続ジョブのSQS投入
- 商品通知とGist更新

取得結果だけを処理する3本目のLambdaは設けない。1対象の取得、判定、対象レコード更新を同じworker起動内で終える。

### 5.3 ネットワーク

Lambdaはユーザー管理VPCへ接続しない。Lambda標準のインターネット接続を使用し、NAT Gateway、サブネット、Security Groupを追加しない。

### 5.4 AWSリソース数

| リソース | 数 | 用途 |
|---|---:|---|
| Lambda | 2 | dispatch・Alarm通知、worker |
| EventBridge Scheduler | 3 | セール、新刊、紙書籍・Kindle版 |
| SQS FIFO | 2 | Work Queue、Work DLQ |
| SQS Standard | 1 | Scheduler DLQ |
| CloudWatch Log Group | 2 | Lambdaごと。保持30日 |
| Logs Metric Filter | 2 | 各Log GroupのERRORを同じErrorCountへ集計 |
| CloudWatch Alarm | 3 | Work DLQ、Scheduler DLQ、Work滞留 |
| Lambda実行Role | 2 | 関数別の最小権限 |
| Scheduler実行Role | 1 | `schedule-checks`のinvoke専用 |

常時起動するcompute resourceは設けない。PoC用Lambdaと既存`release-notifier`はこのSAM stackの管理対象に含めない。

## 6. 実行周期

EventBridge Schedulerのタイムゾーンを`Asia/Tokyo`、Flexible Time Windowを`OFF`にする。

| チェック | cron式 | JST起動時刻 | 1日の周回数 |
|---|---|---|---:|
| セール | `cron(0 0/2 * * ? *)` | 0:00から2時間ごと | 12 |
| 新刊 | `cron(10 0/6 * * ? *)` | 0:10、6:10、12:10、18:10 | 4 |
| 紙書籍・Kindle版 | `cron(20 0/6 * * ? *)` | 0:20、6:20、12:20、18:20 | 4 |

1周は、Scheduler起動時にS3に存在する対象のスナップショットを1回ずつジョブ化したものと定義する。起動後に手動または自動で追加された対象は次の周回から処理する。

Schedulerイベントの`<aws.scheduler.scheduled-time>`を基に、次の形式で決定的な`cycle_id`を作る。Scheduler再試行でも同じ値になるようにする。

```text
{check_type}:{scheduled_time_utc}
```

Schedulerから`schedule-checks`へ渡す入力は次の形とする。

```json
{
    "version": 1,
    "source": "scheduler",
    "check_type": "sale",
    "scheduled_at": "<aws.scheduler.scheduled-time>"
}
```

`Enabled=false`のCheckerは`cycle_disabled`をINFOログへ出してジョブを投入しない。対象配列が空の場合は`target_count=0`の正常な`cycle_dispatched`とし、エラーにしない。Saleだけは空の場合も`sale_finalize`を投入し、Gistを現在の空リストへ同期できるようにする。

各Schedulerの再試行設定は次とする。

- Maximum event age: 240秒
- Maximum retry attempts: 3回
- Scheduler DLQ: Standard SQS、保持14日、SSE-SQS有効

## 7. SQSによる処理分割

### 7.1 Work Queue設定

| 項目 | 値 |
|---|---|
| Queue種別 | FIFO |
| Queue名 | `kindle-automation-jobs.fifo` |
| MessageGroupId | Amazon系は`amazon-requests`、Gist更新は`external-updates` |
| ContentBasedDeduplication | 無効 |
| BatchSize | 1 |
| Lambda timeout | 30秒 |
| Visibility timeout | 180秒 |
| Message retention | 4日 |
| Encryption | SQS managed encryption（SSE-SQS） |
| maxReceiveCount | 5 |
| DLQ | `kindle-automation-jobs-dlq.fifo` |
| DLQ retention | 14日 |

Amazonへアクセスし得るジョブはすべて同じMessageGroupIdであるため、同時に実行されるAmazonリクエストは1件になる。Lambdaの予約済み同時実行数による制限は使用しない。

Amazon系の1件が再試行中は、`amazon-requests`の後続ジョブを待たせる。これはAmazon側の一時的な拒否やHTML構造変化が起きた際に、後続リクエストを連続送信しないためのバックプレッシャーとして扱う。5回失敗したメッセージはDLQへ移り、後続ジョブを再開する。

`gist_update`だけは`external-updates`を使用し、GitHub API障害でAmazon取得を停止させない。`gist_update`はS3を読み取るだけで、AmazonアクセスとS3書き込みを行わない。

### 7.2 メッセージ形式

```json
{
    "version": 1,
    "job_id": "sale:2026-07-23T00:00:00Z:B0XXXXXXXX",
    "kind": "sale_check",
    "check_type": "sale",
    "cycle_id": "sale:2026-07-23T00:00:00Z",
    "scheduled_at": "2026-07-23T00:00:00Z",
    "target": {
        "asin": "B0XXXXXXXX"
    }
}
```

`target`はjob kindごとに必要なfieldだけを持つ。

| `kind` | 必須target field |
|---|---|
| `sale_check` | `asin` |
| `sale_finalize` | なし |
| `new_release_search` | `author_name` |
| `new_release_result` | `asin`、`author_name`と型付き`product` |
| `new_release_detail` | `asin`、`author_name` |
| `paper_to_kindle_check` | `asin` |
| `paper_to_kindle_detail` | Kindle版の`asin`、紙書籍の`source_asin` |
| `gist_update` | `gist_type` |

`new_release_result`の`product`は検索HTMLから抽出したASIN、タイトル、URL、Kindle価格、発売日、作者表記、商品種別だけを持つ。`MaxPrice`、`CreatedAt`、通知状態等の管理値を入れない。商品種別`item_type`の正規値は小文字`kindle`のみとし、検索結果でKindle版と確定できた候補だけがこの値を持つ。未知・非Kindle候補は`new_release_result`へ進めず`new_release_detail`へ回すため、`item_type`へ推測値や空文字を入れない。

メッセージへS3レコード全体を入れない。workerはASINまたは作者名をキーに、処理開始時の最新S3レコードを読み直す。手動削除済みの対象は再追加せず、`target_removed`として正常終了する。

`job_id`は`kind`、`cycle_id`、正規化した対象識別子から決定的に作る。SQSの`MessageDeduplicationId`には`job_id`そのものではなく、`SHA-256(job_id)`の小文字hex 64文字を使用する。

未対応`version`、未知の`kind`、必須field欠落、ASIN形式不正はjob schema errorとしてAmazonへアクセスせずLambdaエラーを返す。5回後にDLQへ残し、黙って削除しない。

### 7.3 ジョブ種別

| `kind` | MessageGroupId | Amazonリクエスト | 用途 |
|---|---|---:|---|
| `sale_check` | `amazon-requests` | 商品ページ1回 | 価格、ポイント、クーポン判定 |
| `sale_finalize` | `amazon-requests` | 0回 | 周回終了を待ち、Sale用`gist_update`を投入 |
| `new_release_search` | `amazon-requests` | 検索ページ1回 | 作者の候補ASIN抽出 |
| `new_release_result` | `amazon-requests` | 0回 | 必須項目が揃った検索候補の判定・保存 |
| `new_release_detail` | `amazon-requests` | 商品ページ1回 | 候補の発売日、価格、Kindle版確認 |
| `paper_to_kindle_check` | `amazon-requests` | 紙書籍ページ1回 | Kindle版スウォッチ確認 |
| `paper_to_kindle_detail` | `amazon-requests` | Kindle商品ページ1回 | Kindle版候補の検証と保存 |
| `gist_update` | `external-updates` | 0回 | Sale、Author、Paper-to-Kindle Gistの再生成 |

検索結果で必須項目が揃った候補は候補ごとに`new_release_result`、不足する候補は候補ごとに`new_release_detail`を投入する。検索job自身は候補のS3保存と商品通知を行わない。紙書籍ページでKindle版を検出した場合も同様に`paper_to_kindle_detail`を投入する。

`schedule-checks`は`SendMessageBatch`の10件単位を順番に送信し、並列送信しない。セール周回では全`sale_check`の送信成功後にだけ`sale_finalize`を送る。batch内に失敗entryが1件でもあればdispatchを失敗させ、同じ`cycle_id`と`job_id`でScheduler再試行を受ける。

### 7.4 再試行と冪等性

- HTTPクライアント内では再試行しない
- 再試行可能エラーはLambdaエラーとして返し、SQSに再配信させる
- エラー発生時は、そのリクエスト結果に基づくS3更新と商品通知を行わない
- FIFOの5分間重複排除だけに依存せず、S3更新をASIN単位のupsertとして再実行可能にする
- 新刊・紙書籍通知は、通知履歴または既存リストを保存前に再確認する
- Sale通知はSQSの少なくとも1回配信により、通知後かつメッセージ削除前に実行環境が停止した場合だけ同一周回で重複する可能性がある

### 7.5 複数S3オブジェクトの収束

S3の複数object更新をtransactionとして扱わない。job再実行時は「最初から未処理か」ではなく、各objectが期待状態かを個別に確認する。

- `notified`に存在しても`upcoming`がなければUpcomingを補完する
- `upcoming`に存在しても`notified`がなければ通知履歴を補完する
- 紙書籍targetが既に削除済みで、Kindle候補が`upcoming`または`unprocessed`に存在する場合は、先行実行成功としてGist更新jobを再投入する
- 紙書籍targetが削除済みでKindle候補も保存されていない場合は、手動削除として新規保存を行わない
- 1つ目のS3更新成功後に2つ目が失敗した場合はjobを失敗させ、再実行で不足分だけを補う
- 通知済み・既知ASINという理由だけで、他objectのreconcileを省略しない

各follow-up jobの`job_id`を決定的にし、同じreconcileからの再投入を安全にする。

## 8. Lambda設定

| 項目 | `schedule-checks` | `check-worker` |
|---|---:|---:|
| Runtime | `provided.al2023` | `provided.al2023` |
| Architecture | `x86_64` | `x86_64` |
| Memory | 256 MB | 512 MB |
| Timeout | 60秒 | 30秒 |
| VPC | なし | なし |
| Amazonアクセス | なし | 1起動最大1回 |

`check-worker`のAmazon HTTP timeoutは15秒とし、S3更新と外部通知に使う時間をLambda timeout内に残す。

## 9. S3データ契約

### 9.1 既存オブジェクト

既存バケット`kindle-asins`、リージョン`ap-northeast-1`を継続利用する。

| オブジェクト | 用途 | 新システムの扱い |
|---|---|---|
| `authors.json` | 新刊対象作者と最新作 | 読み書きする |
| `paper_books_asins.json` | Kindle版待ちの紙書籍 | 読み書きする |
| `unprocessed_asins.json` | セール対象Kindle書籍 | 読み書きする |
| `excluded_title_keywords.json` | 新刊除外語 | 読み取る |
| `notified_asins.json` | 新刊通知履歴、既存発売日通知の入力 | 読み書きする |
| `upcoming_asins.json` | 新刊・Kindle版検出からSale Checkerへの受け渡し | 読み書きする |
| `checker_configs.json` | 有効化、判定値、Gist設定 | 読み取る |
| `prev_index_new_release.txt` | 旧スロット位置 | 新システムでは使用せず、削除もしない |
| `prev_index_paper_to_kindle.txt` | 旧スロット位置 | 新システムでは使用せず、削除もしない |
| `prev_index_sale_checker.txt` | 旧スロット位置 | 新システムでは使用せず、削除もしない |

対象リストや取得結果をDynamoDBへ移さない。平常時の取得結果とエラー履歴を新たなS3オブジェクトへ保存せず、CloudWatch LogsとSQS DLQを使用する。

### 9.2 書籍JSON

`paper_books_asins.json`、`unprocessed_asins.json`、`notified_asins.json`、`upcoming_asins.json`は次の既存フィールドを維持する。

```json
{
    "ASIN": "B0XXXXXXXX",
    "Title": "商品名",
    "ReleaseDate": "2026-08-28T00:00:00Z",
    "CurrentPrice": 759,
    "MaxPrice": 759,
    "URL": "https://www.amazon.co.jp/dp/B0XXXXXXXX?tag=...",
    "CreatedAt": "2026-07-17T00:00:00Z"
}
```

- フィールド名と型を変更しない
- 日付は既存と同じUTCのRFC 3339文字列として保存する
- 発売日の時刻はUTC 00:00:00とする
- 価格はJSON numberの円単位とする
- 新規レコードの`CreatedAt`だけを現在時刻で設定し、更新時は保持する
- URLは設定済みAffiliate Tagを付けた`https://www.amazon.co.jp/dp/{ASIN}`を使用する
- ASINで重複排除する
- 発売日降順、同日の場合はタイトル昇順で保存する
- 4スペースでインデントし、`&`をUnicodeエスケープしない

### 9.3 作者JSON

```json
{
    "Name": "作者名",
    "URL": "https://www.amazon.co.jp/dp/B0XXXXXXXX",
    "LatestReleaseDate": "2025-12-28T00:00:00Z",
    "LatestReleaseTitle": "最新作",
    "LatestReleaseURL": "https://www.amazon.co.jp/dp/B0XXXXXXXX"
}
```

- `Name`で重複排除する
- 最新発売日降順、同日の場合は作者名昇順で保存する
- `LatestReleaseURL`からqueryとfragmentを除去する

### 9.4 手動編集との共存

`authors.json`、`paper_books_asins.json`、`unprocessed_asins.json`は、自動更新だけでなくローカルで直接編集して対象を追加・変更・削除する運用を継続する。

UserScriptのMulti Site Keybind ManagerでAmazonページ上のOption+↑を押すと、`CurrentPrice`へKindle価格、`MaxPrice`へ紙書籍価格またはKindle価格を入れたJSON断片が生成される。この`MaxPrice`は価格履歴としては扱えないため、初回移行パッチの対象にする。

自動処理は次の規則で手動変更を保護する。

- S3オブジェクト全体を、処理開始時に読み取った古い内容で無条件上書きしない
- worker開始時に対象を最新データから再検索する
- 手動で削除された対象をworkerが再追加しない
- 対象以外のレコードと未知のJSONフィールドを保持する
- 同じASINの自動更新では、Amazon由来のタイトル、価格、発売日、URLだけを更新する

### 9.5 条件付き更新

配列JSONの更新手順を次に統一する。

1. `GetObject`で本文とETagを取得する
2. JSON全体を検証する
3. 対象ASINまたは作者だけを最新本文へmergeする
4. `PutObject`を`If-Match: {ETag}`付きで実行する
5. HTTP 409または412相当の競合時は、最新本文を読み直して最大3回mergeをやり直す
6. 3回失敗した場合はS3更新エラーとしてジョブを失敗させる

オブジェクト新規作成時だけ`If-None-Match: *`を使用する。

## 10. Upcoming連携

新刊チェックと紙書籍・Kindle版チェックは、新しいKindle書籍を`unprocessed_asins.json`へ直接追加しない。

```text
新規Kindle書籍を検出
    ├─ notified_asins.jsonへupsert
    └─ upcoming_asins.jsonへupsert
```

セール周回の`schedule-checks`は、ジョブ投入前に次を行う。

1. `unprocessed_asins.json`と`upcoming_asins.json`を本文・ETag付きで取得する
2. ASINで統合する。重複時は既存`unprocessed_asins.json`側を優先する
3. 条件付き更新で統合結果を`unprocessed_asins.json`へ保存する
4. UpcomingのETagが開始時と同じ場合だけ`upcoming_asins.json`を空配列にする
5. Upcomingが変更されていた場合は消去せず、次のセール周回へ残す
6. 統合後の`unprocessed_asins.json`をセール対象としてジョブ化する

既存実装ではセール処理末尾に行っていた取り込みを、分散実行では周回開始時に行う。条件付き更新と「変更時は消去しない」という競合回避の目的は維持する。取り込み後にジョブ投入が失敗しても、対象は`unprocessed_asins.json`へ残り、次の周回で処理される。

## 11. Amazon HTTP取得

### 11.1 共通リクエスト

- Product URL: `https://www.amazon.co.jp/dp/{ASIN}`
- `User-Agent: Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0 Safari/537.36`
- `Accept-Language: ja-JP,ja;q=0.9`
- `Accept: text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8`
- Cookie、Authorization、Amazonログイン情報は送信しない
- リクエストtimeoutは15秒
- redirectは最大5回
- redirect先hostがAmazon Japan以外になった場合は失敗とする
- response bodyは8 MiBを上限とし、超過時は失敗とする

HTTP 200だけでは成功としない。要求したページ種別に応じて、タイトル、対象ASIN、検索結果コンテナなどの必須構造を検証する。

### 11.2 HTML抽出

本番コードではHTML全体に対する正規表現抽出を使用しない。DOMを`goquery`で解析し、CSSセレクタは`internal/amazon/selectors`へ集約する。

商品ページの初期セレクタはUserScriptとPoCで確認した次の値を使用する。

| 値 | セレクタまたは取得元 |
|---|---|
| タイトル | `#productTitle` |
| ASIN | `#ASIN, input[name='idx.asin'], input[name='ASIN.0'], input[name='titleID']`のvalue。取得できなければcanonical URLまたは最終URL |
| Kindle版スウォッチ | `#tmm-grid-swatch-KINDLE` |
| 紙書籍スウォッチ | `[id^='tmm-grid-swatch']:not([id$='KINDLE'])` |
| 発売日 | UserScriptのOption+↑と同じ`#rpi-attribute-book_details-publication_date`配下。UserScriptはdirect-child形式(`> div.a-section.a-spacing-none.a-text-center.rpi-attribute-value > span`)を使用する。`#detailBullets_feature_div`内の発売日はUserScriptに存在しない新規fallback候補とする |
| クーポンバッジ | `i.a-icon.a-icon-addon.newCouponBadge` |
| クーポン文言 | `.couponLabelText` |

Kindle価格はUserScript（`common.js`の`getKindlePrice`）と同じ3層構造で、正の金額を最初に取得できた層を使用する。Kindle Unlimited等でスウォッチ価格が0円になる場合に購入価格へフォールバックする（12.2）。

1. KINDLEスウォッチ価格。次のセレクタの正の金額を使用する。
   - `#tmm-grid-swatch-KINDLE > span.a-button > span.a-button-inner > a.a-button-text > span.slot-price > span`
2. 購入価格（第1層が0円のとき）。次のセレクタを順に試し、購入価格正規表現で解析する。
   - `#tmm-grid-swatch-KINDLE .slot-extraMessage .kindleExtraMessage`
   - `#tmm-grid-swatch-KINDLE .slot-extraMessage`
   - `#tmm-grid-swatch-OTHER .slot-extraMessage .kindleExtraMessage`
   - `#tmm-grid-swatch-OTHER .slot-extraMessage`
3. 従来の価格候補（第1・第2層で取れないとき）。次のセレクタを順に試し、最初の正の金額を使用する。
   - `#tmm-grid-swatch-KINDLE > span.a-button > span.a-button-inner > a.a-button-text > span.slot-price > span`
   - `#tmm-grid-swatch-OTHER > span.a-button > span.a-button-inner > a.a-button-text > span.slot-price > span`
   - `#kindle-price`
   - `#a-autoid-2-announce > span.slot-price > span`
   - `#tmm-grid-swatch-KINDLE > span.a-button > span.a-button-inner > a.a-button-text > span.slot-extraMessage .kindleExtraMessage .a-color-price`

購入価格正規表現はUserScriptの`PURCHASE_PRICE`と同じ次の2パターンを順に適用し、`￥`または`¥`付きの金額を取得する。

- `/(?:または[、,\s]*|購入価格[：:\s]*)[￥¥]\s*([\d,]+)(?:\s*で購入)?/`
- `/[￥¥]\s*([\d,]+)\s*で購入/`

紙書籍価格は次を使用する。

```text
[id^='tmm-grid-swatch']:not([id$='KINDLE']) > span.a-button > span.a-button-inner > a.a-button-text > span.slot-price > span
```

KindleポイントはUserScript（`common.js`の`getKindlePoints`）と同じ2層構造で、正の値を最初に取得できた層を使用する。

1. ポイント専用要素。次のセレクタを順に試し、ポイント正規表現で解析する。
   - `#tmm-grid-swatch-KINDLE > span.a-button > span.a-button-inner > a.a-button-text > span.slot-buyingPoints > span`
   - `#tmm-grid-swatch-OTHER > span.a-button > span.a-button-inner > a.a-button-text > span.slot-buyingPoints > span`
   - `#Ebooks-desktop-KINDLE_ALC-prices-loyaltyPoints`
   - `#Ebooks-mobile-KINDLE_ALC-prices-loyaltyPoints`
2. 購入価格補助文言（第1層が取れないとき）。Kindle価格第2層と同じ`slot-extraMessage`系セレクタを順に試し、同じ要素のtextからポイント正規表現で再抽出する。

検索ページはUserScript（`new_release_checker`の`extractSearchPageInfo`）を基準とし、UserScriptにないセレクタは実HTML fixtureで検証が必要な新規fallback候補として区別する。

| 値 | UserScript由来 | 新規fallback候補（実HTML fixtureで検証） |
|---|---|---|
| 結果 | `[data-component-type="s-search-result"]` | なし |
| ASIN | 商品URLの`/dp/{ASIN}`から抽出。`data-asin`属性はUserScript未使用 | `data-asin`属性（実HTMLで存在・有効か検証） |
| タイトル | `.s-title-instructions-style a h2 span` | `h2 span` |
| 商品URL | タイトル要素の`closest('a')`、次に`h2 a, .a-link-normal[href*="/dp/"]` | なし |
| 価格 | `span.a-offscreen`（`￥`前置で解析） | `.a-price .a-offscreen` |
| 作者表記 | `.a-size-base` | `.a-row.a-size-base.a-color-secondary` |
| Kindle形式 | `.puis-price-instructions-style a.a-text-bold`の最初のtext（実HTMLで`Kindle版`を確認） | なし |
| 発売日表示 | `.puis-desktop-list-row .puisg-col-4-of-24 div:nth-child(2) div:nth-child(2) span span` | なし |

検索URLの`i=digital-text`だけでは候補がKindle版と確定できない。検索結果カード内のKindle形式表示（`.puis-price-instructions-style a.a-text-bold`のtextが`Kindle版`）で初めてKindle版と確定し、確定できた候補だけ`new_release_result`へ進む。形式表示が`Kindle版`以外、または取得できない候補は非Kindle/種別不明として`new_release_detail`へ回し、Kindle扱いしない。

作者表記は1つのテキストに複数 contributor と販売者・日付が混入し得る（例: `著者A、 著者B | 販売者:... | 2026/8/7`）。contributor 境界を保持するため、` | `より前の著者部分を取り出し`、`で分割して各 contributor を個別に格納する。

UserScriptは価格・作者表記に表のUserScript由来欄のセレクタを使用し、新規fallback候補欄は使用しないため、実装時もUserScript由来を優先し、取得できない場合だけ新規fallback候補を試す。ASINの`data-asin`属性はUserScriptが使用しないため、本仕様でもURL抽出を優先し、`data-asin`は実HTML検証後に補助とするか判断する。

ASINは`/(?:dp|gp/product|kindle-dbs/product)/([A-Z0-9]{10})`に相当するURL pathの部分から取得する。正規表現は選択済みDOM要素のtext解析にだけ使用できる。

価格とポイントは選択済みDOM要素のtextを正規表現で解析し、カンマを除去して正の整数へ変換する。商品ページのKindle価格・紙書籍価格・ポイントはUserScript（`common.js`）と同じ正規表現を使用する。

- 価格（スウォッチ・ポイント専用要素）: `([\d,]+)`
- 購入価格: Kindle価格の購入価格正規表現（前述）を使用する
- ポイント: `([\d,]+)\s*(?:pt|ポイント)`。大文字小文字を区別しない。UserScriptは`pt`に加え`ポイント`表記と大小文字無視を含むため、`([\d,]+)\s*pt`より広くする
- 検索ページの価格: UserScript（`new_release_checker`）と同じ`￥([\d,]+)`で解析する

発売日文字列はUserScript（`new_release_checker`の`parseDateFromText`）と同じ`(\d{4})年(\d{1,2})月(\d{1,2})日`または`(\d{4})/(\d{1,2})/(\d{1,2})`形式を正規表現で解析する。UserScriptの`new Date()`コンストラクタ任せやローカル時刻生成は採用せず、抽出した年月日を9.2に従いUTC 00:00:00へ正規化する。UserScript間で発売日正規化の挙動が一貫しないため、UTC正規化は新システム要件として本仕様で定める。

クーポンありの判定は、クーポンバッジのtextに`クーポン:`が含まれることとする。クーポン文言は`.couponLabelText`全体の`Text()`ではなく、最初の直接テキストノードだけをtrimして取得する。子要素の「規約」等を通知文へ混ぜない。バッジはあるが文言を取得できない場合も、条件名`クーポンあり`は成立させる。

ポイント要素がない場合は0ポイント、クーポン要素がない場合はクーポンなしとする。Kindle価格は業務判定に必須であり、取得できない場合に0円として保存しない。

検索ページとして検証できたにもかかわらず検索結果が0件の場合は、既存Go実装と同じく`search_empty`の再試行可能エラーとする。

### 11.3 取得結果の分類

| 状態 | 再試行 | 処理 |
|---|---:|---|
| 200かつ必須構造あり | しない | 業務判定へ進む |
| 404または明示的な商品不存在 | しない | `not_found`をWARNログ、対象はリストへ残す |
| ページ内ASINが要求ASINと異なる | しない | `asin_mismatch`をWARNログ、対象はリストへ残す |
| 対象がKindle版ではない | しない | `not_kindle`をWARNログ、対象はリストへ残す |
| 400等の恒久的4xx | しない | ERRORログ、対象はリストへ残す |
| 403、429、5xx | する | Lambdaエラーとして返す |
| timeout、DNS、接続失敗 | する | Lambdaエラーとして返す |
| CAPTCHA、アクセス拒否ページ | する | Lambdaエラーとして返す |
| body超過、短い本文、必須構造欠落 | する | 取得内容不足としてLambdaエラーを返す |
| 必須価格・発売日の解析失敗 | する | 解析エラーとしてLambdaエラーを返す |

単一ジョブの失敗は同じ起動内で継続せず、起動全体を失敗させる。他の対象は別メッセージであるため、対象単位で分離される。

response byte数だけの固定下限は設けない。短い本文であっても必須構造があれば処理し、長い本文であってもCAPTCHAや必須構造欠落なら失敗とする。

## 12. セールチェック

### 12.1 対象と周期

- 対象: セール周回開始時に統合済みの`unprocessed_asins.json`
- 周期: 2時間ごと
- 1日: 全対象を12周
- 1ジョブ: 1 ASIN、商品ページ1リクエスト

### 12.2 取得値

- 商品タイトル
- 対象ASIN
- Kindle価格
- Kindleポイント
- クーポン有無
- クーポン文言
- Kindle版であること

紙書籍価格は取得・判定に使用しない。Kindle Unlimited対象であっても、購入可能なKindle価格を通常と同じ価格候補から抽出する。Kindle Unlimitedの有無自体はセール条件にしない。

クーポン抽出器自体は商品種別に依存させない。書籍以外でもクーポンを抽出できる状態を維持し、Saleユースケース側で対象がKindle版であることを先に確認する。実運用で書籍以外を除外する正本は`unprocessed_asins.json`である。

### 12.3 価格履歴更新

取得前のレコードを`old`、今回取得価格を`current`とする。

```text
new.CurrentPrice = current
new.MaxPrice = max(old.MaxPrice, old.CurrentPrice, current)
new.CreatedAt = old.CreatedAt
```

既存`MaxPrice`が0の場合は今回価格を初回基準にする。価格差セールは初回取得では成立しないが、ポイントとクーポンは初回取得でも判定する。

セール条件成立時も`CurrentPrice`と`MaxPrice`を保存する。既存Go実装にある、セール通知時に更新済み書籍を保存対象へ追加しない挙動は引き継がない。

### 12.4 セール条件

次の4条件はすべて独立したセール条件であり、主従を設けない。1つ以上成立した場合に、成立した条件を1件のセール通知へ列挙する。

| 条件 | 判定式 |
|---|---|
| 過去最高価格との差 | `new.MaxPrice - current >= SaleThreshold` |
| ポイント数 | `points >= SaleThreshold` |
| ポイント還元率 | `points / current * 100 >= PointPercent` |
| クーポン | Amazon HTML上にクーポン表示が存在する |

通知の条件名はそれぞれ`✅ 最高額との価格差 {diff}円`、`✅ ポイント {points}pt`、`✅ ポイント還元 {percent}%`、`✅ クーポンあり`とする。クーポン文言を取得できた場合だけ括弧内へ追加する。

`SaleThreshold`は既存どおり、価格差とポイント数の双方に使用する。`SaleThreshold`と`PointPercent`はS3設定値を使用する。

同じ商品が条件を満たし続ける場合、2時間ごとの各周回で通知する。セール通知の周期内抑制履歴は追加しない。

### 12.5 価格変動通知

セール条件とは別に、既存`CurrentPrice`からの変化を通知する。

```text
price_change = current - old.CurrentPrice
```

- `old.CurrentPrice == 0`の場合は通知しない
- `price_change >= PriceChangeAmount`なら値上がり通知
- `price_change <= -PriceChangeAmount`なら値下がり通知
- セール条件も成立した場合はセール通知だけを送り、同じ取得結果から価格変動通知を重ねて送らない

### 12.6 保存と通知の順序

1. 最新の対象レコードをS3から確認する
2. Amazonを取得・解析する
3. `CurrentPrice`と`MaxPrice`を条件付き更新する
4. セール条件または価格変動条件を判定する
5. Slack・Mastodonへ通知する

外部通知失敗時に価格履歴を巻き戻さない。通知失敗はERRORログへ記録する。

周回の最後に`sale_finalize`がSale用`gist_update`を1回投入する。`gist_update`は実行時点の`unprocessed_asins.json`からSale Gistを再生成する。

## 13. 新刊チェック

### 13.1 対象と周期

- 対象: `authors.json`
- 周期: 6時間ごと
- 1日: 全作者を4周
- 検索ジョブ: 1作者、検索ページ1リクエスト
- 詳細ジョブ: 1候補ASIN、商品ページ1リクエスト

### 13.2 検索URL

```text
https://www.amazon.co.jp/s?k={URLエンコードした作者名}&i=digital-text&rh=n%3A2250738051&s=date-desc-rank
```

`[data-component-type="s-search-result"]`の先頭10件を候補として処理する。

### 13.3 検索結果の事前除外

次を候補から除外する。

- タイトル、URL、ASINを取得できない
- ASINが10〜13桁の数字だけで構成される紙書籍ISBN
- `notified_asins.json`に同じASINがある
- タイトルに`excluded_title_keywords.json`の文字列を含む
- タイトルに`\d{4}年\d{1,2}月`を含む
- 検索結果の作者表記が対象作者と一致しない

作者名の比較では、全角ASCIIを半角へ変換し、全角・半角スペースを除去した正規化名同士を比較する。contributor 表記は役割（`(著)`等）や販売者・日付が混入し得るため、各 contributor ごとに役割表記を除去して正規化した完全名を作り、対象作者の正規化名と完全一致する contributor が1つでもあれば一致とする。空白で分解した姓・名トークン単位の部分一致は見逃しや誤検出を生むため行わない（例: contributor`山田 太郎`を`山田`/`太郎`に分けて対象`山田次郎`へ部分一致させることはしない）。

UserScriptの`MIN_PRICE`による221円以下の除外と、直近7日間という判定窓は使用しない。これらはGo側の新刊管理仕様に存在しないためである。

### 13.4 商品詳細確認

事前除外を通過した候補について、検索結果から次を取得できた場合は`new_release_result`を投入する。

- Kindle商品であることを示す情報（検索結果カードのKindle形式表示が`Kindle版`であること）
- 正のKindle価格
- 発売日
- 作者表記

Kindle種別は検索URLの`i=digital-text`だけで確定せず、カード内のKindle形式表示で`Kindle版`と確認できた候補だけをKindle版とする。確認できない候補は`new_release_result`へ進めない。発売日、価格、Kindle種別のいずれかを検索結果から確定できない場合だけ、候補ごとに`new_release_detail`を投入して商品ページを確認する。検索結果に発売日が存在しないこと自体は検索失敗としない。`new_release_result`と`new_release_detail`は同じdomain判定・保存ユースケースを呼び出す。

詳細ページで次を満たすことを必須とする。

- 要求したASINの商品ページである
- Kindle版である
- タイトルを取得できる
- 正のKindle価格を取得できる
- 発売日を取得できる
- 作者が一致する
- 除外キーワードと年月タイトル条件に該当しない

商品詳細でも発売日を取得できない場合は解析失敗として再試行する。

### 13.5 作者の最新作更新

候補の発売日が`LatestReleaseDate`より後なら、発売日が過去か将来かに関係なく次を更新する。

- `LatestReleaseDate`
- `LatestReleaseTitle`
- `LatestReleaseURL`

複数候補がある場合も、常に最も後の発売日を残す。作者一覧を既存の順序へ並べ直し、変更があった場合にAuthor用`gist_update`を投入する。

### 13.6 新刊通知と保存

新刊予定として通知・保存するのは、既存Go実装と同じく、`ReleaseDate.After(now)`を満たす未通知ASINとする。`ReleaseDate`の時刻が処理時刻以前なら、新刊予定としては通知しない。

1. 条件付き更新直前に`notified_asins.json`を読み直す
2. `ReleaseDate.After(now)`を満たさないレコードを通知履歴から除外する
3. 処理開始時に同じASINが通知済みだったかを記録する
4. 13.5 の作者最新作更新で変更があった場合、Author用`gist_update`を決定的なIDで投入する
5. `notified_asins.json`にASINがなければupsertする（将来発売分のみ）
6. `upcoming_asins.json`にASINがなければupsertする（将来発売分のみ）
7. 両S3 objectが期待状態になった後、処理開始時に未通知だった場合だけSlack・Mastodonへ通知する

Author gist の投入（手順4）を notified/upcoming の upsert（手順5-6）より前に置く。Gist は authors.json 全体から毎回再生成する（15）ため、投入済みの Author gist job は後続 upsert の失敗に依存せず生存する。再実行時は同じ決定的 job_id で冪等となり、authorChanged が false に変わっても job は欠損・重複しない。これにより 7.5「再実行で不足分を補う」を Gist 後続 job についても満たす。手順4は過去発売の候補で Author 最新作が更新された場合も適用する。商品通知（手順7）は将来発売分の S3 保存成功後という順序を維持する。

通知済み保存期間は既存Go実装と同じく、発売日が将来である間とする。UserScriptのlocalStorageは使用しない。

## 14. 紙書籍・Kindle版チェック

### 14.1 対象と周期

- 対象: `paper_books_asins.json`
- 周期: 6時間ごと
- 1日: 全対象を4周
- 確認ジョブ: 1紙書籍ASIN、商品ページ1リクエスト
- 詳細ジョブ: 1Kindle候補ASIN、商品ページ1リクエスト

### 14.2 紙書籍ページ確認

紙書籍の商品ページから次を抽出する。

- タイトル
- 紙書籍価格
- 紙書籍版スウォッチ
- Kindle版スウォッチ
- Kindle版へのURLとASIN

紙書籍版とKindle版の両スウォッチが存在する場合だけKindle候補ありとする。紙書籍版スウォッチがない場合は`not_paper_book`、Kindle版スウォッチがない場合は`kindle_not_available`として対象を保持する。

`CurrentPrice == 0`かつ紙書籍価格を取得できた場合だけ、既存Go実装と同じく紙書籍レコードの価格を初期化する。紙書籍価格を取得できなくても、両スウォッチとKindle候補を確認できる場合は詳細確認を続ける。通知では0円と表示せず、紙書籍価格を取得できなかったことを記載する。

Amazon内検索によるKindle候補探索は行わない。UserScriptと同様に、同一商品ページの形式スウォッチから得たKindle版だけを候補とする。

### 14.3 Kindle候補確認

`paper_to_kindle_detail`で次を確認する。

- Kindle候補ASINが紙書籍ASINと異なる
- Kindle版の商品ページである
- 正のKindle価格を取得できる
- タイトルと発売日を取得できる
- 紙書籍とKindle版の発売日が同じJST暦日である
- 候補URLが紙書籍ページのKindle版スウォッチから取得したものである

条件を満たさない場合は紙書籍対象を削除せず、`edition_mismatch`として記録する。

### 14.4 検出後の更新

1. `notified_asins.json`、`upcoming_asins.json`、`unprocessed_asins.json`を読み、処理開始時の既知状態を記録する
2. `unprocessed_asins.json`にない場合は、`notified_asins.json`と`upcoming_asins.json`の不足側へそれぞれupsertする
3. Kindle候補が`upcoming`または`unprocessed`に存在することを確認する
4. `paper_books_asins.json`から紙書籍ASINを削除する
5. Paper-to-Kindle用`gist_update`を決定的なIDで投入する
6. 処理開始時に3リストすべてで未知だった場合だけSlack・Mastodonへ通知する

既に`upcoming`または`unprocessed`に存在するKindle ASINの場合は再通知しない。途中でS3更新に失敗した場合はjobを失敗させ、再実行時に不足しているobjectだけを補う。紙書籍対象が先行実行で削除済みでも、Kindle候補が保存済みならGist更新jobの投入までは再実行する。候補も未保存なら手動削除として正常終了する。

## 15. GitHub Gist

既存の3つのGist更新を継続する。

| Gist | 更新契機 | 内容 |
|---|---|---|
| Sale | `sale_finalize`が`gist_update`を投入 | 発売日降順の`unprocessed_asins.json` |
| New Release | 作者最新作が変化した時に`gist_update`を投入 | 作者、作者URL、最新発売日、最新作、最新作URLの表 |
| Paper-to-Kindle | 紙書籍レコードの初期化・削除時に`gist_update`を投入 | 発売日降順の`paper_books_asins.json` |

Gist IDとfilenameは既存`checker_configs.json`の値を使用する。`gist_update`は実行時点のS3全体からMarkdownを再生成する。GitHub API失敗は当該Gistジョブのエラーとして再試行し、先に完了したS3更新は巻き戻さない。

GitHub HTTP clientのtimeoutは10秒とする。

SaleとPaper-to-Kindleは既存と同じ形式を使用する。

```text
## 合計 {count}冊
* [[YYYY-MM-DD]{Title} ({CurrentPrice}円)]({URL})
```

タイトルに`モンスターコミックス`を含む場合は、既存どおりタイトル末尾へ` 👹`を付ける。

Author Gistは次の形式を使用する。

```text
## 合計 {count}人(最新の単行本発売日降順)
| 作者 | 最新作 |
|------|--------|
| [{Name}]({URL}) | [[YYYY-MM-DD] {LatestReleaseTitle}]({LatestReleaseURL}) |
```

## 16. Checker設定

既存`checker_configs.json`の形は変更しない。新システムで使用するフィールドは次のとおりとする。

| 設定 | 使用 |
|---|---|
| 各Checkerの`Enabled` | `schedule-checks`がジョブ投入可否に使用 |
| 各Checkerの`GistID` | 使用 |
| 各Checkerの`GistFilename` | 使用 |
| `SaleChecker.SaleThreshold` | 価格差とポイント数に使用 |
| `SaleChecker.PointPercent` | ポイント還元率に使用 |
| `SaleChecker.PriceChangeAmount` | 価格変動通知に使用 |

次のPA API・旧スロット向けフィールドはJSONに残してよいが、新システムでは読み取っても使用しない。

- `ReportFailure`
- `ExecutionIntervalMinutes`
- `CycleDays`
- `GetItemsPaapiRetryCount`
- `GetItemsInitialRetrySeconds`
- `SearchItemsPaapiRetryCount`
- `SearchItemsInitialRetrySeconds`

周期はEventBridge Scheduler、再試行はSQS redrive設定を正とする。workerエラーを`ReportFailure=false`で成功に変換してはならない。

起動時に、使用する閾値が正の値であること、EnabledなCheckerにGist設定があることを検証する。不正値をデフォルト値で補完しない。

## 17. 通知

### 17.1 商品通知

既存Go実装と同じく、商品通知をSlack notice channelとMastodonへ送る。

- 新刊予定: タイトル、作者、発売日、ASIN、商品URL
- 紙書籍からKindle版: タイトル、紙書籍価格・URL、Kindle価格・URL
- セール: タイトル、成立した全条件、商品URL
- 価格変動: 旧価格、新価格、差額、商品URL

通知本文は既存Go形式を基準にする。

```text
📚 新刊予定があります: {Title}
作者: {AuthorName}
発売日: {YYYY-MM-DD}
ASIN: {ASIN}
{URL}
```

```text
📚 新刊予定があります: {KindleTitle}
📕 紙書籍({PaperPrice}円): {PaperURL}
📱 電子書籍({KindlePrice}円): {KindleURL}
```

紙書籍価格が未取得なら、2行目を`📕 紙書籍(価格取得不可): {PaperURL}`とする。

```text
📚 セール情報: {Title}
条件達成: {成立条件を空白区切り}
{URL}
```

```text
📈 プチ値上がり情報: {Title}
価格変動: {OldPrice}円 → {CurrentPrice}円 ({Diff}円)
{URL}
```

値下がりは先頭を`📉 プチ値下がり情報:`にする。ポイント還元率は小数第1位まで表示し、クーポン文言を取得できた場合は`✅ クーポンあり ({CouponText})`とする。

商品通知はS3更新成功後に行う。SlackまたはMastodonの一方が失敗しても、他方の送信と処理済みデータを巻き戻さない。

SlackとMastodonのHTTP clientはそれぞれ5秒timeoutとし、片方の失敗後も他方を実行する。通知adapterは各送信結果を個別に構造化ログへ出す。

商品通知は既存実装と同じbest-effortとし、通知outboxは追加しない。S3 commit後かつ通知前に実行環境が停止した場合は通知が欠落し、通知後かつSQS message削除前に停止した場合は再実行で重複する余地がある。S3状態の整合性を通知のexactly-once性より優先する。

### 17.2 運用エラー通知

個別のAmazonリクエスト失敗をその場でSlackへ送らない。再試行中のエラーはCloudWatch Logsだけへ記録する。

次のCloudWatch Alarmが`ALARM`へ遷移した時だけ、`schedule-checks`をAlarmイベントで起動してSlack error channelへ1件通知する。

- Work DLQの`ApproximateNumberOfMessagesVisible >= 1`
- Scheduler DLQの`ApproximateNumberOfMessagesVisible >= 1`
- Work Queueの`ApproximateAgeOfOldestMessage >= 7200秒`

同じAlarm状態の間に個別エラー数だけ通知を増やさない。復旧はCloudWatch Alarmの`OK`遷移で確認する。

## 18. ログとメトリクス

正常系と異常系をともに`log/slog`のJSON構造化ログで記録する。

### 18.1 共通フィールド

| フィールド | 内容 |
|---|---|
| `timestamp` | UTC時刻 |
| `level` | `INFO`、`WARN`、`ERROR` |
| `event` | 固定イベント名 |
| `check_type` | `sale`、`new_release`、`paper_to_kindle` |
| `job_id` | ジョブ識別子 |
| `cycle_id` | 周回識別子 |
| `target` | ASINまたは作者名 |
| `receive_count` | SQS受信回数 |
| `result` | 処理結果 |
| `error_type` | エラー分類。正常時は空 |
| `http_status` | Amazon HTTP status。未送信時は空 |
| `duration_ms` | ジョブ処理時間 |
| `response_bytes` | Amazon response byte数 |
| `aws_request_id` | Lambda request ID |
| `error` | エラー本文。正常時は空 |

Cookie、Authorization、アクセストークン、Slack token、GitHub token、HTML本文をログへ出さない。

### 18.2 初期メトリクス

- `level=ERROR`の構造化ログを、ログメトリクスフィルタで単一の`KindleAutomation/ErrorCount`へ集計する
- 正常件数、HTTP status別、`error_type`別のカスタムメトリクスは初期実装で作らない
- `PutMetricData`を直接呼び出さない
- Lambda、SQS、DLQのAWS標準メトリクスはそのまま利用する

運用ログを蓄積した後、分割する必要が確認できた項目だけを`http_status`または`error_type`別メトリクスへ追加する。

### 18.3 固定イベント名

- `cycle_dispatched`: 対象数、投入成功数、Upcoming取り込み数
- `cycle_disabled`: Checker設定により投入を省略
- `job_completed`: 正常処理結果
- `job_terminal`: 404、対象種別不一致等の再試行しない結果
- `job_error`: 再試行する処理エラー
- `notification_error`: Slack、Mastodon送信エラー
- `gist_error`: GitHub Gist更新エラー
- `alarm_notification`: CloudWatch AlarmのSlack通知結果

`cycle_dispatched`には`target_count`と`enqueued_count`を含め、周回が対象全件を投入したことをログだけで検証できるようにする。

## 19. 秘密情報とIAM

Amazon商品情報の取得には認証情報を使用しない。PA APIのAccess Key、Secret、Partner API資格情報は新Lambdaへ渡さない。

既存発売日通知と共有する次の値は、既存SSM Parameter Storeから必要な名前だけを取得する。

| 用途 | SSM key |
|---|---|
| Affiliate Tag | `AMAZON_PARTNER_TAG` |
| Slack Bot Token | `SLACK_BOT_TOKEN` |
| Slack notice channel | `SLACK_NOTICE_CHANNEL` |
| Slack error channel | `SLACK_ERROR_CHANNEL` |
| Mastodon server | `MASTODON_SERVER` |
| Mastodon client ID | `MASTODON_CLIENT_ID` |
| Mastodon client secret | `MASTODON_CLIENT_SECRET` |
| Mastodon access token | `MASTODON_ACCESS_TOKEN` |
| GitHub token | `GITHUB_TOKEN` |

既存コードと同じprefixを使用し、各keyを次の順序で個別取得する。

1. `/myapp/secure/{KEY}`を`WithDecryption=true`で`GetParameter`する
2. `ParameterNotFound`の場合だけ`/myapp/plain/{KEY}`を取得する
3. 両方に存在する場合はsecure側を使用する
4. 両方に存在しない場合は起動エラーにする

`GetParametersByPath`と`DescribeParameters`は使用しない。これにより、同じprefixにあるPA API資格情報や本処理に不要なparameterを読み取らない。

S3 bucket/key、SQS URL、ログレベルはSAMテンプレートからLambda環境変数へ渡す。秘密値を環境変数、SAM parameter、Git、ログへ平文で保存しない。

IAMは関数別に分ける。

### `schedule-checks`

- 対象S3キーの`GetObject`、`PutObject`
- Work Queueへの`SendMessage`
- 必要なSSM parameterの`GetParameter`または`GetParameters`
- SecureStringがcustomer managed KMS keyを使用する場合だけ、そのkeyの`kms:Decrypt`
- CloudWatch Logs出力

### `check-worker`

- 対象S3キーの`GetObject`、`PutObject`
- Work Queueへの後続`SendMessage`
- 必要なSSM parameterの読み取り
- SecureStringがcustomer managed KMS keyを使用する場合だけ、そのkeyの`kms:Decrypt`
- CloudWatch Logs出力

### その他

- Scheduler実行ロールは`lambda:InvokeFunction`を`schedule-checks`だけに限定する
- CloudWatch Alarmから`schedule-checks`を呼ぶresource-based permissionを付与する
- Work Queueの`sqs:ReceiveMessage`、`sqs:DeleteMessage`、`sqs:GetQueueAttributes`と、後続投入用`sqs:SendMessage`を`check-worker`へ限定する
- `Resource: *`を、CloudWatch Logs作成等でAWS仕様上必要な箇所以外に使用しない

## 20. 移行

### 20.1 現行データスナップショット

2026-07-23に既存S3を読み取った件数は次のとおりである。移行時は再取得し、この値を固定の期待件数として使用しない。

| Object | 件数 | 補足 |
|---|---:|---|
| `authors.json` | 517 | 既存5 field |
| `paper_books_asins.json` | 23 | 全件`CurrentPrice=0` |
| `unprocessed_asins.json` | 237 | 13件が`CurrentPrice=0` |
| `notified_asins.json` | 59 | 既存発売日通知と共有 |
| `upcoming_asins.json` | 0 | 空配列 |
| `excluded_title_keywords.json` | 14 | 新刊除外語 |

同日時点のSale設定は`SaleThreshold=151`、`PointPercent=20`、`PriceChangeAmount=100`である。これらをsource codeへ固定せず、実行時の`checker_configs.json`を使用する。

### 20.2 価格履歴初期化

UserScriptのOption+↑で生成した既存`MaxPrice`には紙書籍価格が入るため、過去最高Kindle価格として利用できない。リリース切り替え時に、次の3オブジェクトへ一度だけパッチを適用する。

- `unprocessed_asins.json`
- `upcoming_asins.json`
- `notified_asins.json`

各レコードを次のように更新する。

```text
MaxPrice = CurrentPrice
```

`CurrentPrice == 0`なら`MaxPrice`も0にする。最初の正常取得で`CurrentPrice`と`MaxPrice`を同じ価格に初期化する。`paper_books_asins.json`はセール価格履歴ではないため、このパッチ対象にしない。

パッチはASIN件数、ASIN集合、`CurrentPrice`、他フィールドを変更してはならない。実行前後の差分と件数を検証する。

### 20.3 切り替え手順

前提: 対象S3 bucket の Versioning が `Enabled` であること。`Suspended` や未設定の場合は本番切り替えを行わない（§20.4 の version 指定 rollback が成立しないため）。Versioning=Enabled なら backup prefix への object copy は作らず、切り替え直前の各 object の現時点 VersionId を記録して rollback 基準にする。

1. 新しいSAMリソースをScheduler無効状態でデプロイする
2. bucket の Versioning が `Enabled` であることを確認する。Enabled でなければ切り替えを中止する。Enabled なら対象 object（`authors.json`、`paper_books_asins.json`、`unprocessed_asins.json`、`upcoming_asins.json`、`notified_asins.json`）の現時点 VersionId を記録し、backup prefix への copy は行わない
3. 既存の新刊・セール・紙書籍CheckerのEventBridge triggerを無効にする
4. `MaxPrice`初期化パッチをdry-runし、変更件数を確認する
5. パッチを実行し、JSON schema、ASIN集合、価格差分を再検証する
6. 既知HTML fixtureを使って新Lambdaを確認する
7. 明示的な手動invokeでSQSから1件ずつ疎通確認する
8. SQS event source mappingを有効にする
9. 3つのSchedulerを有効にする
10. 既存`release-notifier`が有効なことを確認する

### 20.4 ロールバック

rollback は §20.3 step2 で記録した VersionId を基準とする。切り替え後に発生した手動編集を盲目的に上書きしないため、配列全体の無条件上書きは行わず、object ごとに復元要否を判断する。

1. 新SchedulerとSQS event source mappingを無効にする
2. 旧3 Checkerのtriggerを再有効化する
3. 対象 object ごとに、記録した VersionId の内容と現行データを比較する
4. 新システムだけが変更した部分のみ、記録した旧 version の内容へ選択的に戻す。切り替え後に手動で追加・変更・削除されたレコードは保持する

S3 backupを配列全体で無条件に上書きしてロールバックしない。Versioning が `Enabled` でない場合は version 指定での選択的復元ができないため、切り替えへ進まない（§20.3 前提）。

## 21. PoCで確認済みの事実

2026-07-22から2026-07-23にGoの`net/http`と`goquery`で確認した。

- ローカルとLambdaの双方からAmazon Japanの商品ページへ生HTTPリクエストが通った
- Lambda `provided.al2023`、x86_64でGoバイナリが動作した
- `B0FX3X569X`はHTTP 200で、タイトル、Kindle価格776円、紙書籍価格792円、388ポイントを取得した
- `B0CX8CD1XL`はHTTP 200で、書籍以外の商品からも「10% OFF」のクーポンを取得した
- 無効な`B000000000`はHTTP 404となり、商品ページとして無効と判定できた
- 作者「海李」の検索ページはHTTP 200で16件の検索結果を取得した
- 検索結果に発売日がない商品でも、商品詳細ページでは発売日を取得できた
- Goの最初のリクエストで一度だけHTTP 200・3,869 bytesの短い応答を観測したが、本文を保存しておらず原因は特定できていない
- 別ASINを使った10回の追加確認では短い応答を再現しなかった

短い応答の原因は断定せず、HTTP statusだけで成功扱いしない構造検証とfixtureテストで対処する。

## 22. テスト要件

### 22.1 単体テスト

- セール4条件をそれぞれ単独で成立させる
- 複数セール条件を同時に列挙する
- `MaxPrice`、`CurrentPrice`、初回価格の更新
- 値上がり・値下がりとセール通知の排他
- 作者名正規化、除外キーワード、年月タイトル、ISBN除外
- 通知履歴の将来発売分だけを残す処理
- 紙書籍とKindle版の発売日一致
- Book、Authorの重複排除と並び順
- `cycle_id`、`job_id`の決定性
- SQS job schema validation

### 22.2 HTML fixtureテスト

- 通常のKindle商品
- ポイントあり
- ポイントなし
- クーポンあり。直接テキストノードだけを取得する
- クーポンなし
- Kindle Unlimited表示を含む商品
- 紙書籍とKindle版の両スウォッチあり
- Kindle版スウォッチなし
- 検索結果に発売日あり
- 検索結果に発売日なし
- CAPTCHA、アクセス拒否、短い200本文
- 404商品不存在
- 必須価格・タイトル欠落

fixtureは実HTMLを`testdata`へ固定保存し、テストからAmazonへアクセスしない。

### 22.3 S3競合テスト

- ETag一致時の更新
- 412後に最新本文へmergeして再試行
- 手動追加された別ASINを保持
- 手動削除された処理対象を再追加しない
- 処理中にUpcomingが増えた場合に消去しない
- 同一ASINの重複実行でレコードが増えない
- 未知フィールドを保持する

### 22.4 ユースケース・ハンドラーテスト

- 各Scheduler起動で対象件数と同数のジョブを投入する
- Amazonへアクセスし得る全ジョブのMessageGroupIdが`amazon-requests`になる
- `gist_update`のMessageGroupIdが`external-updates`になり、Amazon clientを呼ばない
- 1 worker起動のAmazonリクエストが最大1回になる
- 検索・紙書籍確認から後続ジョブを投入する
- 新刊検索job自身がS3保存と商品通知を行わない
- 検索項目が揃う候補は`new_release_result`、不足候補は`new_release_detail`になる
- S3変更後のGist更新を0リクエストの別ジョブとして再試行できる
- retryable errorをLambdaエラーとして返す
- terminal resultを再試行せず対象リストへ残す
- S3保存失敗時に商品通知しない
- 通知失敗時に保存済み価格を巻き戻さない
- `sale_finalize`が1周1回だけSale用`gist_update`を投入する

## 23. 受け入れ条件

- 本番Lambdaは`schedule-checks`と`check-worker`の2本だけである
- API Gateway、ヘッドレスブラウザ、PA API資格情報を使用していない
- 1 worker起動のAmazonリクエストが0回または1回である
- Amazonリクエストが同時に2件実行されない
- セール対象全件を2時間ごとに1周する
- 作者全件と紙書籍全件を1日4周する
- セール判定4条件が独立して動作する
- セール成立時も価格履歴が更新される
- 紙書籍価格をセール判定へ使用していない
- UserScriptと同じ方法でクーポン文言を取得できる
- 既存S3 JSONのschema、手動追加、Upcoming競合回避を維持する
- 既存`release-notifier`が同じS3データで動作する
- エラーと正常処理が同じ形式の構造化ログへ出る
- 初期カスタムメトリクスはログ由来の単一ErrorCountだけである
- 個別AmazonエラーをSlackへ連投しない
- Work DLQ、Scheduler DLQ、2時間超の滞留をSlackで検知できる
- 移行前後で対象ASIN集合が変化していない
- `go test -race ./...`、`go vet ./...`、`staticcheck ./...`、`govulncheck ./...`が成功する

## 24. 実装順序

1. ドメイン型、既存JSON codec、Checker設定validation
2. Amazon HTTP client、response分類、goquery extractor、fixtureテスト
3. セール判定と価格履歴更新
4. 新刊検索・詳細の2段階ユースケース
5. 紙書籍確認・Kindle詳細の2段階ユースケース
6. S3 ETag付きmerge adapter
7. SQS job codec、dispatcher、worker router
8. Slack、Mastodon、GitHub Gist adapter
9. 構造化ログ、ログメトリクスフィルタ、Alarm通知
10. SAMテンプレートとデプロイスクリプト
11. `MaxPrice`移行スクリプトとdry-run
12. AWS上の段階的な疎通確認と切り替え

## 25. 実装開始時点の判断残り

ユーザー判断が必要な仕様項目は残っていない。以下はリリース後の構造化ログに基づいて調整する運用値であり、実装開始の阻害要因ではない。

- Amazon HTML変更に伴うセレクタ追加
- 15秒HTTP timeoutの変更
- SQSの5回再試行回数の変更
- 1件直列処理で2時間以内に終わらなくなった場合のMessageGroup分割
- 実測傾向に基づく`error_type`別メトリクス追加

## 26. 運用手順

### 26.1 Work DLQ

1. Alarm通知のqueue名とmessage数を確認する
2. DLQ messageの`job_id`、`cycle_id`、`kind`、targetを確認する
3. `job_id`でCloudWatch Logsを検索し、5回分の`error_type`、HTTP status、response bytesを比較する
4. 単一targetの問題か、同じ時刻の複数targetに共通する問題かを判定する
5. selector・code・対象dataの必要な修正を行い、fixtureと自動testを追加する
6. 修正版をdeployする
7. DLQ messageをWork Queueへredriveする
8. `job_completed`、DLQ空、AlarmのOK遷移を確認する

原因確認前にDLQ messageを削除しない。404、商品種別不一致等のterminal resultはDLQへ入らない。

### 26.2 Queue滞留

`ApproximateAgeOfOldestMessage`が2時間を超えた場合は、次を確認する。

- 同じAmazon jobが再試行を繰り返していないか
- 403、429、CAPTCHA、短い200本文が複数targetで発生していないか
- 1件の平均`duration_ms`が増加していないか
- 直前の周回が次のセール周回までに終了しているか

原因を確認せずにMessageGroupIdを分割しない。分割するとAmazon同時リクエスト数が増えるため、構造化ログで直列処理が周期内に収まらないことを確認した場合だけ変更する。

### 26.3 ローカルJSON編集

- upload直前に対象S3 objectの最新版を取得する
- ローカル追加分を最新版へmergeしてからuploadする
- 自動処理が先に書き込んだ内容を、古いローカルcopyで配列全体上書きしない
- upload後に件数と追加ASINまたは作者を確認する

自動処理側はETag競合時に手動変更を読み直してmergeする。手動側が古いcopyを後から無条件uploadした場合はS3の条件付き書き込みでは検出できないため、上記手順で回避する。

## 27. AWS仕様参照

- [LambdaからSQSを処理する際の少なくとも1回配信](https://docs.aws.amazon.com/lambda/latest/dg/with-sqs.html)
- [SQS FIFOのMessageGroupIdごとのLambda同時実行](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/fifo-queue-lambda-behavior.html)
- [SQS visibility timeoutをLambda timeoutの6倍以上にする設定](https://docs.aws.amazon.com/lambda/latest/dg/services-sqs-configure.html)
- [SQS FIFOの5分間重複排除](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/FIFO-key-terms.html)
- [S3 `If-Match`条件付き書き込み](https://docs.aws.amazon.com/AmazonS3/latest/userguide/conditional-writes.html)
- [EventBridge Schedulerのcronとタイムゾーン](https://docs.aws.amazon.com/scheduler/latest/UserGuide/schedule-types.html)
- [EventBridge Schedulerのcontext attribute](https://docs.aws.amazon.com/scheduler/latest/UserGuide/managing-schedule-context-attributes.html)
- [EventBridge Schedulerの再試行設定](https://docs.aws.amazon.com/scheduler/latest/APIReference/API_RetryPolicy.html)
- [VPC未接続Lambdaのインターネット接続](https://docs.aws.amazon.com/lambda/latest/dg/configuration-vpc-internet.html)
- [CloudWatch AlarmからLambdaを呼ぶAction](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/alarms-and-actions-Lambda.html)
