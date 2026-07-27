#!/usr/bin/env bash
set -euo pipefail

# shellcheck source=lib.sh
source "$(dirname "$0")/lib.sh"
: "${BOSUN_E2E_ACCESS_TOKEN:?必须设置 BOSUN_E2E_ACCESS_TOKEN}"

stress_pid=""
metrics_replicas=""
session_id=""
pod_namespace=""
pod_name=""

cleanup() {
  if [[ -n "${stress_pid}" ]]; then
    kill "${stress_pid}" >/dev/null 2>&1 || true
  fi
  if [[ -n "${pod_namespace}" && -n "${pod_name}" ]]; then
    kubectl --namespace "${pod_namespace}" exec "${pod_name}" --container agent -- \
      pkill --full cpu_mem_stress.py >/dev/null 2>&1 || true
  fi
  if [[ -n "${metrics_replicas}" ]]; then
    kubectl --namespace kube-system scale deployment/metrics-server \
      --replicas="${metrics_replicas}" >/dev/null 2>&1 || true
  fi
  if [[ -n "${session_id}" ]]; then
    api DELETE "/sessions/${session_id}" \
      -H "Authorization: Bearer ${BOSUN_E2E_ACCESS_TOKEN}" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

cluster_snapshot() {
  api GET /admin/cluster
}

scaling_predicate() {
  local expression="$1"
  printf '.code == 0 and (.data.pods[] | select(.sessionID == "%s") | %s)' \
    "${session_id}" "${expression}"
}

assert_memory_high() {
  local stage="$1"
  local snapshot
  local request_bytes=""
  local limit_bytes=""
  local expected
  local actual=""

  for _ in {1..15}; do
    snapshot="$(cluster_snapshot)"
    if request_bytes="$(
      jq -er --arg session_id "${session_id}" '
        .data.pods[] |
        select(.sessionID == $session_id) |
        .containers[] |
        select(.name == "agent") |
        .requests.memoryBytes
      ' <<<"${snapshot}"
    )" && limit_bytes="$(
      jq -er --arg session_id "${session_id}" '
        .data.pods[] |
        select(.sessionID == $session_id) |
        .resourceScaling.actualResources.memoryBytes
      ' <<<"${snapshot}"
    )"; then
      break
    fi
    sleep 1
  done
  if [[ -z "${request_bytes}" || -z "${limit_bytes}" ]]; then
    printf 'MemoryQoS %s check failed: agent actual resources are unavailable\n' "${stage}" >&2
    return 1
  fi

  expected=$((request_bytes + ((limit_bytes - request_bytes) * 9 / 10)))
  if ((expected > limit_bytes)); then
    expected="${limit_bytes}"
  fi

  for _ in {1..15}; do
    actual="$(
      kubectl --namespace "${pod_namespace}" exec "${pod_name}" --container agent -- \
        cat /sys/fs/cgroup/memory.high
    )"
    if [[ "${actual}" == "${expected}" ]]; then
      return 0
    fi
    sleep 1
  done

  printf '%s\n' \
    "MemoryQoS ${stage} check failed: request=${request_bytes}, limit=${limit_bytes}, expected memory.high=${expected}, actual=${actual}" \
    >&2
  return 1
}

start_stress() {
  local duration="$1"
  kubectl --namespace "${pod_namespace}" exec "${pod_name}" --container agent -- \
    python3 /usr/local/share/bosun/user-defaults/cpu_mem_stress.py \
    --cpu-cores 1 --memory 0 --duration "${duration}" --ramp-up 0 --quiet \
    >/dev/null 2>&1 &
  stress_pid=$!
}

stop_stress() {
  kubectl --namespace "${pod_namespace}" exec "${pod_name}" --container agent -- \
    pkill --full cpu_mem_stress.py >/dev/null 2>&1 || true
  if [[ -n "${stress_pid}" ]]; then
    wait "${stress_pid}" >/dev/null 2>&1 || true
    stress_pid=""
  fi
}

created="$(
  api POST /sessions \
    -H 'Content-Type: application/json' \
    -H "Authorization: Bearer ${BOSUN_E2E_ACCESS_TOKEN}" \
    -H "Idempotency-Key: $(uuidgen)" \
    --data '{"runtime":"claude-code","provider":{"mode":"platform"},"storagePolicy":"local","memoryRequest":"2Gi"}'
)"
assert_code_zero <<<"${created}"
session_id="$(jq -r '.data.id' <<<"${created}")"

wait_json 90 2 \
  "api GET /sessions/${session_id} -H 'Authorization: Bearer ${BOSUN_E2E_ACCESS_TOKEN}'" \
  '.code == 0 and .data.phase == "Running"' >/dev/null

