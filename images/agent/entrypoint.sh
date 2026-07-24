#!/usr/bin/env bash
set -euo pipefail

if [[ "${1:-}" == "--smoke-test" ]]; then
  test "$(id -u)" -ne 0
  test "$(node --version)" = "v24.14.0"
  test "$(claude --version | awk '{print $1}')" = "${CLAUDE_CODE_VERSION}"
  test "${LANG}" = "C.UTF-8"
  test "${LC_ALL}" = "C.UTF-8"
  test "$(locale charmap)" = "UTF-8"
  test -x /usr/local/bin/bosun-auth-proxy
  test -x /usr/local/bin/bosun-runtime-control
  test -x /usr/local/lib/bosun/hooks/session-start
  test -r /etc/claude-code/managed-settings.json
  test -r /usr/local/share/bosun/recovery.schema.json
  tmux -V
  exit 0
fi

state_dir=/workspace/.bosun-state
recovery_file="${state_dir}/recovery.json"
workspace_path=/workspace

claude_defaults=/usr/local/share/bosun/claude-defaults
claude_config="${HOME}/.claude.json"
claude_settings_dir="${HOME}/.claude"
claude_settings="${claude_settings_dir}/settings.json"

mkdir -p \
  "${HOME}" \
  "${claude_settings_dir}" \
  "${TMUX_TMPDIR}" \
  "${state_dir}/runtime/tmp" \
  "${state_dir}/runtime/tmux"

if [[ ! -e "${claude_config}" ]]; then
  install -m 0600 "${claude_defaults}/.claude.json" "${claude_config}"
fi

if [[ ! -e "${claude_settings}" ]]; then
  install -m 0600 "${claude_defaults}/settings.json" "${claude_settings}"
fi

if [[ -f "${recovery_file}" ]] && /usr/local/bin/bosun-runtime-control validate; then
  candidate="$(jq -r '.workspace.path' "${recovery_file}")"
  candidate="$(realpath -m "${candidate}")"
  if [[ "${candidate}" == /workspace || "${candidate}" == /workspace/* ]] && [[ -d "${candidate}" ]]; then
    workspace_path="${candidate}"
  fi
fi

rm -f "${state_dir}/quiescing"

if ! tmux has-session -t bosun 2>/dev/null; then
  tmux new-session -d -s bosun -c "${workspace_path}" /usr/local/bin/bosun-shell-supervisor
fi

if (($# > 0)); then
  exec "$@"
fi

exec tail -f /dev/null
