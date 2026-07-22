#!/usr/bin/env bash
set -euo pipefail

payload="$(cat)"
jq -e '.hook_event_name == "SessionStart"' <<<"${payload}" >/dev/null
test "${ANTHROPIC_BASE_URL:-}" = "http://127.0.0.1:8080"
test -d /workspace
test -w /workspace

session_id="$(jq -r '.session_id // empty' <<<"${payload}")"
if [[ "${session_id}" =~ ^[0-9a-fA-F-]{16,64}$ ]]; then
  state_dir=/workspace/.bosun-state
  mkdir -p "${state_dir}"
  session_tmp="${state_dir}/claude-session-id.tmp.$$"
  printf '%s\n' "${session_id}" > "${session_tmp}"
  chmod 0600 "${session_tmp}"
  mv -f "${session_tmp}" "${state_dir}/claude-session-id"
  sync -f "${state_dir}/claude-session-id"
fi
