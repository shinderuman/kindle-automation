#!/usr/bin/env bash
# scripts/deploy_test.sh — deploy.sh の回帰テスト。
# sam / aws / date を stub 化し、AWS・S3・実デプロイへ一切アクセスせずに
# package 対象 template・changeset 作成引数・同一 change set の execute・
# all の非 deploy・途中失敗時停止・状態不正時の非 execute を検証する。
# 実行: bash scripts/deploy_test.sh
set -uo pipefail

SCRIPT="$(cd "$(dirname "$0")" && pwd)/deploy.sh"
ORIG_PATH="$PATH"
PASS=0
FAIL_COUNT=0
ERRORS=0

# stub が呼び出しを記録する log と、fake aws の内部状態 dir。各ケースで setup_root が再設定する。
CALL_LOG=""
AWS_FAKE_DIR=""
ROOT=""
RUN_RC=0

fail() {
    echo "  FAIL: $*" >&2
    ERRORS=$((ERRORS + 1))
}

assert_contains() {
    # <needle> <file> <description>
    if grep -qF -- "$1" "$2"; then
        echo "  ok: $3"
    else
        fail "expected log to contain [$1] — $3"
    fi
}

assert_not_contains() {
    if grep -qF -- "$1" "$2"; then
        fail "expected log to NOT contain [$1] — $3"
    else
        echo "  ok: $3"
    fi
}

assert_var_contains() {
    # <needle> <var-value> <description>
    if [[ "$2" == *"$1"* ]]; then
        echo "  ok: $3"
    else
        fail "expected value to contain [$1] — $3"
    fi
}

assert_var_not_contains() {
    if [[ "$2" == *"$1"* ]]; then
        fail "expected value to NOT contain [$1] — $3"
    else
        echo "  ok: $3"
    fi
}

assert_rc_zero() {
    if [[ "$RUN_RC" -eq 0 ]]; then
        echo "  ok: $1"
    else
        fail "expected exit 0 but got $RUN_RC — $1"
    fi
}

assert_rc_nonzero() {
    if [[ "$RUN_RC" -ne 0 ]]; then
        echo "  ok: $1"
    else
        fail "expected non-zero exit but got 0 — $1"
    fi
}

assert_count_eq() {
    # <needle> <expected> <file> <description>
    local n
    n=$(grep -cF -- "$1" "$3" 2>/dev/null || true)
    if [[ "$n" -eq "$2" ]]; then
        echo "  ok: $4 ($n)"
    else
        fail "expected $2 occurrence(s) of [$1] but got $n — $4"
    fi
}

setup_root() {
    ROOT="$(mktemp -d)"
    mkdir -p "$ROOT/scripts" "$ROOT/infra" "$ROOT/bin" "$ROOT/aws-fake"
    cp "$SCRIPT" "$ROOT/scripts/deploy.sh"
    chmod +x "$ROOT/scripts/deploy.sh"
    echo "AWSTemplateFormatVersion: '2010-09-09'" > "$ROOT/infra/template.yaml"
    CALL_LOG="$ROOT/call.log"
    AWS_FAKE_DIR="$ROOT/aws-fake"
    : > "$CALL_LOG"

    cat > "$ROOT/bin/sam" <<'SAM_EOF'
#!/usr/bin/env bash
echo "sam :: $*" >> "$CALL_LOG"
if [[ "${FAKE_SAM_BUILD_FAIL:-}" == "1" && "$1" == "build" ]]; then
    echo "fake sam build failure" >&2
    exit 1
fi
if [[ "${FAKE_SAM_PACKAGE_FAIL:-}" == "1" && "$1" == "package" ]]; then
    echo "fake sam package failure" >&2
    exit 1
fi
case "$1" in
    build)
        build_dir=""
        shift
        while [[ $# -gt 0 ]]; do
            case "$1" in
                --build-dir) build_dir="$2"; shift 2 ;;
                *) shift ;;
            esac
        done
        mkdir -p "$build_dir/build"
        echo "Resources: built" > "$build_dir/build/template.yaml"
        ;;
    package)
        out=""
        shift
        while [[ $# -gt 0 ]]; do
            case "$1" in
                --output-template-file) out="$2"; shift 2 ;;
                *) shift ;;
            esac
        done
        mkdir -p "$(dirname "$out")"
        echo "packaged template" > "$out"
        ;;
    *)
        echo "unexpected sam subcommand: $1" >&2
        exit 1
        ;;
