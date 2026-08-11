// Package main は check-worker Lambda のエントリポイント。実装は internal/lambda/checkworker へ分離。
package main

import "github.com/shinderuman/kindle-automation/internal/lambda/checkworker"

func main() {
	checkworker.Start()
}
