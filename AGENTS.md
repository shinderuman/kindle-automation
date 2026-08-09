# kindle-automation 開発ルール

このファイルは`kindle-automation`固有の実装規約です。業務仕様は`SPECIFICATION.md`を正とし、本ファイルはその実装方法と品質基準を定めます。

## 1. 仕様と実装範囲

- 本番コードはGoで実装する
- 本番Lambdaは`schedule-checks`と`check-worker`の2本だけにする
- Amazonへの取得は`net/http`の生HTTPリクエストを使用する
- Amazon HTMLの解析は`goquery`を使用する
- AWS SDKはAWS SDK for Go v2を使用する
- IaCはAWS SAMで管理する
- `authors.json`、`paper_books_asins.json`、`unprocessed_asins.json`を含む既存S3 JSONの契約を変更しない
- UserScriptのAmazon取得項目とセレクタを参照し、業務管理は既存Go実装に合わせる
- 発売日当日の通知機能は本リポジトリへ実装しない
- シリーズ一括購入向け機能は実装しない
- ヘッドレスブラウザ、Node.js、API Gateway、Fargate、Step Functions、DynamoDBを本番構成へ追加しない
- 仕様変更が必要になった場合は、コードより先に`SPECIFICATION.md`を更新する

実装間で挙動が競合する場合の優先順位は次とする。

1. `SPECIFICATION.md`の明示的な要件
2. 既存`kindle_bot`の業務判定、S3管理、履歴、通知、並び順
3. UserScriptのAmazon URL、HTMLセレクタ、値抽出

## 2. 標準ディレクトリ構成

```text
kindle-automation/
├── cmd/
│   ├── schedule-checks/              # 対象投入・Alarm通知Lambda（薄いentrypoint）
│   └── check-worker/                 # SQSジョブ処理Lambda（薄いentrypoint）
├── internal/
│   ├── domain/                       # 外部サービスに依存しない業務ルール
│   │   ├── author/
│   │   ├── book/
│   │   ├── sale/
│   │   └── scheduling/
│   ├── application/                  # ユースケース
│   │   ├── dispatch/
│   │   ├── newrelease/
│   │   ├── sale/
│   │   └── papertokindle/
│   ├── lambda/                       # 各Lambdaのcomposition root
│   │   ├── checkworker/              # 起動・依存組み立て・event decode・振り分け・DTO/adapter
│   │   └── schedulechecks/           # 起動・依存組み立て・event decode・振り分け・adapter
│   ├── job/                          # SQSメッセージ型・validation・routing
│   ├── amazon/                       # HTTP client・goquery抽出・response分類
│   │   └── selectors/                # CSSセレクタの単一管理場所
│   ├── storage/                      # S3 JSON・ETag付きmerge adapter
│   ├── queue/                        # SQS FIFO adapter
│   ├── notification/                 # Slack・Mastodon adapter
│   ├── gist/                         # GitHub Gist adapter
│   ├── config/                       # 環境変数・S3 Checker設定・SSM
│   └── logging/                      # slog JSON logger
├── testdata/
│   ├── amazon/                       # 商品・検索HTML fixture
│   └── s3/                           # 既存JSON fixture
├── infra/
│   └── template.yaml                 # AWS SAMテンプレート
├── scripts/                          # build・deploy・移行・検証
├── docs/                             # 補助設計・運用手順
├── lambda-poc-go/                    # Go PoC。本番からimportしない
├── lambda-poc/                       # 旧Node.js PoC。本番からimportしない
├── AGENTS.md
├── SPECIFICATION.md
├── go.mod
├── go.sum
└── Makefile
```

`pkg`は外部リポジトリから利用する公開ライブラリを作る場合だけ使用する。本アプリケーション内の共有コードは`internal`へ置く。

ディレクトリを先に細分化しない。2つ以上の実装ファイルまたは明確な独立責務が生じた時にパッケージを分ける。

### 依存方向

```text
cmd → internal/lambda → application / job / config / logging
internal/lambda → amazon / storage / queue / notification / gist （composition root での組み立てとbridge）
application → domain
application → 利用側で定義したinterface
amazon / storage / queue / notification / gist → 外部サービス
```

