#!/usr/bin/env bash
# scripts/deploy.sh — AWS SAM 標準フロー（sam build / sam deploy）の薄い wrapper。
# 自前の package・change set 管理・state file・CREATE/UPDATE 自己判定は持たず、
# sam deploy 自身の change set 確認プロンプト（infra/samconfig.toml の
# confirm_changeset）で人間が変更を確認してから同じ sam deploy が適用する（AGENTS.md §13）。
# 明示的な --profile と --region を必須とし、AWS default profile/region へ暗黙依存しない。
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
TEMPLATE="$ROOT_DIR/infra/template.yaml"
SAMCONFIG="$ROOT_DIR/infra/samconfig.toml"
BUILD_DIR="$ROOT_DIR/.aws-sam"
# sam build --build-dir の出力は <BUILD_DIR>/template.yaml になる（SAM CLI 実機確認済み）。
# sam deploy は build しないため、source template (infra/template.yaml) ではなく
# この build 済み template（build 済み bootstrap 参照を含む）を sam deploy へ渡す。
BUILD_TEMPLATE="$BUILD_DIR/template.yaml"

STACK_NAME="kindle-automation"
PROFILE=""
REGION=""
STAGE=""
S3_BUCKET=""
PARAMS=()

usage() {
    cat <<EOF
Usage: $0 --profile P --region R --stage STAGE [options]
Stages:
  build       sam build で Lambda バイナリ等を build する（deploy はしない）
  deploy      sam deploy を実行する。samconfig.toml の confirm_changeset により
              change set が表示され、人間の確認後に同じ sam deploy が適用する
  all         sam build 後に sam deploy を実行する（deploy の確認プロンプトあり）
Options:
  --stack-name NAME        CloudFormation stack 名（default: ${STACK_NAME}）
  --s3-bucket BUCKET       デプロイ成果物用 S3 bucket。未指定時は --resolve-s3
  --parameter-override K=V SAM Parameter override（複数指定可）
EOF
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --profile) PROFILE="$2"; shift 2 ;;
        --region) REGION="$2"; shift 2 ;;
        --stage) STAGE="$2"; shift 2 ;;
        --stack-name) STACK_NAME="$2"; shift 2 ;;
        --s3-bucket) S3_BUCKET="$2"; shift 2 ;;
        --parameter-override) PARAMS+=("$2"); shift 2 ;;
        -h | --help) usage; exit 0 ;;
        *) echo "unknown argument: $1" >&2; usage >&2; exit 2 ;;
    esac
done

# profile と region の明示を強制する（AGENTS.md §13）。
if [[ -z "$PROFILE" || -z "$REGION" || -z "$STAGE" ]]; then
    echo "--profile, --region, --stage は必須です" >&2
    exit 2
fi

# デプロイ成果物の S3 bucket。明示指定がなければ SAM 管理 bucket を使う。
s3_args=()
if [[ -n "$S3_BUCKET" ]]; then
    s3_args=(--s3-bucket "$S3_BUCKET")
else
    s3_args=(--resolve-s3)
fi

do_build() {
    echo "==> sam build"
    # --build-dir で build 済み template の出力先を固定し、do_deploy が参照する位置を確定させる。
    sam build --template-file "$TEMPLATE" --build-dir "$BUILD_DIR" --config-file "$SAMCONFIG" \
        --profile "$PROFILE" --region "$REGION"
}

do_deploy() {
    # source template でなく build 済み template を sam deploy へ渡す。sam deploy は build せず、
    # source template を渡すと build 済み bootstrap を含まない Lambda になるため不可。
    if [[ ! -f "$BUILD_TEMPLATE" ]]; then
        echo "build 済み template が見つかりません: $BUILD_TEMPLATE" >&2
        echo "先に --stage build を実行してください。" >&2
        return 1
    fi
    echo "==> sam deploy"
    # samconfig.toml が stack_name/capabilities/confirm_changeset を供給する。
    # --parameter-overrides は後続の引数をすべて吸収するため、必ず最尾に置く。
    local deploy_args=(sam deploy
        --template-file "$BUILD_TEMPLATE"
        --config-file "$SAMCONFIG"
        --stack-name "$STACK_NAME"
        --profile "$PROFILE"
        --region "$REGION")
    deploy_args+=("${s3_args[@]}")
    if ((${#PARAMS[@]})); then
        deploy_args+=(--parameter-overrides "${PARAMS[@]}")
    fi
    "${deploy_args[@]}"
}

case "$STAGE" in
    build) do_build ;;
    deploy) do_deploy ;;
    all)
        do_build
        do_deploy
        ;;
    *) echo "unknown stage: $STAGE" >&2; usage >&2; exit 2 ;;
esac
