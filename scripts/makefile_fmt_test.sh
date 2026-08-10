#!/usr/bin/env bash
# scripts/makefile_fmt_test.sh — Makefile の fmt 品質ゲート回帰テスト。
# gofmt未適用Goファイルが1件でもあれば make fmt が非zero終了し、probe 除去後に
# 再び exit 0 になることを検証する。AWS・Amazon・ネットワークへはアクセスしない。
# make check の fmt 失敗伝播は Make の依存関係 (check: fmt test vet staticcheck)
# で構造保証されるため、ここでは fmt target だけを直接検証する。
# 実行: bash scripts/makefile_fmt_test.sh
set -uo pipefail

REPO="$(cd "$(dirname "$0")/.." && pwd)"
PROBE="$REPO/fmtgate_probe.go"
trap 'rm -f "$PROBE"' EXIT

# (1) 整形済み状態では make fmt は成功する。
if make -C "$REPO" fmt >/dev/null 2>&1; then
    echo "ok: 整形済み状態の make fmt は成功する"
else
    echo "FAIL: 整形済み状態の make fmt が成功しない" >&2
    exit 1
fi

# (2) trap で保護した一時未整形Goファイルで make fmt は非zero終了し、対象パスを表示する。
cat > "$PROBE" <<'EOF'
package fmtgateprobe

func probe(){
x:=1
_ = x
}
EOF
if output="$(make -C "$REPO" fmt 2>&1)"; then
    echo "FAIL: 未整形1件で make fmt が非零終了しない" >&2
    exit 1
fi
if ! printf '%s\n' "$output" | grep -qF "fmtgate_probe.go"; then
    echo "FAIL: 対象パスを表示しない" >&2
    exit 1
fi
echo "ok: 未整形1件で make fmt は非零終了し対象パスを表示する"

# (3) 一時ファイル除去後に clean へ戻る（probe が working tree に残らない）。
rm -f "$PROBE"
if make -C "$REPO" fmt >/dev/null 2>&1; then
    echo "ok: probe 除去後に clean へ戻る"
else
    echo "FAIL: probe 除去後に make fmt が成功しない" >&2
    exit 1
fi

echo "makefile_fmt_test: PASS"