- `cmd`は各Lambdaの`main()`だけとし、対応する`internal/lambda` package の起動関数を呼ぶ薄いentrypointとする
- `internal/lambda/{checkworker,schedulechecks}` が composition root として AWS/config 依存組み立て、event decode、use-case 振り分け、application と各 adapter 間の DTO 変換・bridge を担う
- `domain`から`application`、AWS SDK、Lambda、HTTP、S3、SQS、Slack、Mastodon、GitHubをimportしない
- `application`で必要なinterfaceを利用側パッケージへ定義する
- adapter側の都合でinterfaceをdomainへ置かない
- `application`、`amazon`、`storage`などcomposition rootより下位の層から`internal/lambda`をimportしない
- `cmd`同士、`internal/lambda`同士、PoC、本番パッケージ間でimportしない

## 3. Go設計

- Go標準パッケージを優先し、外部依存はAWS SDK、Lambda runtime、goquery等の必要なものに限定する
- パッケージ名は短い小文字の名詞にする
- `Manager`、`Service`、`Helper`、`Util`へ無関係な責務を集約しない
- interfaceは利用側に必要な最小メソッドだけ定義する
- 依存性はコンストラクタまたは関数引数で注入する
- package-levelの可変状態を使用しない
- Lambda実行環境で再利用するHTTP・AWS clientは、不変な依存として初期化する
- `context.Context`は外部I/Oとキャンセル可能処理の第1引数に渡し、構造体へ保存しない
- goroutineを使用する場合は終了条件、cancel、エラー回収を実装する
- 本システムはAmazonリクエストを直列化するため、workerユースケース内でgoroutineによる並列取得を行わない
- 業務判定は入力と出力が明確な純粋関数を優先する
- 時刻依存処理はClock interfaceまたは関数引数から時刻を受け取る
- 金額は既存JSON互換の型とし、計算時の丸め規則をテストする
- 取得値の存在有無を0や空文字だけで表現せず、値と状態を型で区別する
- `any`、不要なpointer、意味のない型変換を避ける
- functional optionは必須引数を隠す用途に使用しない
- 生成コードやmock frameworkを導入せず、まず小さな手書きstubを使用する

### エラー

- `fmt.Errorf("処理文脈: %w", err)`で原因を保持する
- エラー文字列を比較せず、sentinel error、`errors.Is`、`errors.As`を使用する
- retryable、terminal、target mismatchを型または明示的なcodeで区別する
- エラーをログへ出しただけで成功扱いに変換しない
- `panic`は起動時に依存を組み立てられない場合等、継続不能な初期化エラーに限定する
- catch相当箇所ではerror本文と分類を必ず構造化ログへ含める

## 4. LambdaとSQS

### ハンドラー

- `cmd`は`main()`だけとし、対応する`internal/lambda` package の起動関数を呼ぶ
- composition root（`internal/lambda`）はイベントdecode、validation、依存組み立て、ユースケース呼び出し、結果変換だけを行う
- composition root へ業務判定、HTML selector、S3 mergeロジックを書かない
- `schedule-checks`からAmazon、GitHub Gist、商品通知へアクセスしない
- `check-worker`は1起動につきAmazonへ0回または1回だけアクセスする
- 商品検索後に商品詳細が必要な場合は、同じ起動で続けず後続SQSジョブを作る
- 新刊検索jobはS3保存と商品通知を行わず、候補ごとに`new_release_result`または`new_release_detail`を投入する
- `sale_finalize`はSale用`gist_update`を投入するだけにし、GitHub APIは`gist_update`からだけ呼ぶ

### Work Queue

