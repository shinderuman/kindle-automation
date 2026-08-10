#!/usr/bin/env bash
# scripts/deploy.sh — build / package / change set 確認 / deploy を分離する（AGENTS.md 13）。
# 明示的な --profile と --region を必須とし、AWS default 値へ暗黙依存しない。
# deploy 段階は sam deploy で別 change set を作らず、changeset 段階で人間が確認した
# 同一 change set を execute-change-set で適用するだけとする。
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
TEMPLATE="$ROOT_DIR/infra/template.yaml"
BUILD_DIR="$ROOT_DIR/.aws-sam"
BUILD_TEMPLATE="$BUILD_DIR/build/template.yaml"
PACKAGED="$BUILD_DIR/packaged.yaml"
# changeset 段階で作成した change set の id 等を deploy 段階へ確実に引き継ぐ状態 file。
# 曖昧な推測に依存せず、確認した同一 change set だけを execute するための単一の真実の源。
CHANGESET_STATE="$BUILD_DIR/changeset.state"

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
  build       sam build で Lambda バイナリを生成し .aws-sam/build/template.yaml へ出力する
  package     sam package で build 済み template (.aws-sam/build/template.yaml) を package する
  changeset   変更内容を change set として作成（execute なし）し、確認用 id を保存する
  deploy      changeset 段階で確認した同一 change set だけを execute する
  all         build → package → changeset まで実行して停止する（deploy は行わない）
Options:
  --stack-name NAME        CloudFormation stack 名（default: ${STACK_NAME}）
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

mkdir -p "$BUILD_DIR"

# package 先 S3 bucket の解決方法。明示指定がなければ SAM 管理bucket を使う。
pkg_s3_args=()
if [[ -n "$S3_BUCKET" ]]; then
    pkg_s3_args=(--s3-bucket "$S3_BUCKET")
else
    pkg_s3_args=(--resolve-s3)
fi

# stack が既存かどうかで CREATE / UPDATE を切り替える。
# create-change-set は --change-set-type が必要で、未指定時は UPDATE 扱いになるため
# 初回 deploy（stack 未作成）を失敗させないよう実在確認で決める。
detect_change_set_type() {
    if aws cloudformation describe-stacks --stack-name "$STACK_NAME" \
        --profile "$PROFILE" --region "$REGION" >/dev/null 2>&1; then
        echo "UPDATE"
    else
        echo "CREATE"
    fi
}

do_build() {
    echo "==> sam build"
    # build 出力先を固定し、package 段階が参照する build template の位置を確定させる。
    sam build --template-file "$TEMPLATE" --build-dir "$BUILD_DIR" \
        --profile "$PROFILE" --region "$REGION"
}

do_package() {
    # build 済み template を package する。元 template を位置引数へ渡さない（レビュー指摘1）。
    if [[ ! -f "$BUILD_TEMPLATE" ]]; then
        echo "build 済み template が見つかりません: $BUILD_TEMPLATE" >&2
        echo "先に --stage build を実行してください。" >&2
        return 1
    fi
    echo "==> sam package (build 済み template): $BUILD_TEMPLATE -> $PACKAGED"
    sam package --template-file "$BUILD_TEMPLATE" --output-template-file "$PACKAGED" \
        --profile "$PROFILE" --region "$REGION" "${pkg_s3_args[@]}"
}