pod_ref="$(
  kubectl get pods --all-namespaces \
    --selector "bosun.io/session=${session_id}" \
    --output json |
    jq -r '.items[0] | [.metadata.namespace, .metadata.name] | @tsv'
)"
IFS=$'\t' read -r pod_namespace pod_name <<<"${pod_ref}"
initial_restart_count="$(
  kubectl --namespace "${pod_namespace}" get pod "${pod_name}" --output json |
    jq -r '.status.containerStatuses[] | select(.name == "agent") | .restartCount'
)"
sidecar_limits="$(
  kubectl --namespace "${pod_namespace}" get pod "${pod_name}" --output json |
    jq -c '.spec.containers[] | select(.name == "auth-proxy") | .resources'
)"
assert_memory_high "initial"

start_stress 90s
scaled_up="$(
  wait_json 30 5 \
    cluster_snapshot \
    "$(scaling_predicate '.resourceScaling.desiredResources.cpuMillicores > 500 and .resourceScaling.actualResources.cpuMillicores == .resourceScaling.desiredResources.cpuMillicores')"
)"
scaled_cpu="$(
  jq -r --arg session_id "${session_id}" \
    '.data.pods[] | select(.sessionID == $session_id) | .resourceScaling.desiredResources.cpuMillicores' \
    <<<"${scaled_up}"
)"
stop_stress

wait_json 36 5 \
  cluster_snapshot \
  "$(scaling_predicate ".resourceScaling.loadClass == \"CPULow\" and .resourceScaling.desiredResources.cpuMillicores < ${scaled_cpu}")" \
  >/dev/null

before_metrics_outage="$(
  cluster_snapshot |
    jq -r --arg session_id "${session_id}" \
      '.data.pods[] | select(.sessionID == $session_id) | .resourceScaling.desiredResources.cpuMillicores'
)"
metrics_replicas="$(
  kubectl --namespace kube-system get deployment/metrics-server \
    --output jsonpath='{.spec.replicas}'
)"
kubectl --namespace kube-system scale deployment/metrics-server --replicas=0 >/dev/null
for _ in {1..30}; do
  if [[ -z "$(
    kubectl --namespace kube-system get pods \
      --selector k8s-app=metrics-server \
      --output name
  )" ]]; then
    break
  fi
  sleep 1
done
[[ -z "$(
  kubectl --namespace kube-system get pods \
    --selector k8s-app=metrics-server \
    --output name
)" ]]
start_stress 45s
scaled_without_metrics="$(
  wait_json 30 2 \
    cluster_snapshot \
    "$(scaling_predicate ".resourceScaling.desiredResources.cpuMillicores > ${before_metrics_outage} and .resourceScaling.actualResources.cpuMillicores == .resourceScaling.desiredResources.cpuMillicores")"
)"
stop_stress
[[ -n "${scaled_without_metrics}" ]]
kubectl --namespace kube-system scale deployment/metrics-server \
  --replicas="${metrics_replicas}" >/dev/null
metrics_replicas=""

final_restart_count="$(
  kubectl --namespace "${pod_namespace}" get pod "${pod_name}" --output json |
    jq -r '.status.containerStatuses[] | select(.name == "agent") | .restartCount'
)"
final_sidecar_limits="$(
  kubectl --namespace "${pod_namespace}" get pod "${pod_name}" --output json |
    jq -c '.spec.containers[] | select(.name == "auth-proxy") | .resources'
)"
agent_cpu_pair="$(
  kubectl --namespace "${pod_namespace}" get pod "${pod_name}" --output json |
    jq -r '.spec.containers[] | select(.name == "agent") | [.resources.requests.cpu, .resources.limits.cpu] | @tsv'
)"
[[ "${initial_restart_count}" == "${final_restart_count}" ]]
[[ "${sidecar_limits}" == "${final_sidecar_limits}" ]]
IFS=$'\t' read -r agent_cpu_request agent_cpu_limit <<<"${agent_cpu_pair}"
[[ "${agent_cpu_request}" == "${agent_cpu_limit}" ]]

api DELETE "/sessions/${session_id}" \
  -H "Authorization: Bearer ${BOSUN_E2E_ACCESS_TOKEN}" |
  assert_code_zero
session_id=""

jq -cn \
  --arg pod "${pod_namespace}/${pod_name}" \
  --arg scaledCPU "${scaled_cpu}" \
  '{pod:$pod,scaledCPU:$scaledCPU,memoryQoS:"verified",result:"resource autoscaling demo passed"}'