- FIFO Queueを使用する
- Amazonへアクセスし得るメッセージの`MessageGroupId`を`amazon-requests`に固定する
- `gist_update`だけは`external-updates`を使用し、Amazon clientとS3書き込みを禁止する
- Lambda event source mappingの`BatchSize`を1にする
- Amazon直列化のためにLambda reserved concurrency=1を設定しない
- `MessageDeduplicationId`は決定的な`job_id`のSHA-256 lowercase hexを使用する
- `SendMessageBatch`は10件単位を順番に送り、batch内の失敗entryを見落とさない
- `sale_finalize`は全`sale_check`送信成功後に投入する
- SQSメッセージへS3レコード全体を入れない
- job kindに不要なtarget fieldを空値で送らない
- workerは対象ASINまたは作者名を基に最新S3データを読み直す
- HTTP client内で再試行せず、retryable errorはLambdaエラーとしてSQSへ返す
- terminal resultは再試行せず、対象をリストへ残す
- SQSの少なくとも1回配信を前提に、S3状態遷移を冪等にする
- 複数S3 object更新はtransactionとみなさず、再実行時に不足objectだけをreconcileする
- 通知済みASINを理由にUpcoming補完やGist job投入を省略しない
- FIFOの重複排除時間だけを冪等性の根拠にしない
- 失敗対象を失わず、`job_id`、`cycle_id`、対象、受信回数、error分類をログへ出す

### Job型

初期job kindは次に限定する。

- `sale_check`
- `sale_finalize`
- `new_release_search`
- `new_release_result`
- `new_release_detail`
- `paper_to_kindle_check`
- `paper_to_kindle_detail`
- `gist_update`

job schemaを変更する場合は`version`を使って後方互換性または明示的な拒否を実装する。

未対応version、未知kind、必須field欠落はAmazonへアクセスせずerrorを返し、DLQへ残せるようにする。

## 5. Amazon取得

- `net/http`で`https://www.amazon.co.jp`へGETする
- User-Agent、Accept、Accept-Language、15秒timeoutを設定する
- Cookie、Authorization、Amazonログイン情報を送らない
- redirect回数とredirect先hostを検証する
- response bodyを8 MiBで制限する
- HTTP statusだけで成功を判定しない
- 商品ページは要求ASIN、`#productTitle`、処理に必要な要素を検証する
- 検索ページは検索結果ページの識別要素と結果containerを検証する
- CAPTCHA、アクセス拒否、商品不存在、短い本文、要素欠落を異なる結果型で返す
- 403、429、5xx、timeout、通信失敗、取得内容不足をretryableとする
- 404、商品種別不一致をterminal resultとする
- Kindle価格を取得できない場合に0円を保存しない
- ポイント要素なしは0ポイント、クーポン要素なしはクーポンなしとして扱う
- HTML全体に正規表現を適用して値を抽出しない
- HTML解析はgoqueryを使用する
- CSS selectorを`internal/amazon/selectors`へ集約する
- 複数selectorは優先順を明示し、最初に有効な値を返す
- クーポン文言は`.couponLabelText`の最初の直接text nodeだけを取得する
- selector変更時は、変更前に失敗fixtureを追加する
- 実Amazon HTMLを`testdata/amazon`へ保存し、単体テストからネットワークへアクセスしない

## 6. ドメインルール

次を外部サービスに依存しない関数として実装する。

- 過去最高Kindle価格と現在価格の差分判定
- ポイント数判定
- ポイント還元率計算
- クーポン判定結果の統合
- CurrentPrice、MaxPriceの更新
- 価格変動判定
- 新刊の除外条件と作者名正規化
- 通知済み新刊の保存期間判定
- 紙書籍とKindle版の対応判定
- Author、Bookの重複排除と並び順
- `cycle_id`、`job_id`生成
- Upcomingのmerge・消去可否判定

セールの価格差、ポイント数、ポイント還元率、クーポンは独立した条件とし、主従を設けない。紙書籍価格とKindle価格の差はセール条件に使用しない。

セール条件が成立した場合もCurrentPriceとMaxPriceを更新する。既存Go実装の保存漏れを再現しない。

## 7. S3と既存JSON

- `SPECIFICATION.md`に記載された既存object keyを維持する
- 既存フィールド名、JSON型、UTC RFC 3339日時、4スペースindent、並び順を維持する
- JSON encode時に`&`を`\u0026`へ変換しない
- ASINまたは作者名以外のレコードを更新しない
- 手動で追加・変更・削除されたレコードを保護する
- 手動削除済み対象をworkerが再追加しない
- 既存schemaにないfieldをdecode・encodeで消さない
- S3 adapterは本文とETagを一緒に返す
- `PutObject`は`If-Match`を使用する
- 409・412時は最新本文へmergeし直し、最大3回まで再試行する
- 無条件のobject全体上書きを禁止する
- JSON decode、validation、merge、encodeをstorage adapterまたは専用codecへ集約する

