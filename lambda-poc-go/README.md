# Go 生リクエストLambda PoC

Go標準の`net/http`でAmazon商品ページを取得し、`goquery`で商品情報とクーポン表示を抽出する実行基盤検証用コードです。

入力例：

```json
{"asin":"B0CX8CD1XL"}
```
