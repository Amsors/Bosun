#!/usr/bin/env bash
set -euo pipefail

platform_namespace="${BOSUN_PLATFORM_NAMESPACE:-bosun-platform}"
expected_context="${RESET_CONTEXT:-}"
confirmation="${RESET_CONFIRM:-}"
confirmation_phrase="DELETE ALL BOSUN DATA"
delete_timeout="${BOSUN_RESET_DELETE_TIMEOUT:-10m}"
rollout_timeout="${BOSUN_RESET_ROLLOUT_TIMEOUT:-5m}"

api_replicas=""
gateway_replicas=""
platform_stopped=false

die() {
  echo "reset failed: $*" >&2
  exit 1
}

require_command() {
  if ! command -v "$1" >/dev/null 2>&1; then
    die "required command is unavailable: $1"
  fi
}

kube() {
  kubectl --context "${expected_context}" "$@"
}

object_count() {
  local description="$1"
  shift
  local output
  local count=0
  if ! output="$(kube "$@" --output=name)"; then
    die "could not count ${description}"
  fi
  if [[ -z "${output}" ]]; then
    printf '0'
    return
  fi
  while IFS= read -r _; do
    ((count += 1))
  done <<<"${output}"
  printf '%d' "${count}"
}

resources_exist() {
  local description="$1"
  shift
  local output
  if ! output="$(kube "$@" --output=name)"; then
    die "could not inspect ${description}"
  fi
  [[ -n "${output}" ]]
}

ensure_no_resources() {
  local description="$1"
  shift
  local output
  if ! output="$(kube "$@" --output=name)"; then
    die "could not verify that ${description} were deleted"
  fi
  if [[ -n "${output}" ]]; then
    die "${description} remain after reset"
  fi
}

wait_for_no_pods() {
  local selector="$1"
  local timeout_seconds=120
  local deadline=$((SECONDS + timeout_seconds))
  local output

  while true; do
    if ! output="$(
      kube --namespace "${platform_namespace}" get pods \
        --selector "${selector}" --output=name
    )"; then
      die "could not verify whether Pods matching ${selector} have stopped"
    fi
    if [[ -z "${output}" ]]; then
      return
    fi
    if ((SECONDS >= deadline)); then
      die "Pods matching ${selector} did not stop within ${timeout_seconds}s"
    fi
    sleep 2
  done
}

wait_for_resource_deleted() {
  local resource="$1"
  local name="$2"
  local timeout_seconds=300
  local deadline=$((SECONDS + timeout_seconds))
  local output

  while true; do
    if ! output="$(
      kube get "${resource}" "${name}" --ignore-not-found --output=name
    )"; then
      die "could not verify whether ${resource}/${name} was deleted"
    fi
    if [[ -z "${output}" ]]; then
      return
    fi
    if ((SECONDS >= deadline)); then
      die "${resource}/${name} was not deleted within ${timeout_seconds}s"
    fi
    sleep 2
  done
}

restore_platform_after_failure() {
  local status=$?
  trap - EXIT

  if [[ ${status} -ne 0 && "${platform_stopped}" == true ]]; then
    echo "reset did not complete; restoring API and gateway replicas" >&2
    if [[ -n "${api_replicas}" ]]; then
      kube --namespace "${platform_namespace}" scale deployment/bosun-api \
        --replicas="${api_replicas}" >/dev/null 2>&1 || true
    fi
    if [[ -n "${gateway_replicas}" ]]; then
      kube --namespace "${platform_namespace}" scale deployment/bosun-gateway \
        --replicas="${gateway_replicas}" >/dev/null 2>&1 || true
    fi
  fi

  exit "${status}"
}

trap restore_platform_after_failure EXIT

require_command kubectl

if [[ -z "${expected_context}" ]]; then
  die "RESET_CONTEXT must name the exact kubectl context to reset"
fi

current_context="$(kubectl config current-context 2>/dev/null || true)"
if [[ "${current_context}" != "${expected_context}" ]]; then
  die "refusing to operate on context ${current_context:-<none>}; expected ${expected_context}"
fi

kube --request-timeout=10s get namespace "${platform_namespace}" >/dev/null
for resource in \
  customresourcedefinition/agentsessions.bosun.io \
  customresourcedefinition/userenvironments.bosun.io; do
  kube --request-timeout=10s get "${resource}" >/dev/null
done
for resource in \
  deployment/bosun-api \
  deployment/bosun-gateway \
  deployment/bosun-operator \
  statefulset/bosun-postgresql; do
  kube --namespace "${platform_namespace}" --request-timeout=10s get "${resource}" >/dev/null
done

api_replicas="$(
  kube --namespace "${platform_namespace}" get deployment/bosun-api \
    --output=jsonpath='{.spec.replicas}'
)"
gateway_replicas="$(
  kube --namespace "${platform_namespace}" get deployment/bosun-gateway \
    --output=jsonpath='{.spec.replicas}'
)"
if [[ ! "${api_replicas}" =~ ^[1-9][0-9]*$ ]]; then
  die "bosun-api must have at least one desired replica before reset"
