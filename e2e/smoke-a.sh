#!/usr/bin/env bash
set -euo pipefail

# shellcheck source=lib.sh
source "$(dirname "$0")/lib.sh"
: "${BOSUN_E2E_PASSWORD:?必须设置 BOSUN_E2E_PASSWORD}"

email="bosun-e2e-$(date +%s)-${RANDOM}@example.test"
register="$(
  api POST /auth/register \
    -H 'Content-Type: application/json' \
    -H "Idempotency-Key: $(uuidgen)" \
    --data "$(jq -cn --arg email "${email}" --arg password "${BOSUN_E2E_PASSWORD}" '{email:$email,password:$password}')"
)"
assert_code_zero <<<"${register}"
user_id="$(jq -r '.data.user.id' <<<"${register}")"

login="$(
  api POST /auth/login \
    -H 'Content-Type: application/json' \
    --data "$(jq -cn --arg email "${email}" --arg password "${BOSUN_E2E_PASSWORD}" '{email:$email,password:$password}')"
)"
assert_code_zero <<<"${login}"
token="$(jq -r '.data.accessToken' <<<"${login}")"

wait_json 60 2 \
  "api GET /me -H 'Authorization: Bearer ${token}'" \
  '.code == 0 and .data.environmentPhase == "Ready"' >/dev/null
namespace="bosun-u-$(printf '%s' "${user_id}" | sha256sum | cut -c1-12)"

for resource in resourcequota limitrange networkpolicy rolebinding; do
  kubectl -n "${namespace}" get "${resource}" -l "app.kubernetes.io/managed-by=bosun" -o name |
    grep --quiet .
done

jq -cn --arg email "${email}" --arg userID "${user_id}" --arg namespace "${namespace}" \
  --arg accessToken "${token}" \
  '{email:$email,userID:$userID,namespace:$namespace,accessToken:$accessToken}'
