#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: $0 <base-sha> <head-sha>" >&2
  exit 2
fi

base_sha="$1"
head_sha="$2"

if ! git cat-file -e "${head_sha}^{commit}" 2>/dev/null; then
  echo "head SHA is not available: ${head_sha}" >&2
  exit 1
fi

if [[ "${base_sha}" =~ ^0+$ ]]; then
  changed_files="$(git diff-tree --no-commit-id --name-only --root -r "${head_sha}")"
elif git cat-file -e "${base_sha}^{commit}" 2>/dev/null; then
  # 三点 diff 只取 head 相对共同祖先的净改动。PR 的 base 常领先于分叉点，
  # 两点 diff 会把 base 独有提交的反向差异误算成本次改动而多触发模块 job。
  changed_files="$(git diff --name-only "${base_sha}...${head_sha}")"
else
  echo "base SHA is not available: ${base_sha}" >&2
  exit 1
fi

backend=false
operator=false
frontend=false
agent_image=false
egress_image=false
delivery=false

while IFS= read -r path; do
  case "${path}" in
    .github/*)
      delivery=true
      ;;
    backend/*)
      backend=true
      ;;
    operator/*)
      operator=true
      backend=true
      delivery=true
      ;;
    frontend/*)
      frontend=true
      ;;
    images/agent/*)
      agent_image=true
      ;;
    images/egress-proxy/*)
      egress_image=true
      ;;
    deploy/* | e2e/*)
      delivery=true
      ;;
  esac
done <<<"${changed_files}"

{
  echo "backend=${backend}"
  echo "operator=${operator}"
  echo "frontend=${frontend}"
  echo "agent_image=${agent_image}"
  echo "egress_image=${egress_image}"
  echo "delivery=${delivery}"
} >> "${GITHUB_OUTPUT:-/dev/stdout}"
