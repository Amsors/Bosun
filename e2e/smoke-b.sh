#!/usr/bin/env bash
set -euo pipefail

# shellcheck source=lib.sh
source "$(dirname "$0")/lib.sh"
: "${BOSUN_E2E_ACCESS_TOKEN:?必须设置 BOSUN_E2E_ACCESS_TOKEN}"

created="$(
  api POST /sessions \
    -H 'Content-Type: application/json' \
    -H "Authorization: Bearer ${BOSUN_E2E_ACCESS_TOKEN}" \
    -H "Idempotency-Key: $(uuidgen)" \
    --data '{"tier":"small","runtime":"claude-code","provider":{"mode":"platform"},"storagePolicy":"local"}'
)"
assert_code_zero <<<"${created}"
session_id="$(jq -r '.data.id' <<<"${created}")"
session_short_id="$(printf '%s' "${session_id}" | sha256sum | cut -c1-12)"

wait_json 90 2 \
  "api GET /sessions/${session_id} -H 'Authorization: Bearer ${BOSUN_E2E_ACCESS_TOKEN}'" \
  '.code == 0 and .data.phase == "Running"' >/dev/null

api DELETE "/sessions/${session_id}" -H "Authorization: Bearer ${BOSUN_E2E_ACCESS_TOKEN}" |
  assert_code_zero

for ((i = 1; i <= 60; i++)); do
  if ! kubectl get agentsession "sess-${session_short_id}" >/dev/null 2>&1 &&
    ! kubectl get pod,pvc -A -l "bosun.io/session=${session_id}" -o name | grep --quiet .; then
    printf '{"sessionID":"%s","cleanup":"ok"}\n' "${session_id}"
    exit 0
  fi
  sleep 2
done

kubectl get pod,pvc,agentsession -A -l "bosun.io/session=${session_id}" >&2
exit 1