fi
if [[ ! "${gateway_replicas}" =~ ^[1-9][0-9]*$ ]]; then
  die "bosun-gateway must have at least one desired replica before reset"
fi

kube --namespace "${platform_namespace}" rollout status deployment/bosun-operator \
  --timeout="${rollout_timeout}"
kube --namespace "${platform_namespace}" rollout status statefulset/bosun-postgresql \
  --timeout="${rollout_timeout}"

declare -a user_namespaces=()
namespace_inventory=""
if ! namespace_inventory="$(
  kube get namespaces --output=go-template='{{range .items}}{{.metadata.name}}{{"|"}}{{index .metadata.labels "app.kubernetes.io/managed-by"}}{{"|"}}{{index .metadata.labels "bosun.io/user"}}{{"\n"}}{{end}}'
)"; then
  die "could not inventory user namespaces"
fi
if [[ -n "${namespace_inventory}" ]]; then
  while IFS='|' read -r namespace managed_by user_id; do
    if [[ ! "${namespace}" =~ ^bosun-u-[a-z0-9]{8,16}$ ]]; then
      continue
    fi
    if [[ "${managed_by}" != "bosun" || -z "${user_id}" || "${user_id}" == "<no value>" ]]; then
      die "user namespace ${namespace} is missing Bosun ownership labels; refusing automatic deletion"
    fi
    user_namespaces+=("${namespace}")
  done <<<"${namespace_inventory}"
fi

declare -A workspace_pvs=()

add_workspace_pv() {
  local pv_name="$1"
  local reclaim_policy
  if [[ -z "${pv_name}" || "${pv_name}" == "<no value>" ]]; then
    return
  fi
  if [[ -n "${workspace_pvs[${pv_name}]:-}" ]]; then
    return
  fi
  if ! reclaim_policy="$(
    kube get persistentvolume "${pv_name}" \
      --output=jsonpath='{.spec.persistentVolumeReclaimPolicy}'
  )"; then
    die "could not inspect PersistentVolume ${pv_name}"
  fi
  if [[ "${reclaim_policy}" != "Delete" ]]; then
    die "PersistentVolume ${pv_name} uses reclaim policy ${reclaim_policy}; physical data deletion is not guaranteed"
  fi
  workspace_pvs["${pv_name}"]=1
}

pvc_inventory=""
if ! pvc_inventory="$(
  kube get persistentvolumeclaims --all-namespaces --selector bosun.io/session \
    --output=go-template='{{range .items}}{{.metadata.name}}{{"|"}}{{.spec.volumeName}}{{"\n"}}{{end}}'
)"; then
  die "could not inventory agent PersistentVolumeClaims"
fi
if [[ -n "${pvc_inventory}" ]]; then
  while IFS='|' read -r pvc_name pv_name; do
    if [[ -n "${pvc_name}" ]]; then
      add_workspace_pv "${pv_name}"
    fi
  done <<<"${pvc_inventory}"
fi

pv_inventory=""
if ! pv_inventory="$(
  kube get persistentvolumes \
    --output=go-template='{{range .items}}{{.metadata.name}}{{"|"}}{{.spec.claimRef.namespace}}{{"|"}}{{.spec.persistentVolumeReclaimPolicy}}{{"\n"}}{{end}}'
)"; then
  die "could not inventory PersistentVolumes"
fi
if [[ -n "${pv_inventory}" ]]; then
  while IFS='|' read -r pv_name claim_namespace reclaim_policy; do
    if [[ ! "${claim_namespace}" =~ ^bosun-u-[a-z0-9]{8,16}$ ]]; then
      continue
    fi
    if [[ "${reclaim_policy}" != "Delete" ]]; then
      die "PersistentVolume ${pv_name} uses reclaim policy ${reclaim_policy}; physical data deletion is not guaranteed"
    fi
    workspace_pvs["${pv_name}"]=1
  done <<<"${pv_inventory}"
fi

session_count="$(object_count "AgentSession resources" get agentsessions.bosun.io --all-namespaces)"
environment_count="$(object_count "UserEnvironment resources" get userenvironments.bosun.io)"
agent_pod_count="$(object_count "agent Pods" get pods --all-namespaces --selector bosun.io/session)"
agent_pvc_count="$(object_count "agent PersistentVolumeClaims" get persistentvolumeclaims --all-namespaces --selector bosun.io/session)"

echo "Bosun data reset target:"
echo "  context: ${current_context}"
echo "  platform namespace: ${platform_namespace}"
echo "  UserEnvironments: ${environment_count}"
echo "  user namespaces: ${#user_namespaces[@]}"
echo "  AgentSessions: ${session_count}"
echo "  agent Pods: ${agent_pod_count}"
echo "  agent PVCs: ${agent_pvc_count}"
echo "  workspace PVs: ${#workspace_pvs[@]}"
echo "  PostgreSQL business schema: bosun"

if [[ -z "${confirmation}" && -t 0 ]]; then
  read -r -p "Type '${confirmation_phrase}' to continue: " confirmation
fi
if [[ "${confirmation}" != "${confirmation_phrase}" ]]; then
  die "set RESET_CONFIRM='${confirmation_phrase}' or enter the exact phrase interactively"
fi

