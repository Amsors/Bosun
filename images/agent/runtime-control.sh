#!/usr/bin/env bash
set -euo pipefail

readonly state_dir=/workspace/.bosun-state
readonly recovery_file="${state_dir}/recovery.json"
readonly terminal_file="${state_dir}/terminal.cast"
readonly schema_file=/usr/local/share/bosun/recovery.schema.json
readonly max_terminal_bytes=$((1024 * 1024))

validate_recovery() {
  [[ -r "${schema_file}" && -r "${recovery_file}" ]] || return 1
  jq -e --arg session_id "${BOSUN_SESSION_ID:-}" '
    .schemaVersion == 1 and
    (.sessionID | type == "string" and length > 0) and
    ($session_id == "" or .sessionID == $session_id) and
    (.createdAt | type == "string" and length > 0) and
    (.agent.imageDigest | type == "string") and
    (.agent.claudeCodeVersion | type == "string" and length > 0) and
    (.workspace.path | type == "string" and startswith("/workspace")) and
    (.tmux.session == "bosun") and
    (.tmux.pane | type == "string") and
    (.tmux.window | type == "string") and
    (.tmux.cols | type == "number" and . > 0) and
    (.tmux.rows | type == "number" and . > 0) and
    (.claude.wasRunning | type == "boolean") and
    (.claude.sessionID == null or (.claude.sessionID | type == "string")) and
    (.terminal.path == "/workspace/.bosun-state/terminal.cast") and
    (.terminal.sizeBytes | type == "number" and . >= 0 and . <= 1048576) and
    (.terminal.sha256 | test("^[0-9a-f]{64}$")) and
    (.quiesce.succeeded == true) and
    (.quiesce.unfinishedExternalRequests == false)
  ' "${recovery_file}" >/dev/null || return 1

  local expected_size expected_sha actual_size actual_sha
  expected_size="$(jq -r '.terminal.sizeBytes' "${recovery_file}")"
  expected_sha="$(jq -r '.terminal.sha256' "${recovery_file}")"
  [[ -f "${terminal_file}" ]] || return 1
  actual_size="$(stat -c '%s' "${terminal_file}")"
  actual_sha="$(sha256sum "${terminal_file}" | awk '{print $1}')"
  [[ "${actual_size}" == "${expected_size}" && "${actual_sha}" == "${expected_sha}" ]]
}

quiesce() {
  local image_digest="${1:-}"
  [[ -n "${BOSUN_SESSION_ID:-}" ]] || {
    printf 'BOSUN_SESSION_ID is required\n' >&2
    return 1
  }
  mkdir -p "${state_dir}/runtime/tmp" "${state_dir}/runtime/tmux"
  : > "${state_dir}/quiescing"

  local drain_response
  drain_response="$(curl --fail --silent --show-error --max-time 28 \
    --request POST 'http://127.0.0.1:8080/__bosun/drain?timeout=25s')"
  jq -e '.draining == true and .activeRequests == 0' <<<"${drain_response}" >/dev/null

  tmux detach-client -s bosun 2>/dev/null || true
  local terminal_tmp="${terminal_file}.tmp.$$"
  tmux capture-pane -p -e -S - -t bosun > "${terminal_tmp}"
  if (( $(stat -c '%s' "${terminal_tmp}") > max_terminal_bytes )); then
    tail -c "${max_terminal_bytes}" "${terminal_tmp}" > "${terminal_tmp}.bounded"
    mv -f "${terminal_tmp}.bounded" "${terminal_tmp}"
  fi
  chmod 0600 "${terminal_tmp}"
  mv -f "${terminal_tmp}" "${terminal_file}"

  local pane window cols rows current_path current_command claude_session_id=null
  pane="$(tmux display-message -p -t bosun '#{pane_index}')"
  window="$(tmux display-message -p -t bosun '#{window_index}')"
  cols="$(tmux display-message -p -t bosun '#{pane_width}')"
  rows="$(tmux display-message -p -t bosun '#{pane_height}')"
  current_path="$(tmux display-message -p -t bosun '#{pane_current_path}')"
  current_command="$(tmux display-message -p -t bosun '#{pane_current_command}')"
  if [[ "${current_path}" != /workspace && "${current_path}" != /workspace/* ]]; then
    current_path=/workspace
  fi
  if [[ -r "${state_dir}/claude-session-id" ]]; then
    local candidate
    candidate="$(head -n 1 "${state_dir}/claude-session-id")"
    if [[ "${candidate}" =~ ^[0-9a-fA-F-]{16,64}$ ]]; then
      claude_session_id="$(jq -Rn --arg value "${candidate}" '$value')"
    fi
  fi

  local was_running=false
  if [[ "${claude_session_id}" != null ]] &&
    { [[ "${current_command}" == claude ]] || pgrep -u "$(id -u)" -f '[/]usr/local/bin/claude|[@]anthropic-ai/claude-code' >/dev/null; }; then
    was_running=true
  fi
  local terminal_size terminal_sha created_at manifest_tmp
  terminal_size="$(stat -c '%s' "${terminal_file}")"
  terminal_sha="$(sha256sum "${terminal_file}" | awk '{print $1}')"
  created_at="$(date -u +'%Y-%m-%dT%H:%M:%SZ')"
  manifest_tmp="${recovery_file}.tmp.$$"

  jq -n \
    --arg session_id "${BOSUN_SESSION_ID}" \
    --arg created_at "${created_at}" \
    --arg image_digest "${image_digest}" \
    --arg image_ref "${BOSUN_AGENT_IMAGE:-}" \
    --arg claude_version "${CLAUDE_CODE_VERSION}" \
    --arg workspace_path "${current_path}" \
    --arg pane "${pane}" \
    --arg window "${window}" \
    --argjson cols "${cols}" \
    --argjson rows "${rows}" \
    --arg current_command "${current_command}" \
    --argjson claude_session_id "${claude_session_id}" \
    --argjson was_running "${was_running}" \
    --argjson terminal_size "${terminal_size}" \
    --arg terminal_sha "${terminal_sha}" \
    '{
      schemaVersion: 1,
      sessionID: $session_id,
      createdAt: $created_at,
      agent: {
        imageDigest: $image_digest,
        imageReference: $image_ref,
        claudeCodeVersion: $claude_version
      },
      workspace: {path: $workspace_path},
      tmux: {
        session: "bosun",
        window: $window,
        pane: $pane,
        cols: $cols,
        rows: $rows,
        currentCommand: $current_command
      },
      claude: {sessionID: $claude_session_id, wasRunning: $was_running},
      terminal: {
        path: "/workspace/.bosun-state/terminal.cast",
        sizeBytes: $terminal_size,
        sha256: $terminal_sha
      },
      quiesce: {succeeded: true, unfinishedExternalRequests: false}
    }' > "${manifest_tmp}"
  chmod 0600 "${manifest_tmp}"
  mv -f "${manifest_tmp}" "${recovery_file}"
  sync -f "${terminal_file}"
  sync -f "${recovery_file}"
  sync -f "${state_dir}"
  sync -f /workspace
  validate_recovery
  jq -c '{createdAt, sizeBytes: .terminal.sizeBytes, sha256: .terminal.sha256, agentImageDigest: .agent.imageDigest}' "${recovery_file}"
}

case "${1:-}" in
  quiesce)
    shift
    quiesce "${1:-}"
    ;;
  validate|ready)
    validate_recovery
    ;;
  *)
    printf 'usage: bosun-runtime-control {quiesce [agent-image-digest]|validate|ready}\n' >&2
    exit 2
    ;;
esac
