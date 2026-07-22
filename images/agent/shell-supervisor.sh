#!/usr/bin/env bash
set -uo pipefail

trap 'exit 0' HUP TERM

recovery_file=/workspace/.bosun-state/recovery.json
if [[ -f "${recovery_file}" ]] && /usr/local/bin/bosun-runtime-control validate; then
  snapshot=/workspace/.bosun-state/terminal.cast
  if [[ -s "${snapshot}" ]]; then
    cat "${snapshot}"
    printf '\r\n[Bosun] 运行时已重启，上方为休眠前的终端快照。\r\n'
  fi
  claude_session_id="$(jq -r '.claude.sessionID // empty' "${recovery_file}")"
  claude_was_running="$(jq -r '.claude.wasRunning' "${recovery_file}")"
  if [[ "${claude_was_running}" == true ]] &&
    [[ "${claude_session_id}" =~ ^[0-9a-fA-F-]{16,64}$ ]]; then
    claude --resume "${claude_session_id}"
    printf '\r\n[Bosun] Claude Code exited; starting an interactive shell.\r\n'
  fi
fi

while true; do
  /bin/bash
  status=$?
  printf '\r\n[Bosun] Shell exited with status %d; starting a new shell.\r\n' "${status}"
  sleep 0.2
done