echo "stopping API and gateway"
platform_stopped=true
kube --namespace "${platform_namespace}" scale deployment/bosun-api --replicas=0
kube --namespace "${platform_namespace}" scale deployment/bosun-gateway --replicas=0
wait_for_no_pods app.kubernetes.io/name=bosun-api
wait_for_no_pods app.kubernetes.io/name=bosun-gateway

if ((session_count > 0)); then
  echo "deleting AgentSessions and waiting for Operator cleanup"
  kube delete agentsessions.bosun.io --all --all-namespaces \
    --wait=true --timeout="${delete_timeout}"
fi

if resources_exist "orphaned agent Pods" \
  get pods --all-namespaces --selector bosun.io/session; then
  echo "deleting orphaned agent Pods"
  kube delete pods --all-namespaces --selector bosun.io/session \
    --wait=true --timeout="${delete_timeout}"
fi
if resources_exist "orphaned agent ServiceAccounts" \
  get serviceaccounts --all-namespaces --selector bosun.io/session; then
  echo "deleting orphaned agent ServiceAccounts"
  kube delete serviceaccounts --all-namespaces --selector bosun.io/session \
    --wait=true --timeout="${delete_timeout}"
fi
if resources_exist "orphaned agent PersistentVolumeClaims" \
  get persistentvolumeclaims --all-namespaces --selector bosun.io/session; then
  echo "deleting orphaned agent PVCs"
  kube delete persistentvolumeclaims --all-namespaces --selector bosun.io/session \
    --wait=true --timeout="${delete_timeout}"
fi

if ((environment_count > 0)); then
  echo "deleting UserEnvironments and their owned resources"
  kube delete userenvironments.bosun.io --all --cascade=foreground \
    --wait=true --timeout="${delete_timeout}"
fi

for namespace in "${user_namespaces[@]}"; do
  if resources_exist "user namespace ${namespace}" \
    get namespace "${namespace}" --ignore-not-found; then
    echo "deleting orphaned user namespace ${namespace}"
    kube delete namespace "${namespace}" --wait=true --timeout="${delete_timeout}"
  fi
done

for pv_name in "${!workspace_pvs[@]}"; do
  wait_for_resource_deleted persistentvolume "${pv_name}"
done

echo "resetting PostgreSQL business schema"
kube --namespace "${platform_namespace}" exec --stdin statefulset/bosun-postgresql -- \
  psql -v ON_ERROR_STOP=1 -U bosun -d bosun <<'SQL'
DROP SCHEMA IF EXISTS bosun CASCADE;
DROP TABLE IF EXISTS public.schema_migrations;
SQL

echo "starting API and applying embedded migrations"
kube --namespace "${platform_namespace}" scale deployment/bosun-api \
  --replicas="${api_replicas}"
kube --namespace "${platform_namespace}" rollout status deployment/bosun-api \
  --timeout="${rollout_timeout}"

kube --namespace "${platform_namespace}" exec --stdin statefulset/bosun-postgresql -- \
  psql -v ON_ERROR_STOP=1 -U bosun -d bosun <<'SQL'
DO $reset$
DECLARE
  table_record record;
  has_rows boolean;
BEGIN
  FOR table_record IN
    SELECT schemaname, tablename
    FROM pg_tables
    WHERE schemaname = 'bosun'
  LOOP
    EXECUTE format(
      'SELECT EXISTS (SELECT 1 FROM %I.%I)',
      table_record.schemaname,
      table_record.tablename
    ) INTO has_rows;
    IF has_rows THEN
      RAISE EXCEPTION 'table %.% is not empty after reset',
        table_record.schemaname,
        table_record.tablename;
    END IF;
  END LOOP;
END
$reset$;
SQL

echo "starting gateway"
kube --namespace "${platform_namespace}" scale deployment/bosun-gateway \
  --replicas="${gateway_replicas}"
kube --namespace "${platform_namespace}" rollout status deployment/bosun-gateway \
  --timeout="${rollout_timeout}"

ensure_no_resources "AgentSession resources" \
  get agentsessions.bosun.io --all-namespaces
ensure_no_resources "UserEnvironment resources" \
  get userenvironments.bosun.io
ensure_no_resources "agent Pods" \
  get pods --all-namespaces --selector bosun.io/session
ensure_no_resources "agent ServiceAccounts" \
  get serviceaccounts --all-namespaces --selector bosun.io/session
ensure_no_resources "agent PersistentVolumeClaims" \
  get persistentvolumeclaims --all-namespaces --selector bosun.io/session

remaining_namespaces=""
if ! remaining_namespaces="$(
  kube get namespaces --output=go-template='{{range .items}}{{.metadata.name}}{{"\n"}}{{end}}'
)"; then
  die "could not verify that user namespaces were deleted"
fi
if [[ -n "${remaining_namespaces}" ]]; then
  while IFS= read -r namespace; do
    if [[ "${namespace}" =~ ^bosun-u-[a-z0-9]{8,16}$ ]]; then
      die "user namespace ${namespace} remains after reset"
    fi
  done <<<"${remaining_namespaces}"
fi

platform_stopped=false
echo "Bosun data reset completed successfully"
