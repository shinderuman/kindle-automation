.PHONY: test vet staticcheck fmt check build-ScheduleChecksFunction build-CheckWorkerFunction

# 単体テスト。自動テストから実AWS・Amazon・Slack・Mastodon・GitHubへはアクセスしない。
test:
	go test ./...

vet:
	go vet ./...

staticcheck:
	staticcheck ./...

# gofmt未適用ファイルが1件でもあればパスを表示して非zero終了する。
# 整形済み、またはGoファイル0件なら成功。gofmt自身のエラー（構文エラー等）も失敗にする。
fmt:
	@files=$$(rg --files -g '*.go'); \
	if [ -z "$$files" ]; then \
		exit 0; \
	fi; \
	unformatted=$$(gofmt -l $$files); \
	if [ $$? -ne 0 ]; then \
		echo "gofmtがエラーを検出した" 1>&2; \
		exit 1; \
	fi; \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt未適用ファイル:" 1>&2; \
		printf '%s\n' "$$unformatted" 1>&2; \
		exit 1; \
	fi

check: fmt test vet staticcheck

# ARTIFACTS_DIR は SAM の BuildMethod: makefile が渡す出力ディレクトリ。
# 未設定時は .build を既定値とし、SAM 契約を壊さず手動実行の事故を防ぐ。
ARTIFACTS_DIR ?= .build

# SAM build (BuildMethod: makefile) が各 cmd を個別にビルドし、bootstrap を生成する。
# GOOS=linux GOARCH=amd64 CGO_ENABLED=0 で静的リンクした provided.al2023 配布バイナリを作る（AGENTS.md 12）。
build-ScheduleChecksFunction:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="-s -w" -o "$(ARTIFACTS_DIR)/bootstrap" ./cmd/schedule-checks

build-CheckWorkerFunction:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="-s -w" -o "$(ARTIFACTS_DIR)/bootstrap" ./cmd/check-worker
