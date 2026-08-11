#!/usr/bin/env bash
# scripts/deploy_test.sh — deploy.sh の引数構成回帰テスト。
# sam を stub 化し、AWS・S3・実デプロイへ一切アクセスせずに、deploy.sh が
# sam build / sam deploy を想定どおりの引数で呼ぶことと、必須引数・stage 分岐・
# 途中失敗停止を検証する。SAM 標準 CLI 呼出しに対する最小の argument test とし、
# 自前の change set/state file framework は再構築しない。
# 実行: bash scripts/deploy_test.sh
set -uo pipefail

SCRIPT="$(cd "$(dirname "$0")" && pwd)/deploy.sh"
ORIG_PATH="$PATH"
PASS=0
FAIL_COUNT=0
ERRORS=0

CALL_LOG=""
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

setup_root() {
    ROOT="$(mktemp -d)"
    mkdir -p "$ROOT/scripts" "$ROOT/infra" "$ROOT/bin"
    cp "$SCRIPT" "$ROOT/scripts/deploy.sh"
    chmod +x "$ROOT/scripts/deploy.sh"
    echo "AWSTemplateFormatVersion: '2010-09-09'" > "$ROOT/infra/template.yaml"
    CALL_LOG="$ROOT/call.log"
    : > "$CALL_LOG"

    cat > "$ROOT/bin/sam" <<'SAM_EOF'
#!/usr/bin/env bash
echo "sam :: $*" >> "$CALL_LOG"
sub="$1"
shift
case "$sub" in
    build)
        if [[ "${FAKE_SAM_BUILD_FAIL:-}" == "1" ]]; then
            echo "fake sam build failure" >&2
            exit 1
        fi
        # sam build --build-dir X は X/template.yaml を生成する（実機準拠）。
        build_dir=""
        while [[ $# -gt 0 ]]; do
            case "$1" in
                --build-dir) build_dir="$2"; shift 2 ;;
                *) shift ;;
            esac
        done
        if [[ -n "$build_dir" ]]; then
            mkdir -p "$build_dir"
            echo "Resources: built" > "$build_dir/template.yaml"
        fi
        ;;
    deploy)
        if [[ "${FAKE_SAM_DEPLOY_FAIL:-}" == "1" ]]; then
            echo "fake sam deploy failure" >&2
            exit 1
        fi
        ;;
    *)
        echo "unexpected sam subcommand: $sub" >&2
        exit 1
        ;;
esac
exit 0
SAM_EOF
    chmod +x "$ROOT/bin/"*
}

