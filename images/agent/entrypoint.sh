#!/usr/bin/env bash
set -euo pipefail

if [[ "${1:-}" == "--smoke-test" ]]; then
  test "$(id -u)" -ne 0
  test "$(node --version)" = "v24.14.0"
  test "$(claude --version | awk '{print $1}')" = "${CLAUDE_CODE_VERSION}"
  test -x /usr/local/bin/bosun-auth-proxy
  test -x /usr/local/lib/bosun/hooks/session-start
  test -r /etc/claude-code/managed-settings.json
  tmux -V
  exit 0
fi

mkdir -p "${HOME}" "${TMUX_TMPDIR}"
printf '%s\n' '{"hasCompletedOnboarding":true}' > "${HOME}/.claude.json"

if ! tmux has-session -t bosun 2>/dev/null; then
  tmux new-session -d -s bosun -c /workspace
fi

if (($# > 0)); then
  exec "$@"
fi

exec tail -f /dev/null