esac
exit 0
SAM_EOF

    cat > "$ROOT/bin/aws" <<'AWS_EOF'
#!/usr/bin/env bash
echo "aws :: $*" >> "$CALL_LOG"
sub="${2:-}"
shift 2 2>/dev/null || true
case "$sub" in
    describe-stacks)
        stack=""
        while [[ $# -gt 0 ]]; do
            case "$1" in
                --stack-name) stack="$2"; shift 2 ;;
                *) shift ;;
            esac
        done
        if [[ -f "$AWS_FAKE_DIR/stack-exists-$stack" ]]; then
            exit 0
        fi
        echo "Stack does not exist" >&2
        exit 1
        ;;
    create-change-set)
        csname=""
        while [[ $# -gt 0 ]]; do
            case "$1" in
                --change-set-name) csname="$2"; shift 2 ;;
                *) shift ;;
            esac
        done
        id="arn:aws:cloudformation:us-east-1:123456789012:changeSet/${csname}/abc123"
        echo "CREATE_COMPLETE" > "$AWS_FAKE_DIR/cs-${csname}"
        echo "$id" > "$AWS_FAKE_DIR/id-${csname}"
        echo "$id"
        exit 0
        ;;
    wait)
        if [[ "${FAKE_AWS_WAIT_FAIL:-}" == "1" ]]; then
            exit 1
        fi
        exit 0
        ;;
    describe-change-set)
        status="CREATE_COMPLETE"
        if [[ -f "$AWS_FAKE_DIR/status-override" ]]; then
            status="$(cat "$AWS_FAKE_DIR/status-override")"
        fi
        echo "$status"
        exit 0
        ;;
    execute-change-set)
        if [[ "${FAKE_AWS_EXECUTE_FAIL:-}" == "1" ]]; then
            exit 1
        fi
        exit 0
        ;;
    *)
        echo "unexpected aws subcommand: $sub" >&2
        exit 1
        ;;
esac
AWS_EOF

    cat > "$ROOT/bin/date" <<'DATE_EOF'
#!/usr/bin/env bash
echo "20260810120000"
DATE_EOF

    chmod +x "$ROOT/bin/"*
}

run_deploy() {
    CALL_LOG="$CALL_LOG" AWS_FAKE_DIR="$AWS_FAKE_DIR" \
        PATH="$ROOT/bin:$ORIG_PATH" \
        "$ROOT/scripts/deploy.sh" "$@" >/dev/null 2>&1
    RUN_RC=$?
}

read_state() {
    # <key> <state-file>
    grep -F "$1=" "$2" | head -1 | cut -d= -f2-
}

report() {
    if [[ "$ERRORS" -eq 0 ]]; then
        echo "PASS $1"
        PASS=$((PASS + 1))
    else
        echo "FAIL $1 ($ERRORS error(s))"
        FAIL_COUNT=$((FAIL_COUNT + 1))
    fi
}

# T9: CLI 互換性。--help は成功、必須引数欠落・未知 stage は exit 2。
t9_cli_compatibility() {
    ERRORS=0
    setup_root
    run_deploy --help
    assert_rc_zero "--help は成功する"
    run_deploy --stage build
    assert_rc_nonzero "必須引数 (--profile/--region) 欠落で非零終了する"
    # exit 2 を厳密に検証
    CALL_LOG="$CALL_LOG" AWS_FAKE_DIR="$AWS_FAKE_DIR" \
        PATH="$ROOT/bin:$ORIG_PATH" \
        "$ROOT/scripts/deploy.sh" --stage build >/dev/null 2>&1
    if [[ $? -eq 2 ]]; then echo "  ok: 必須引数欠落は exit 2"; else fail "必須引数欠落の exit code が 2 でない"; fi
    run_deploy --profile p --region us-east-1 --stage bogus
    if [[ "$RUN_RC" -eq 2 ]]; then echo "  ok: 未知 stage は exit 2"; else fail "未知 stage の exit code が 2 でない ($RUN_RC)"; fi
    report "t9_cli_compatibility"
}

