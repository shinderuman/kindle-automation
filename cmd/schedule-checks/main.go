// Package main は schedule-checks Lambda のエントリポイント。実装は internal/lambda/schedulechecks へ分離。
package main

import "github.com/shinderuman/kindle-automation/internal/lambda/schedulechecks"

func main() {
	schedulechecks.Start()
}
