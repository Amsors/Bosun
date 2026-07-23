#!/usr/bin/env bash
set -euo pipefail

payload="$(cat)"
event="$(jq -r '.hook_event_name // empty' <<<"${payload}")"

case "${event}" in
  SessionStart | Stop)
    state=awaiting_input
    ;;
  UserPromptSubmit | PostToolUse | PostToolUseFailure)
    state=working
    ;;
  PreToolUse)
    if [[ "$(jq -r '.tool_name // empty' <<<"${payload}")" == "AskUserQuestion" ]]; then
      state=awaiting_choice
    else
      state=working
    fi
    ;;
  PermissionRequest)
    state=awaiting_approval
    ;;
  SessionEnd)
    state=stopped
    ;;
  *)
    exit 0
    ;;
esac

state_dir=/workspace/.bosun-state
mkdir -p "${state_dir}"
state_tmp="${state_dir}/agent-status.tmp.$$"
printf '%s\n' "${state}" > "${state_tmp}"
chmod 0600 "${state_tmp}"
mv -f "${state_tmp}" "${state_dir}/agent-status"