# T1: package は build 済み template を --template-file で指定し、元 template を位置引数に渡さない。
t1_package_uses_built_template() {
    ERRORS=0
    setup_root
    run_deploy --profile p --region us-east-1 --stage build
    assert_rc_zero "build 成功"
    run_deploy --profile p --region us-east-1 --stage package
    assert_rc_zero "package 成功"
    assert_contains ".aws-sam/build/template.yaml" "$CALL_LOG" "package が build 済み template を参照する"
    assert_contains "--template-file" "$CALL_LOG" "package が --template-file で template を渡す"
    pkg_line="$(grep '^sam :: package' "$CALL_LOG" || true)"
    assert_var_not_contains "infra/template.yaml" "$pkg_line" "package 行に元 template が位置引数として無い"
    report "t1_package_uses_built_template"
}

# T2: changeset は package 済み template で change set を作成し、state を保存する。
t2_changeset_creates_change_set_and_state() {
    ERRORS=0
    setup_root
    run_deploy --profile p --region us-east-1 --stage build
    run_deploy --profile p --region us-east-1 --stage package
    run_deploy --profile p --region us-east-1 --stage changeset
    assert_rc_zero "changeset 成功"
    assert_contains "create-change-set" "$CALL_LOG" "change set を作成する"
    assert_contains ".aws-sam/packaged.yaml" "$CALL_LOG" "package 済み template で change set を作成する"
    assert_contains "--change-set-name cs-kindle-automation-" "$CALL_LOG" "決定的な change set 名を指定する"
    assert_contains "--stack-name kindle-automation" "$CALL_LOG" "stack 名を明示する"
    state="$ROOT/.aws-sam/changeset.state"
    if [[ -f "$state" ]]; then
        echo "  ok: state file が存在する"
    else
        fail "state file が無い: $state"
    fi
    csid="$(read_state CHANGESET_ID "$state")"
    assert_var_contains "arn:aws:cloudformation" "$csid" "state に change set id が保存されている"
    report "t2_changeset_creates_change_set_and_state"
}

# T3: deploy は changeset 段階で確認した同一 change set のみ execute し、sam deploy しない。
t3_deploy_executes_same_changeset() {
    ERRORS=0
    setup_root
    run_deploy --profile p --region us-east-1 --stage build
    run_deploy --profile p --region us-east-1 --stage package
    run_deploy --profile p --region us-east-1 --stage changeset
    run_deploy --profile p --region us-east-1 --stage deploy
    assert_rc_zero "deploy 成功"
    state="$ROOT/.aws-sam/changeset.state"
    csid="$(read_state CHANGESET_ID "$state")"
    assert_var_contains "arn:aws:cloudformation" "$csid" "state から change set id を取得できる"
    assert_contains "$csid" "$CALL_LOG" "execute が state の同一 change set id を使う"
    assert_contains "execute-change-set" "$CALL_LOG" "deploy は execute-change-set を呼ぶ"
    assert_not_contains "sam deploy" "$CALL_LOG" "deploy は sam deploy で別 change set を作らない"
    assert_count_eq "create-change-set" 1 "$CALL_LOG" "change set 作成は1回だけ（deploy では新規作成しない）"
    report "t3_deploy_executes_same_changeset"
}

# T4: all は build → package → changeset までで停止し、execute しない。
t4_all_does_not_deploy() {
    ERRORS=0
    setup_root
    run_deploy --profile p --region us-east-1 --stage all
    assert_rc_zero "all 成功（deploy 前提で停止）"
    assert_contains "sam :: build" "$CALL_LOG" "all は build する"
    assert_contains "sam :: package" "$CALL_LOG" "all は package する"
    assert_contains "create-change-set" "$CALL_LOG" "all は change set を作成する"
    assert_not_contains "execute-change-set" "$CALL_LOG" "all は execute しない"
    report "t4_all_does_not_deploy"
}

