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

mkdir -p "${HOME}" "${TMUX_TMPDIR}" "${state_dir}/runtime/tmp" "${state_dir}/runtime/tmux"

claude_config="${HOME}/.claude.json"
if [[ ! -e "${claude_config}" ]]; then
  printf '%s\n' '{"hasCompletedOnboarding":true}' > "${claude_config}"
else
  if jq -e 'type == "object"' "${claude_config}" >/dev/null; then
    config_tmp="${claude_config}.tmp.$$"
    jq '. + {hasCompletedOnboarding:true}' "${claude_config}" > "${config_tmp}"
    chmod --reference="${claude_config}" "${config_tmp}" 2>/dev/null || chmod 0600 "${config_tmp}"
    mv -f "${config_tmp}" "${claude_config}"
  else
    printf '[Bosun] Existing .claude.json is invalid; preserving it unchanged.\n' >&2
  fi
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