### Upcoming

- 新刊・Kindle版検出結果を`unprocessed_asins.json`へ直接追加しない
- `notified_asins.json`と`upcoming_asins.json`へupsertする
- セールdispatch開始時にUpcomingをUnprocessedへ条件付きmergeする
- 重複ASINは既存Unprocessed側を優先する
- UpcomingのETagが変わっていない場合だけ空配列にする
- 処理中に追加されたUpcomingを消去しない
- DynamoDBや分散lockを追加しない

## 8. 設定と秘密情報

- セール閾値と各CheckerのEnabled、Gist設定は既存`checker_configs.json`を正とする
- EventBridge周期とSQS再試行回数をChecker JSONから制御しない
- 旧PA API retry、CycleDays、ExecutionIntervalMinutes、ReportFailureを新処理へ適用しない
- 設定を起動時にvalidationし、不正値を暗黙にdefault補完しない
- S3 key、queue URL、log levelは環境変数からconfig構造体へ集約する
- 環境変数をdomain層から参照しない
- Amazon PA API資格情報を新Lambdaへ渡さない
- Slack、Mastodon、GitHub tokenはSSM Parameter Storeから必要なparameterだけ取得する
- 必要keyは`/myapp/secure/{KEY}`を先に個別取得し、存在しない場合だけ`/myapp/plain/{KEY}`へfallbackする
- `GetParametersByPath`と`DescribeParameters`で不要なparameterを列挙しない
- 秘密情報をsource、Git、SAM template、Lambda event、SQS message、ログへ記録しない

## 9. 通知とGist

- 商品通知はS3保存成功後にSlack notice channelとMastodonへ送る
- Slack・Mastodonは各5秒、GitHub Gistは10秒のHTTP timeoutを設定する
- 通知adapter失敗時に保存済みdomain stateを巻き戻さない
- 商品通知outboxを追加せず、best-effortであることをtestと運用文書で隠さない
- 個別AmazonリクエストエラーをSlackへ送らない
- Work DLQ、Scheduler DLQ、queue滞留AlarmだけをSlack error channelへ通知する
- 同じAlarm状態でエラー件数分の通知を増やさない
- Gistは現在のS3全体から毎回再生成する
- Gist API失敗はGist jobを失敗させ、SQSで再試行する
- Gist失敗を理由に先行するS3更新を巻き戻さない

## 10. 構造化ログとメトリクス

- `log/slog`のJSON handlerを使用する
- 正常結果、terminal result、retryable errorを同じ固定field形式で出す
- `SPECIFICATION.md`の共通ログfieldを省略しない
- `job_id`、`cycle_id`、対象、SQS受信回数、result、error_type、HTTP status、duration、response bytes、Lambda request IDを含める
- errorを通知だけしてログから失わない
- Cookie、Authorization、token、HTML本文をログへ出さない
- 初期custom metricは`level=ERROR`を集計する単一`KindleAutomation/ErrorCount`だけにする
- 初期実装で`PutMetricData`を直接呼ばない
- 発生傾向を確認する前にstatus別、error_type別metricを追加しない
- Lambda、SQS、DLQのAWS標準metricは利用する

## 11. テスト

Go標準`testing` packageを使用し、table-driven testを優先する。test名は入力ではなく期待する振る舞いを表す。

### 必須テスト

- `SPECIFICATION.md`に列挙されたdomain判定の単体テスト
- SQS job codec、version、validation、routing
- 1 worker起動でAmazon clientが最大1回だけ呼ばれること
- Scheduleごとの対象件数、重複排除、決定的ID
- Amazon商品・検索HTML fixtureの抽出
- CAPTCHA、短い200、404、要素欠落、価格解析失敗の分類
- S3 JSONの入出力契約と未知field保持
- ETag競合、手動追加、手動削除、Upcoming競合
- 外部adapterをstubへ差し替えた各ユースケース
- Lambdaハンドラーのevent decodeとerror伝播
- 通知とGistの失敗時の状態
- 移行scriptのdry-run、MaxPriceだけが変わること

