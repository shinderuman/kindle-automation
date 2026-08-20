# LambdaブラウザPoC

Lambda上でヘッドレスChromiumを起動し、Amazonの商品ページから商品タイトル・Kindle価格・クーポン表示を取得するPoCです。

## Lambda

```text
Function: codex-kindle-browser-poc
Region: ap-northeast-1
Runtime: nodejs22.x
Architecture: x86_64
Memory: 2048 MB
Timeout: 120 seconds
Role: codex-role
```

API Gatewayは使用していません。AWS CLIからLambdaを直接Invokeします。

## Invoke

```bash
printf '%s' '{"asin":"B0H93PBNZ5"}' > invoke.json
aws lambda invoke \
  --profile codex-user \
  --region ap-northeast-1 \
  --function-name codex-kindle-browser-poc \
  --invocation-type RequestResponse \
  --cli-binary-format raw-in-base64-out \
  --payload fileb://invoke.json \
  invoke-response.json
```

## 確認結果

2026-07-22、既存`unprocessed_asins.json`に含まれるASINで実行した結果、以下を取得できました。

- Lambda実行：成功
- Amazonページ遷移：成功
- 商品タイトル：取得成功
- Kindle価格：取得成功
- CAPTCHA判定：検出なし
- 実行時間：約3.9秒
- 最大使用メモリ：約536MB

取得例：

```json
{
  "title": "捕虜英雄～捨て駒にされた剣奴は敵国で成り上がる～ 3 (ヤングアニマルコミックス)",
  "price": "￥759",
  "coupon": null
}
```

## Chromium

デプロイパッケージのサイズ制限を避けるため、`@sparticuz/chromium-min`を使用し、初回実行時に公開packを取得します。