do_changeset() {
    if [[ ! -f "$PACKAGED" ]]; then
        echo "package 済み template が見つかりません: $PACKAGED" >&2
        echo "先に --stage build, package を実行してください。" >&2
        return 1
    fi
    local changeset_name changeset_type changeset_id status reason
    local p
    # create-change-set は --parameters ParameterKey=K,ParameterValue=V 形式。
    # SAM の --parameter-overrides K=V とは違うため、ここで変換する。
    local cs_extra=()
    if ((${#PARAMS[@]})); then
        for p in "${PARAMS[@]}"; do
            cs_extra+=("ParameterKey=${p%%=*},ParameterValue=${p#*=}")
        done
    fi
    changeset_name="cs-${STACK_NAME}-$(date -u +%Y%m%d%H%M%S)"
    changeset_type="$(detect_change_set_type)"
    echo "==> create change set (no execute): $STACK_NAME / $changeset_name ($changeset_type)"
    # create-change-set は生成した Id を返すので、これを deploy 段階の入力にする。
    local cs_args=(aws cloudformation create-change-set
        --stack-name "$STACK_NAME"
        --change-set-name "$changeset_name"
        --change-set-type "$changeset_type"
        --template-body "file://$PACKAGED"
        --capabilities CAPABILITY_IAM
        --profile "$PROFILE" --region "$REGION"
        --query Id --output text)
    # bash 3.2 + set -u で空配列を展開できないため、--parameters は非空時だけ付与する。
    if ((${#cs_extra[@]})); then
        cs_args+=(--parameters "${cs_extra[@]}")
    fi
    changeset_id="$("${cs_args[@]}")"
    echo "==> waiting for change set creation: $changeset_id"
    # 変更が無い場合は CREATE_COMPLETE へ到達せず FAILED になるため、wait 失敗を拾う。
    if ! aws cloudformation wait change-set-create-complete \
        --change-set-name "$changeset_id" \
        --profile "$PROFILE" --region "$REGION"; then
        status="$(aws cloudformation describe-change-set \
            --change-set-name "$changeset_id" \
            --profile "$PROFILE" --region "$REGION" \
            --query 'Status' --output text)"
        reason="$(aws cloudformation describe-change-set \
            --change-set-name "$changeset_id" \
            --profile "$PROFILE" --region "$REGION" \
            --query 'StatusReason' --output text)"
        # deploy 可能な change set が無いので、古い state を誤って execute させない。
        rm -f "$CHANGESET_STATE"
        echo "change set の作成に失敗しました (status=$status): $reason" >&2
        echo "deploy 可能な change set がないため state を破棄しました。" >&2
        return 1
    fi
    # 後続 execute に必要な情報を state file へ確定させる（推測に依存しない）。
    cat > "$CHANGESET_STATE" <<EOF
STACK_NAME=$STACK_NAME
CHANGESET_NAME=$changeset_name
CHANGESET_ID=$changeset_id
CHANGESET_TYPE=$changeset_type
EOF
    echo "変更内容を確認してください:"
    echo "  aws cloudformation describe-change-set --change-set-name $changeset_id --profile $PROFILE --region $REGION"
    echo "問題なければ --stage deploy で同一 change set を適用してください。"
}

# state file は本 script が changeset 段階で書いた制御済み値だけを想定して読み込む。
read_changeset_state() {
    local requested_stack="$STACK_NAME"
    # shellcheck disable=SC1090
    source "$CHANGESET_STATE"
    if [[ "${STACK_NAME:-}" != "$requested_stack" || -z "${CHANGESET_ID:-}" || -z "${CHANGESET_TYPE:-}" ]]; then
        echo "change set の状態が不正です（stack 不一致または id/type 欠落）。" >&2
        echo "再度 --stage changeset を実行してください。" >&2
        return 1
    fi
}

do_deploy() {
    # deploy は changeset 段階で確認した同一 change set の execute のみ行う（レビュー指摘3）。
    if [[ ! -f "$CHANGESET_STATE" ]]; then
        echo "change set の状態 file が見つかりません: $CHANGESET_STATE" >&2
        echo "先に --stage changeset を実行し、確認済みの change set を作成してください。" >&2
        return 1
    fi
    read_changeset_state
    local status
    status="$(aws cloudformation describe-change-set \
        --change-set-name "$CHANGESET_ID" \
        --profile "$PROFILE" --region "$REGION" \
        --query 'Status' --output text)"
    # 失敗・未確認・状態不正時には execute しない。
    if [[ "$status" != "CREATE_COMPLETE" ]]; then
        echo "change set が適用可能状態ではありません (status=$status): $CHANGESET_ID" >&2
        echo "再度 --stage changeset を実行して新しい change set を作成してください。" >&2
        return 1
    fi
    echo "==> execute change set (changeset 段階で確認した同一 change set): $CHANGESET_ID"
    aws cloudformation execute-change-set \
        --change-set-name "$CHANGESET_ID" \
        --profile "$PROFILE" --region "$REGION"
    echo "==> waiting for stack to reach terminal state: $STACK_NAME ($CHANGESET_TYPE)"
    if [[ "$CHANGESET_TYPE" == "CREATE" ]]; then
        aws cloudformation wait stack-create-complete --stack-name "$STACK_NAME" \
            --profile "$PROFILE" --region "$REGION"
    else
        aws cloudformation wait stack-update-complete --stack-name "$STACK_NAME" \
            --profile "$PROFILE" --region "$REGION"
    fi
}

case "$STAGE" in
    build) do_build ;;
    package) do_package ;;
    changeset) do_changeset ;;
    deploy) do_deploy ;;
    # all は deploy しない。明示的な --stage deploy を要求する（レビュー指摘3）。
    all)
        do_build
        do_package
        do_changeset
        echo "==> all は build → package → changeset までで停止しました。deploy は --stage deploy で明示的に行ってください。"
        ;;
    *) echo "unknown stage: $STAGE" >&2; usage >&2; exit 2 ;;
esac