### テスト規則

- 自動テストから実AWS、Amazon、Slack、Mastodon、GitHubへアクセスしない
- 外部サービスは利用側interfaceと手書きstubへ差し替える
- 成功例だけでなく、値欠落、重複配信、S3競合、途中失敗をテストする
- 時刻を固定する
- test間で外部状態やpackage-level可変状態を共有しない
- fixtureの期待値を本番selector実装から生成しない
- PoCの疎通結果を自動テスト成功の代わりにしない

## 12. 品質確認

Goファイルを編集したら、変更した全Goファイルへ`gofmt`を実行する。その後、リポジトリルートで次を実行する。

```bash
gofmt -l $(rg --files -g '*.go')
go test ./...
go vet ./...
staticcheck ./...
```

リポジトリ全体の提出前確認は次とする。

```bash
gofmt -l $(rg --files -g '*.go')
go test -race ./...
go vet ./...
staticcheck ./...
govulncheck ./...
```

続けて`cmd/schedule-checks`と`cmd/check-worker`の各ディレクトリで、次を実行する。

```bash
go build -o /dev/null .
```

- `gofmt -l`の出力を空にする
- test、vet、staticcheck、govulncheckのerrorとwarningを残さない
- lint無効化は理由を直前に記載し、最小範囲に限定する
- toolが未導入の場合は勝手に確認済み扱いせず、未実行理由を報告する
- Lambda buildは`GOOS=linux GOARCH=amd64 CGO_ENABLED=0`を指定し、各cmdを個別にbuildする
- 生成した`bootstrap`、ZIP、SAM build directoryをGit管理対象へ入れない

## 13. IaCとデプロイ

- Lambda、SQS、DLQ、EventBridge Scheduler、event source mapping、CloudWatch Logs、metric filter、Alarm、IAMを`infra/template.yaml`へ定義する
- 2つのLambda Log Groupは保持30日を明示する
- Consoleで作成した本番設定をIaC外へ残さない
- Schedulerは初期deploy時に無効化できるparameterを持たせる
- Lambdaはユーザー管理VPCへ接続しない
- SQS visibility timeoutをworker timeoutの6倍以上にする
- Work QueueはFIFO、BatchSize=1とし、job kindごとのMessageGroupIdをapplication側で固定する
- Work Queueと2つのDLQはSSE-SQSを有効にする
- IAM roleを2 Lambdaで共有しない
- 既存S3 bucketそのものをstackの削除対象にしない
- deploy scriptはbuild、package、change set確認、deployを分離する
- deployは明示的なAWS profileとregionを要求し、default値へ暗黙依存しない
- 移行scriptはdry-runを既定とし、applyを明示指定させる

## 14. PoCと生成物

- `lambda-poc-go`と`lambda-poc`を本番codeからimportしない
- PoCの固定ASIN、URL、selectorを検証なしで本番configへ持ち込まない
- PoCで得た挙動は仕様、fixture、testのいずれかへ反映してから本番実装する
- `bootstrap`、`function.zip`、invoke結果、取得HTML等の生成物を本番sourceへ混在させない
- 旧Node.js PoCを本番実装の設計基準にしない

## 15. レビュー完了条件

作業完了報告前に、変更範囲に応じて次を確認する。

- `SPECIFICATION.md`と実装が一致する
- 本番Lambda entrypointが2本だけである
- workerのAmazon呼び出しが1起動最大1回である
- Amazon外部アクセスが`internal/amazon`へ限定されている
- Lambda handlerが薄い
- domainがAWS SDKとHTTPに依存していない
- 既存S3 schema、未知field、手動編集が保持される
- Upcoming競合処理が維持される
- セール4条件が独立し、紙書籍価格を使っていない
- セール成立時も価格履歴を保存する
- クーポンの直接text nodeを抽出する
- 個別AmazonエラーをSlack通知しない
- 正常・異常の構造化ログfieldが一致する
- 秘密情報がsource、event、ログへ含まれない
- 新しい業務ロジックに単体テストがある
- `gofmt`、test、vet、staticcheck、govulncheck、2 cmdのbuild結果を確認した
- 依頼範囲外のファイルを変更していない
