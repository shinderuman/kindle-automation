// Package main は schedule-checks Lambda の薄いエントリポイント。
// 依存組み立て・イベント decode・振り分けは internal/lambda/schedulechecks へ分離している。
package main

import "github.com/shinderuman/kindle-automation/internal/lambda/schedulechecks"

func main() {
	schedulechecks.Start()
}