# T5: 途中失敗時は後続段階へ進まない（set -euo pipefail による停止）。
t5_stops_on_mid_failure() {
    ERRORS=0
    setup_root
    CALL_LOG="$CALL_LOG" AWS_FAKE_DIR="$AWS_FAKE_DIR" \
        PATH="$ROOT/bin:$ORIG_PATH" FAKE_SAM_BUILD_FAIL=1 \
        "$ROOT/scripts/deploy.sh" --profile p --region us-east-1 --stage all >/dev/null 2>&1
    RUN_RC=$?
    assert_rc_nonzero "build 失敗で all 全体が非零終了する"
    assert_contains "sam :: build" "$CALL_LOG" "build は実行される"
    assert_not_contains "sam :: package" "$CALL_LOG" "build 失敗後は package へ進まない"
    assert_not_contains "create-change-set" "$CALL_LOG" "build 失敗後は change set 作成へ進まない"
    assert_not_contains "execute-change-set" "$CALL_LOG" "build 失敗後は execute しない"
    report "t5_stops_on_mid_failure"
}

# T6: change set が無い（state 不在）場合は execute しない。
t6_deploy_without_changeset_refuses() {
    ERRORS=0
    setup_root
    run_deploy --profile p --region us-east-1 --stage build
    run_deploy --profile p --region us-east-1 --stage package
    run_deploy --profile p --region us-east-1 --stage deploy
    assert_rc_nonzero "state 不在で deploy は非零終了する"
    assert_not_contains "execute-change-set" "$CALL_LOG" "change set 無しでは execute しない"
    report "t6_deploy_without_changeset_refuses"
}

# T7: change set の状態が不正（FAILED 等）の場合は execute しない。
t7_deploy_invalid_status_refuses() {
    ERRORS=0
    setup_root
    run_deploy --profile p --region us-east-1 --stage build
    run_deploy --profile p --region us-east-1 --stage package
    run_deploy --profile p --region us-east-1 --stage changeset
    echo "FAILED" > "$AWS_FAKE_DIR/status-override"
    run_deploy --profile p --region us-east-1 --stage deploy
    assert_rc_nonzero "状態不正で deploy は非零終了する"
    assert_not_contains "execute-change-set" "$CALL_LOG" "状態不正時は execute しない"
    report "t7_deploy_invalid_status_refuses"
}

# T8: changeset 作成失敗（wait 失敗）時は state を破棄し、deploy 可能な change set を残さない。
t8_changeset_failure_clears_state() {
    ERRORS=0
    setup_root
    run_deploy --profile p --region us-east-1 --stage build
    run_deploy --profile p --region us-east-1 --stage package
    CALL_LOG="$CALL_LOG" AWS_FAKE_DIR="$AWS_FAKE_DIR" \
        PATH="$ROOT/bin:$ORIG_PATH" FAKE_AWS_WAIT_FAIL=1 \
        "$ROOT/scripts/deploy.sh" --profile p --region us-east-1 --stage changeset >/dev/null 2>&1
    RUN_RC=$?
    assert_rc_nonzero "wait 失敗で changeset は非零終了する"
    state="$ROOT/.aws-sam/changeset.state"
    if [[ -f "$state" ]]; then
        fail "changeset 失敗時に state が残っている: $state"
    else
        echo "  ok: changeset 失敗時に state を破棄する"
    fi
    assert_not_contains "execute-change-set" "$CALL_LOG" "changeset 失敗時は execute しない"
    report "t8_changeset_failure_clears_state"
}

t1_package_uses_built_template
t2_changeset_creates_change_set_and_state
t3_deploy_executes_same_changeset
t4_all_does_not_deploy
t5_stops_on_mid_failure
t6_deploy_without_changeset_refuses
t7_deploy_invalid_status_refuses
t8_changeset_failure_clears_state
t9_cli_compatibility

echo "-----------------------------------------"
echo "deploy_test: PASS=$PASS FAIL=$FAIL_COUNT"
[[ "$FAIL_COUNT" -eq 0 ]]
