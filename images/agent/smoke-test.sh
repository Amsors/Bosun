#!/usr/bin/env bash
set -euo pipefail

image="${1:-bosun-agent:smoke}"
docker build --tag "${image}" "$(dirname "$0")"

container="$(
  docker run --detach --rm --read-only \
    --tmpfs /workspace:rw,nosuid,uid=10001,gid=10001,mode=0750,size=64m \
    --tmpfs /tmp:rw,noexec,nosuid,mode=1777,size=64m \
    --tmpfs /run/bosun-tmux:rw,noexec,nosuid,uid=10001,gid=10001,mode=0750,size=16m \
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
docker exec "${container}" test "$(docker exec "${container}" id -u)" -eq 10001
docker exec "${container}" test ! -w /etc/claude-code/managed-settings.json
docker exec "${container}" test ! -w /usr/local/lib/bosun/hooks/session-start
docker exec "${container}" test ! -e /var/run/secrets/kubernetes.io/serviceaccount/token
docker exec "${container}" test "$(docker exec "${container}" stat -c '%u:%g' /workspace/.bosun-home/.claude.json)" = "10001:10001"
docker exec "${container}" jq -e '.hasCompletedOnboarding == true' /workspace/.bosun-home/.claude.json >/dev/null
docker exec "${container}" jq -e \
  '.allowManagedHooksOnly == true and
   .hooks.SessionStart[0].hooks[0].command == "/usr/local/lib/bosun/hooks/session-start"' \
  /etc/claude-code/managed-settings.json >/dev/null
printf '{"hook_event_name":"SessionStart"}' |
  docker exec --interactive "${container}" /usr/local/lib/bosun/hooks/session-start
