#!/usr/bin/env bash
# scripts/deploy.sh — build / package / change set 確認 / deploy を分離する（AGENTS.md 13）。
# 明示的な --profile と --region を必須とし、AWS default 値へ暗黙依存しない。
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
TEMPLATE="$ROOT_DIR/infra/template.yaml"
PACKAGED="$ROOT_DIR/.aws-sam/packaged.yaml"

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
  build       sam build で Lambda バイナリを生成する
  package     sam package でデプロイ用 template を生成する
  changeset   変更内容を change set として作成し、適用前に確認する
  deploy      変更を適用する
  all         build → package → changeset → deploy を順に実行する
Options:
  --stack-name NAME        CloudFormation stack 名（default: $STACK_NAME）
  --s3-bucket BUCKET       sam package 用 S3 bucket。未指定時は --resolve-s3
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

# profile と region の明示を強制する（AGENTS.md 13）。
if [[ -z "$PROFILE" || -z "$REGION" || -z "$STAGE" ]]; then
    echo "--profile, --region, --stage は必須です" >&2
    exit 2
fi

mkdir -p "$ROOT_DIR/.aws-sam"

# package 先 S3 bucket の解決方法。明示指定がなければ SAM 管理bucket を使う。
pkg_s3_args=()
if [[ -n "$S3_BUCKET" ]]; then
    pkg_s3_args=(--s3-bucket "$S3_BUCKET")
else
    pkg_s3_args=(--resolve-s3)
fi

# --parameter-overrides は値があるときだけ渡す（空文字を渡さない）。
param_args=()
if ((${#PARAMS[@]})); then
    param_args=(--parameter-overrides "${PARAMS[@]}")
fi

do_build() {
    echo "==> sam build"
    sam build --template-file "$TEMPLATE" --profile "$PROFILE" --region "$REGION"
}

do_package() {
    echo "==> sam package -> $PACKAGED"
    sam package "$TEMPLATE" --output-template-file "$PACKAGED" \
        --profile "$PROFILE" --region "$REGION" "${pkg_s3_args[@]}"
}

do_changeset() {
    echo "==> create change set (no execute): $STACK_NAME"
    aws cloudformation deploy --no-execute-changeset \
        --template-file "$PACKAGED" --stack-name "$STACK_NAME" \
        --capabilities CAPABILITY_IAM "${param_args[@]}" \
        --profile "$PROFILE" --region "$REGION"
    echo "変更内容を確認してください:"
    echo "  aws cloudformation describe-change-set --change-set-name <ChangeSetId> --profile $PROFILE --region $REGION"
    echo "問題なければ --stage deploy で適用してください。"
}

do_deploy() {
    echo "==> sam deploy: $STACK_NAME"
    sam deploy --template-file "$PACKAGED" --stack-name "$STACK_NAME" \
        --capabilities CAPABILITY_IAM --no-confirm-changeset \
        "${param_args[@]}" "${pkg_s3_args[@]}" \
        --profile "$PROFILE" --region "$REGION"
}

case "$STAGE" in
    build) do_build ;;
    package) do_package ;;
    changeset) do_changeset ;;
    deploy) do_deploy ;;
    all) do_build; do_package; do_changeset; do_deploy ;;
    *) echo "unknown stage: $STAGE" >&2; usage >&2; exit 2 ;;
esac
