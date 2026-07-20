#!/usr/bin/env bash
set -euo pipefail

: "${BOSUN_BASE_URL:?必须设置 BOSUN_BASE_URL}"
: "${KUBECONFIG:?必须设置 KUBECONFIG}"

api() {
  local method="$1"
  local path="$2"
  shift 2
  curl --fail-with-body --silent --show-error \
    --request "${method}" "${BOSUN_BASE_URL}/api/v1${path}" "$@"
}

assert_code_zero() {
  jq --exit-status '.code == 0' >/dev/null
}

wait_json() {
  local attempts="$1"
  local interval="$2"
  local command="$3"
  local predicate="$4"
  local output
  for ((i = 1; i <= attempts; i++)); do
    output="$(eval "${command}")"
    if jq --exit-status "${predicate}" <<<"${output}" >/dev/null; then
      printf '%s' "${output}"
      return 0
    fi
    sleep "${interval}"
  done
  printf '%s\n' "${output}" >&2
  return 1
}
