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
    --data '{"tier":"small","runtime":"claude-code","provider":{"mode":"platform"},"storagePolicy":"local"}'
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

start_stress 90s
scaled_up="$(
  wait_json 30 5 \
    cluster_snapshot \
    "$(scaling_predicate '.resourceScaling.mode == "Auto" and .resourceScaling.desiredResources.cpuMillicores > 450 and .resourceScaling.actualResources.cpuMillicores == .resourceScaling.desiredResources.cpuMillicores')"
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

manual="$(
  api PUT "/admin/sessions/${session_id}/resources" \
    -H 'Content-Type: application/json' \
    --data '{"cpuMillicores":650,"memoryBytes":1073741824}'
)"
assert_code_zero <<<"${manual}"
wait_json 24 5 \
  cluster_snapshot \
  "$(scaling_predicate '.resourceScaling.mode == "Manual" and .resourceScaling.actualResources.cpuMillicores == 650 and .resourceScaling.actualResources.memoryBytes == 1073741824')" \
  >/dev/null

start_stress 30s
sleep 20
manual_cpu="$(
  cluster_snapshot |
    jq -r --arg session_id "${session_id}" \
      '.data.pods[] | select(.sessionID == $session_id) | .resourceScaling.desiredResources.cpuMillicores'
)"
stop_stress
[[ "${manual_cpu}" == "650" ]]

api DELETE "/admin/sessions/${session_id}/resources" | assert_code_zero
wait_json 24 5 \
  cluster_snapshot \
  "$(scaling_predicate '.resourceScaling.mode == "Auto" and .resourceScaling.actualResources.memoryBytes == 1006632960 and (.resourceScaling.loadClass == "WarmingUp" or .resourceScaling.loadClass == "Stable")')" \
  >/dev/null

before_metrics_outage="$(
  kubectl --namespace "${pod_namespace}" get pod "${pod_name}" --output json |
    jq -r '.spec.containers[] | select(.name == "agent") | .resources.limits.cpu'
)"
metrics_replicas="$(
  kubectl --namespace kube-system get deployment/metrics-server \
    --output jsonpath='{.spec.replicas}'
)"
kubectl --namespace kube-system scale deployment/metrics-server --replicas=0 >/dev/null
sleep 15
after_metrics_outage="$(
  kubectl --namespace "${pod_namespace}" get pod "${pod_name}" --output json |
    jq -r '.spec.containers[] | select(.name == "agent") | .resources.limits.cpu'
)"
[[ "${before_metrics_outage}" == "${after_metrics_outage}" ]]
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
[[ "${initial_restart_count}" == "${final_restart_count}" ]]
[[ "${sidecar_limits}" == "${final_sidecar_limits}" ]]

api DELETE "/sessions/${session_id}" \
  -H "Authorization: Bearer ${BOSUN_E2E_ACCESS_TOKEN}" |
  assert_code_zero
session_id=""

jq -cn \
  --arg pod "${pod_namespace}/${pod_name}" \
  --arg scaledCPU "${scaled_cpu}" \
  '{pod:$pod,scaledCPU:$scaledCPU,result:"resource autoscaling demo passed"}'
