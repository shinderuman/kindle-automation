// Package main は check-worker Lambda の薄いエントリポイント。
// 依存組み立て・イベント decode・振り分け・DTO 変換は internal/lambda/checkworker へ分離している。
package main

import "github.com/shinderuman/kindle-automation/internal/lambda/checkworker"

func main() {
	checkworker.Start()
}
