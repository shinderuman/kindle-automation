# Amazon取得方式 PoC計画

## 1. 目的

本PoCの目的は機能を実装することではなく、Amazon取得方式が本番運用に耐えるかをGo/No-Go判定することである。

次の事項を確定する。

1. ローカル環境とAWS LambdaからAmazonの商品・検索HTMLを安定して取得できるか
2. HTTP 200で商品ページ以外が返された場合に、正常応答と混同せず分類・調査できるか
3. 生HTTP方式を採用するか、ヘッドレスブラウザPoCへ移るか、UserScript運用を維持するか

GoかNode.jsかは主な判定対象ではない。同じ生HTTPリクエストに対してAmazonから何が返るか、およびLambda環境で再現性があるかを判定する。

## 2. PoCの制約

- PoC結果を一覧化し、ユーザーが方式採用を明示するまで本番実装へ着手しない
- SQS、業務用S3データ管理、Scheduler、通知、Gist、移行処理をPoCへ追加しない
- API Gatewayを追加しない
- AmazonへCookie、Authorization、ログイン情報を送信しない
- Amazonへのアクセスは直列化する
- 判定基準はPoC開始前に確定し、結果を見てから変更しない
- 失敗を成功扱いにするfallbackを追加しない
- ポイント率、クーポン、価格等の変動値そのものを固定assertしない

## 3. 固定テスト対象

### 3.1 商品ASIN

| 用途 | ASIN |
|---|---|
| 正常商品として確認済み | `[**B0GRC814GK**](https://www.amazon.co.jp/dp/B0GRC814GK "Amazon商品ページを新しいタブで開く: B0GRC814GK")` |
| 初期PoC対象 | `[**B0FX3X569X**](https://www.amazon.co.jp/dp/B0FX3X569X "Amazon商品ページを新しいタブで開く: B0FX3X569X")` |
| クーポン観測用（非書籍） | `[**B0CX8CD1XL**](https://www.amazon.co.jp/dp/B0CX8CD1XL "Amazon商品ページを新しいタブで開く: B0CX8CD1XL")` |
| 初回リリースで解析失敗 | `[**B0FP14RH57**](https://www.amazon.co.jp/dp/B0FP14RH57 "Amazon商品ページを新しいタブで開く: B0FP14RH57")` |
| 初回リリースで解析失敗 | `[**B0FGZSWQXT**](https://www.amazon.co.jp/dp/B0FGZSWQXT "Amazon商品ページを新しいタブで開く: B0FGZSWQXT")` |
| 年齢確認対象 | `[**B0GTK26XJT**](https://www.amazon.co.jp/dp/B0GTK26XJT "Amazon商品ページを新しいタブで開く: B0GTK26XJT")` |
| 年齢確認対象 | `[**B0G37K6BD6**](https://www.amazon.co.jp/dp/B0G37K6BD6 "Amazon商品ページを新しいタブで開く: B0G37K6BD6")` |
| 年齢確認対象 | `[**B0FJKNH1TY**](https://www.amazon.co.jp/dp/B0FJKNH1TY "Amazon商品ページを新しいタブで開く: B0FJKNH1TY")` |
| 年齢確認対象 | `[**B0H9WBW2YF**](https://www.amazon.co.jp/dp/B0H9WBW2YF "Amazon商品ページを新しいタブで開く: B0H9WBW2YF")` |

### 3.2 作者検索

PoC開始前に実際の`authors.json`から対象を選び、期待結果を次の形式で固定する。作者名だけを固定せず、検索結果に現れるべきASINまたはISBNまで記録する。

```text
作者A:
  期待Kindle ASIN:
  期待紙ISBN:
  検索URL:
  期待する商品種別:

作者B:
  期待未来発売Kindle ASIN:
  発売日:
  検索URL:

作者C:
  期待既刊Kindle ASIN:
  期待紙ISBN:
  検索URL:
```

開始時点の商品ページと検索結果を保存し、期待値を確定した日時も記録する。検索HTTPの成功と、目的商品の検出成功は別の指標として扱う。

## 4. 検証段階

### 4.1 ローカル生HTTP

各固定ASINと作者検索へ、本番候補と同じヘッダー、timeout、redirect制限でリクエストする。

次を記録する。

- HTTP status
- 最終URL
- Content-Type
- レスポンスサイズ
- SHA-256
- CAPTCHA、年齢確認、bot判定、商品不存在を示す要素
- ASIN、タイトル、価格、発売日、著者、Kindleスウォッチ
- 欠落した必須selector

ローカルで正常取得できない対象は、原因を説明できるまでLambda検証へ進めない。

### 4.2 Lambda単発検証

ローカル検証と同じHTTP clientおよびparserをPoC専用Lambdaから実行し、ASINまたは検索条件ごとに1リクエストだけ行う。

CloudWatch Logsには構造化した要約だけを出す。HTML本文はログへ出さず、PoC専用S3へgzip圧縮して一時保存する。S3には短期間のLifecycleを設定し、PoC終了後に削除する。

保存する診断情報は次とする。

