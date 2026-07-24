#!/usr/bin/env bash
set -euo pipefail

image="${1:-bosun-agent:smoke}"
session_id=018f9c6e-1234-7000-8000-abcdef012499
docker build --tag "${image}" "$(dirname "$0")"

container="$(
  docker run --detach --rm --read-only \
    --tmpfs /workspace:rw,nosuid,uid=10001,gid=10001,mode=0750,size=64m \
    --tmpfs /tmp:rw,noexec,nosuid,mode=1777,size=64m \
    --tmpfs /run/bosun-tmux:rw,noexec,nosuid,uid=10001,gid=10001,mode=0750,size=16m \
    --env BOSUN_SESSION_ID="${session_id}" \
    --env BOSUN_AGENT_IMAGE="${image}" \
    --env ANTHROPIC_BASE_URL=http://127.0.0.1:8080 \
    "${image}"
)"
trap 'docker rm --force "${container}" >/dev/null 2>&1 || true' EXIT

for _ in {1..20}; do
  if docker exec "${container}" tmux has-session -t bosun 2>/dev/null; then
    break
  fi
  sleep 0.25
done

docker exec "${container}" /usr/local/bin/bosun-entrypoint --smoke-test
docker exec "${container}" tmux has-session -t bosun
docker exec "${container}" tmux send-keys -t bosun exit Enter
for _ in {1..20}; do
  if docker exec "${container}" tmux has-session -t bosun 2>/dev/null; then
    docker exec "${container}" tmux send-keys -t bosun 'printf bosun-shell-restarted' Enter
    sleep 0.1
    if docker exec "${container}" tmux capture-pane -p -t bosun |
      grep -q 'bosun-shell-restarted'; then
      break
    fi
  fi
  sleep 0.25
done
docker exec "${container}" tmux has-session -t bosun
docker exec "${container}" tmux capture-pane -p -t bosun |
  grep -q 'bosun-shell-restarted'
docker exec "${container}" tmux send-keys -t bosun -l "printf '中文 ─╭╯ ✓ ⏺ ⎿ 🟢'"
docker exec "${container}" tmux send-keys -t bosun Enter
sleep 0.1
docker exec "${container}" tmux capture-pane -p -t bosun |
  grep -Fq '中文 ─╭╯ ✓ ⏺ ⎿ 🟢'
docker exec "${container}" test "$(docker exec "${container}" id -u)" -eq 10001
docker exec "${container}" test ! -w /etc/claude-code/managed-settings.json
docker exec "${container}" test ! -w /usr/local/lib/bosun/hooks/session-start
docker exec "${container}" test ! -e /var/run/secrets/kubernetes.io/serviceaccount/token
docker exec "${container}" test "$(docker exec "${container}" stat -c '%u:%g' /workspace/.bosun-home/.claude.json)" = "10001:10001"
docker exec "${container}" test "$(docker exec "${container}" stat -c '%u:%g' /workspace/.bosun-home/.claude/settings.json)" = "10001:10001"
docker exec "${container}" jq -e '.hasCompletedOnboarding == true' /workspace/.bosun-home/.claude.json >/dev/null
docker exec "${container}" jq -e \
  '.permissions.defaultMode == "bypassPermissions" and
   .skipDangerousModePermissionPrompt == true' \
  /workspace/.bosun-home/.claude/settings.json >/dev/null
docker exec "${container}" /bin/bash -c \
  'jq '\''. + {userSetting:"preserved"}'\'' "${HOME}/.claude.json" > "${HOME}/.claude.json.next" && mv "${HOME}/.claude.json.next" "${HOME}/.claude.json"'
docker exec "${container}" /bin/bash -c \
  'jq '\''. + {userSetting:"preserved"}'\'' "${HOME}/.claude/settings.json" > "${HOME}/.claude/settings.json.next" && mv "${HOME}/.claude/settings.json.next" "${HOME}/.claude/settings.json"'
docker exec "${container}" /usr/local/bin/bosun-entrypoint true
docker exec "${container}" jq -e \
  '.hasCompletedOnboarding == true and .userSetting == "preserved"' \
  /workspace/.bosun-home/.claude.json >/dev/null
docker exec "${container}" jq -e \
  '.permissions.defaultMode == "bypassPermissions" and
   .skipDangerousModePermissionPrompt == true and
   .userSetting == "preserved"' \
  /workspace/.bosun-home/.claude/settings.json >/dev/null
docker exec "${container}" jq -e \
  '.allowManagedHooksOnly == true and
   .hooks.SessionStart[0].hooks[0].command == "/usr/local/lib/bosun/hooks/session-start"' \
  /etc/claude-code/managed-settings.json >/dev/null
printf '{"hook_event_name":"SessionStart","session_id":"%s"}' "${session_id}" |
  docker exec --interactive "${container}" /usr/local/lib/bosun/hooks/session-start
docker exec "${container}" test "$(docker exec "${container}" cat /workspace/.bosun-state/claude-session-id)" = "${session_id}"

docker exec --detach "${container}" /usr/local/bin/bosun-auth-proxy \
  --listen=127.0.0.1:8080 \
  --upstream=http://127.0.0.1:18081 \
  --token-file=/workspace/proxy-token
for _ in {1..20}; do
  if docker exec "${container}" /usr/local/bin/bosun-auth-proxy \
    --healthcheck=http://127.0.0.1:8080/healthz; then
    break
  fi
  sleep 0.1
done
docker exec "${container}" tmux send-keys -t bosun -l 'printf bosun-before-hibernate'
docker exec "${container}" tmux send-keys -t bosun Enter
sleep 0.1
docker exec "${container}" /usr/local/bin/bosun-runtime-control quiesce sha256:smoke |
  jq -e '.sizeBytes > 0 and .agentImageDigest == "sha256:smoke"' >/dev/null
docker exec "${container}" /usr/local/bin/bosun-runtime-control validate
docker exec "${container}" jq -e \
  --arg session_id "${session_id}" \
  '.schemaVersion == 1 and .sessionID == $session_id and .quiesce.succeeded == true' \
  /workspace/.bosun-state/recovery.json >/dev/null
docker exec "${container}" tmux kill-server
docker exec "${container}" /usr/local/bin/bosun-entrypoint true
for _ in {1..20}; do
  if docker exec "${container}" tmux capture-pane -p -S - -t bosun 2>/dev/null |
    grep -q 'bosun-before-hibernate'; then
    break
  fi
  sleep 0.1
done
docker exec "${container}" tmux capture-pane -p -S - -t bosun |
  grep -q 'bosun-before-hibernate'