run_deploy() {
    CALL_LOG="$CALL_LOG" \
        PATH="$ROOT/bin:$ORIG_PATH" \
        "$ROOT/scripts/deploy.sh" "$@" >/dev/null 2>&1
    RUN_RC=$?
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

# T1: CLI 互換性。--help は成功、必須引数欠落・未知 stage は exit 2。
t1_cli_compatibility() {
    ERRORS=0
    setup_root
    run_deploy --help
    assert_rc_zero "--help は成功する"
    run_deploy --profile p --region us-east-1
    assert_rc_nonzero "必須 --stage 欠落で非零終了する"
    run_deploy --profile p --region us-east-1 --stage bogus
    if [[ "$RUN_RC" -eq 2 ]]; then echo "  ok: 未知 stage は exit 2"; else fail "未知 stage の exit code が 2 でない ($RUN_RC)"; fi
    run_deploy --stage build
    if [[ "$RUN_RC" -eq 2 ]]; then echo "  ok: --profile/--region 欠落は exit 2"; else fail "--profile/--region 欠落の exit code が 2 でない ($RUN_RC)"; fi
    assert_not_contains "sam ::" "$CALL_LOG" "必須引数欠落時は sam を呼ばない"
    report "t1_cli_compatibility"
}

# T2: build は sam build を決まった引数で呼び、build 済み template を出力する。
t2_build_invokes_sam_build() {
    ERRORS=0
    setup_root
    run_deploy --profile p --region us-east-1 --stage build
    assert_rc_zero "build 成功"
    assert_contains "sam :: build" "$CALL_LOG" "sam build を呼ぶ"
    assert_contains "infra/template.yaml" "$CALL_LOG" "build は source の infra/template.yaml を参照する"
    assert_contains "--build-dir" "$CALL_LOG" "build は --build-dir で出力先を固定する"
    assert_contains "infra/samconfig.toml" "$CALL_LOG" "build は samconfig.toml を参照する"
    assert_contains "--profile p" "$CALL_LOG" "build は --profile を渡す"
    assert_contains "--region us-east-1" "$CALL_LOG" "build は --region を渡す"
    assert_not_contains "sam :: deploy" "$CALL_LOG" "build は deploy しない"
    if [[ -f "$ROOT/.aws-sam/template.yaml" ]]; then
        echo "  ok: build 済み template が .aws-sam/template.yaml へ生成される"
    else
        fail "build 済み template が無い: $ROOT/.aws-sam/template.yaml"
    fi
    report "t2_build_invokes_sam_build"
}

# T3: deploy は build 済み template (.aws-sam/template.yaml) を sam deploy へ渡し、
#     source template (infra/template.yaml) は渡さない。bucket 未指定時は --resolve-s3。
t3_deploy_uses_built_template() {
    ERRORS=0
    setup_root
    run_deploy --profile p --region us-east-1 --stage build
    assert_rc_zero "build 成功"
    run_deploy --profile p --region us-east-1 --stage deploy
    assert_rc_zero "deploy 成功"
    assert_contains "sam :: deploy" "$CALL_LOG" "sam deploy を呼ぶ"
    assert_contains ".aws-sam/template.yaml" "$CALL_LOG" "deploy は build 済み template を参照する"
    # deploy 行だけを抜き出し、source template を渡していないか検証する。
    if grep '^sam :: deploy' "$CALL_LOG" | grep -qF 'infra/template.yaml'; then
        fail "deploy が source template (infra/template.yaml) を渡している"
    else
        echo "  ok: deploy は source template を渡さない"
    fi
    assert_contains "infra/samconfig.toml" "$CALL_LOG" "deploy は samconfig.toml を参照する"
    assert_contains "--stack-name kindle-automation" "$CALL_LOG" "deploy は stack 名を明示する"
    assert_contains "--profile p" "$CALL_LOG" "deploy は --profile を渡す"
    assert_contains "--region us-east-1" "$CALL_LOG" "deploy は --region を渡す"
    assert_contains "--resolve-s3" "$CALL_LOG" "bucket 未指定時は --resolve-s3"
    assert_not_contains "--s3-bucket" "$CALL_LOG" "bucket 未指定時は --s3-bucket を付けない"
    report "t3_deploy_uses_built_template"
}

# T4: --s3-bucket 指定時は --s3-bucket を使い --resolve-s3 を付けない。
t4_deploy_explicit_s3_bucket() {
    ERRORS=0
    setup_root
    run_deploy --profile p --region us-east-1 --stage build
    run_deploy --profile p --region us-east-1 --stage deploy --s3-bucket my-artifacts
    assert_rc_zero "deploy 成功"
    assert_contains "--s3-bucket my-artifacts" "$CALL_LOG" "--s3-bucket を渡す"
    assert_not_contains "--resolve-s3" "$CALL_LOG" "--s3-bucket 指定時は --resolve-s3 を付けない"
    report "t4_deploy_explicit_s3_bucket"
}

# T5: --parameter-override は sam deploy の --parameter-overrides へ渡す。
t5_deploy_parameter_overrides() {
    ERRORS=0
    setup_root
    run_deploy --profile p --region us-east-1 --stage build
    run_deploy --profile p --region us-east-1 --stage deploy \
        --parameter-override SchedulersEnabled=true --parameter-override LogLevel=DEBUG
    assert_rc_zero "deploy 成功"
    assert_contains "--parameter-overrides" "$CALL_LOG" "--parameter-overrides を渡す"
    assert_contains "SchedulersEnabled=true" "$CALL_LOG" "1件目の override を渡す"
    assert_contains "LogLevel=DEBUG" "$CALL_LOG" "2件目の override を渡す"
    report "t5_deploy_parameter_overrides"
}

# T6: all は sam build 後に sam deploy を呼ぶ。
t6_all_build_then_deploy() {
    ERRORS=0
    setup_root
    run_deploy --profile p --region us-east-1 --stage all
    assert_rc_zero "all 成功"
    assert_contains "sam :: build" "$CALL_LOG" "all は sam build を呼ぶ"
    assert_contains "sam :: deploy" "$CALL_LOG" "all は sam deploy を呼ぶ"
    # build 行が deploy 行より先に現れること。
    build_line=$(grep -nF "sam :: build" "$CALL_LOG" | head -1 | cut -d: -f1)
    deploy_line=$(grep -nF "sam :: deploy" "$CALL_LOG" | head -1 | cut -d: -f1)
    if [[ -n "$build_line" && -n "$deploy_line" && "$build_line" -lt "$deploy_line" ]]; then
        echo "  ok: build が deploy より先に実行される"
    else
        fail "build が deploy より先でない (build=$build_line deploy=$deploy_line)"
    fi
    report "t6_all_build_then_deploy"
}

# T7: build 失敗時は all の後続 deploy へ進まない（set -e による停止）。
t7_all_stops_on_build_failure() {
    ERRORS=0
    setup_root
    CALL_LOG="$CALL_LOG" PATH="$ROOT/bin:$ORIG_PATH" FAKE_SAM_BUILD_FAIL=1 \
        "$ROOT/scripts/deploy.sh" --profile p --region us-east-1 --stage all >/dev/null 2>&1
    RUN_RC=$?
    assert_rc_nonzero "build 失敗で all 全体が非零終了する"
    assert_contains "sam :: build" "$CALL_LOG" "build は実行される"
    assert_not_contains "sam :: deploy" "$CALL_LOG" "build 失敗後は deploy へ進まない"
    report "t7_all_stops_on_build_failure"
}

# T8: build 済み template が無い（build 未実行）場合は deploy せず非零終了する。
t8_deploy_without_build_refuses() {
    ERRORS=0
    setup_root
    run_deploy --profile p --region us-east-1 --stage deploy
    assert_rc_nonzero "build 済み template 無しで deploy は非零終了する"
    assert_not_contains "sam :: deploy" "$CALL_LOG" "build 無しでは sam deploy を呼ばない"
    report "t8_deploy_without_build_refuses"
}

t1_cli_compatibility
t2_build_invokes_sam_build
t3_deploy_uses_built_template
t4_deploy_explicit_s3_bucket
t5_deploy_parameter_overrides
t6_all_build_then_deploy
t7_all_stops_on_build_failure
t8_deploy_without_build_refuses

echo "-----------------------------------------"
echo "deploy_test: PASS=$PASS FAIL=$FAIL_COUNT"
[[ "$FAIL_COUNT" -eq 0 ]]