```text
実行日時
実行環境
ASINまたは検索条件
request種別
HTTP status
最終URL
Content-Type
response bytes
SHA-256
response分類
取得フィールド
欠落selector
生HTMLのS3 key
```

### 4.3 ローカルとLambdaの比較

同一入力について次を比較する。

- HTMLの種類とSHA-256
- CAPTCHA、年齢確認、bot判定等のmarker
- 必須要素と欠落selector
- Lambda環境でのみ発生する差異
- cold invocationとwarm invocationの差異

初回リリースで失敗した`[**B0FP14RH57**](https://www.amazon.co.jp/dp/B0FP14RH57 "Amazon商品ページを新しいタブで開く: B0FP14RH57")`と`[**B0FGZSWQXT**](https://www.amazon.co.jp/dp/B0FGZSWQXT "Amazon商品ページを新しいタブで開く: B0FGZSWQXT")`を最優先で確認する。

### 4.4 継続運転試験

単発成功だけでは合格としない。単発検証を通過した後、48～72時間、予定していた実運用相当の時間帯とリクエスト密度でPoCを動かす。

- Sale相当: 2時間ごと
- New Release相当: 1日4周
- Paper-to-Kindle相当: 1日4周
- 通常商品、検索、商品詳細を混在させる
- Amazonアクセスは直列化する
- 非正常応答のHTMLを必ず一時保存する
- transient errorは最大3回まで再試行し、各試行を別に記録する

ポイント、クーポン、価格は変動するため、絶対値や存在を合格条件にしない。取得できる場合に正しく解析でき、存在しない場合を正常に扱えることを確認する。

## 5. 応答分類

PoC期間中に観測された非正常応答を、判断時点ですべて説明可能なカテゴリへ分類する。

未知応答は成功、商品不存在、対象外として処理しない。HTMLを保存し、`retryable unknown`として調査対象にする。未知HTMLが将来一切出ないことは要求しないが、未知HTMLを正常処理へ流さないことと、事後調査できることを要求する。

少なくとも次を区別する。

- 正常な商品ページ
- 正常な検索結果ページ
- 年齢確認ページ
- CAPTCHAまたはbot判定ページ
- アクセス拒否
- 商品不存在
- redirect先不正
- 必須構造欠落
- 未知応答

## 6. 合格基準

判定基準はPoC開始前に固定する。割合と同時に実数の分子・分母を記録する。

| 指標 | 合格基準 |
|---|---:|
| 正常商品ページの初回取得成功率 | 98%以上 |
| 正常商品ページの最大3回以内の最終取得成功率 | 100% |
| 検索ページの解析成功率 | 98%以上 |
| 固定した期待商品の検索1回当たり検出率 | 90%以上 |
| 各期待商品が24時間内に1回以上検出される割合 | 100% |
| transient errorのretry後回復率 | 100% |
| 正常対象でのCAPTCHA・bot判定率 | 1%以下 |
| 同一正常対象での連続CAPTCHA・bot判定 | 0件 |
| 年齢確認対象のterminal分類率 | 100% |
| 未知HTMLを正常・商品不存在と誤認した件数 | 0件 |
| PoC終了時点で説明不能な観測応答 | 0件 |

正常対象が1件でも最大3回すべて失敗した場合は、全体割合にかかわらずNo-Goとする。

## 7. 撤退条件

次のいずれかに該当した場合、Lambdaと生HTTPの組み合わせを不採用とする。

- 正常商品でもLambdaから継続的にCAPTCHAまたはbot判定ページが返る
- ローカルでは成功し、Lambdaだけ失敗する状態が反復する
- Cookie、ログイン、アクセス制限回避を行わなければ商品ページを取得できない
- 実運用相当の頻度で恒常的なアクセス制限が発生する
- 正常対象が最大3回の試行ですべて失敗する
- Amazon検索がHTTP 200でも期待商品を24時間内に一度も返さない

Lambdaと生HTTPがNo-Goになった場合だけ、同じ固定対象、期待結果、数値基準でヘッドレスブラウザPoCを実施する。ヘッドレスブラウザでも合格しない場合は新システムの開発を中止し、UserScript運用を維持する。

## 8. 最終結果

全試行を次の形式で一覧化する。

```text
日時
実行環境
対象
request種別
HTTP status
response分類
初回成功 / 再試行成功 / 恒久失敗
期待商品検出結果
response bytes
SHA-256
保存HTML
```

次を最終報告へ含める。

- 指標ごとの分子、分母、割合
- 対象別の成功・失敗履歴
- ローカルとLambdaの差異
- 観測した非正常応答の分類根拠
- 保存HTMLの参照先と削除期限
- 合格条件と実測値の比較
- 生HTTP方式のGo/No-Go判定
- No-Goの場合にヘッドレスブラウザPoCへ進むか、UserScriptを維持するかの選択肢

この報告をユーザーが確認し、方式採用を明示するまで、本番コード、業務ロジック、S3管理、SQS、Scheduler、通知、Gist、SAM本番Stack、移行処理へ着手しない。
