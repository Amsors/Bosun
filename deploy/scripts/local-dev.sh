#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cluster_name="bosun"
cluster_context="k3d-${cluster_name}"
registry_name="bosun-registry"
registry_container="k3d-${registry_name}"
registry_port="${BOSUN_DEV_REGISTRY_PORT:-5001}"
host_registry="127.0.0.1:${registry_port}/amsors"
cluster_registry="${registry_container}:5000/amsors"
platform_namespace="bosun-platform"

require_commands() {
  local command
  for command in "$@"; do
    if ! command -v "${command}" >/dev/null 2>&1; then
      echo "required command is unavailable: ${command}" >&2
      exit 1
    fi
  done
}

image_tag() {
  local tag="${BOSUN_DEV_IMAGE_TAG:-}"
  if [[ -z "${tag}" ]]; then
    tag="$(git -C "${root}" rev-parse --short=7 HEAD)"
  fi
  if [[ ! "${tag}" =~ ^[0-9a-f]{7}$ ]]; then
    echo "BOSUN_DEV_IMAGE_TAG must be a seven-character lowercase hexadecimal git SHA" >&2
    exit 1
  fi
  printf '%s' "${tag}"
}

provider_config() {
  : "${BOSUN_DEV_PROVIDER_URL:?must set BOSUN_DEV_PROVIDER_URL}"
  : "${BOSUN_DEV_PROVIDER_API_KEY:?must set BOSUN_DEV_PROVIDER_API_KEY}"

  local provider_name="${BOSUN_DEV_PROVIDER_NAME:-platform-default}"
  if [[ ! "${provider_name}" =~ ^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$ ]]; then
    echo "BOSUN_DEV_PROVIDER_NAME must be a lowercase DNS label (for example: ds-test-zfy)" >&2
    exit 1
  fi

  local header="${BOSUN_DEV_PROVIDER_AUTH_HEADER:-x-api-key}"
  local scheme="${BOSUN_DEV_PROVIDER_AUTH_SCHEME:-}"
  if [[ "${header,,}" == "x-api-key" && -n "${scheme}" ]]; then
    echo "BOSUN_DEV_PROVIDER_AUTH_SCHEME must be empty when the auth header is X-Api-Key" >&2
    exit 1
  fi
}

# k3d 是否登记了同名集群（含上次创建到一半、容器还在的残缺集群）。
cluster_present() {
  k3d cluster list "${cluster_name}" >/dev/null 2>&1
}

# 集群是否真正可用：k3d 已登记、context 已写入 kubeconfig 且节点可达。
# 只有 cluster_present 为真、容器却处于 created 未启动、context 从未写入时，
# 本函数会返回假，从而触发重建而非盲目复用。
cluster_ready() {
  cluster_present &&
    kubectl config get-contexts "${cluster_context}" >/dev/null 2>&1 &&
    kubectl --context "${cluster_context}" get nodes --request-timeout=5s >/dev/null 2>&1
}

ensure_local_context() {
  local current_context
  current_context="$(kubectl config current-context 2>/dev/null || true)"
  if [[ "${current_context}" != "${cluster_context}" ]]; then
    echo "refusing to operate on context ${current_context:-<none>}; expected ${cluster_context}" >&2
    exit 1
  fi
}

ensure_registry() {
  if ! docker inspect "${registry_container}" >/dev/null 2>&1; then
    k3d registry create "${registry_name}" --port "127.0.0.1:${registry_port}"
  fi

  local _
  for _ in {1..30}; do
    if curl --fail --silent "http://127.0.0.1:${registry_port}/v2/" >/dev/null; then
      return
    fi
    sleep 1
  done
  echo "local registry did not become ready on port ${registry_port}" >&2
  exit 1
}

create_cluster() {
  ensure_registry
  # 仅复用健康集群；上次创建被打断会留下 created 状态容器却没有 context，
  # 这种残缺集群必须先删除再重建，否则跳过创建后 use-context 必然失败。
  if ! cluster_ready; then
    if cluster_present; then
      echo "removing incomplete '${cluster_name}' cluster before recreating" >&2
      k3d cluster delete "${cluster_name}"
    fi
    k3d cluster create --config "${root}/deploy/local/k3d.yaml"
  fi
  # 无论新建还是复用都刷新并切换 context，不依赖 create 时是否写入 kubeconfig。
  k3d kubeconfig merge "${cluster_name}" --kubeconfig-merge-default --kubeconfig-switch-context >/dev/null
  ensure_local_context
  kubectl wait --for=condition=Ready nodes --all --timeout=180s
  kubectl label node "k3d-${cluster_name}-server-0" region=sg role=core --overwrite
  kubectl label node "k3d-${cluster_name}-agent-0" region=hk role=worker --overwrite
  kubectl label node "k3d-${cluster_name}-agent-1" region=hk role=edge --overwrite
  kubectl taint node "k3d-${cluster_name}-agent-1" bosun.io/edge=true:NoSchedule --overwrite
}

image_name() {
  case "$1" in
    api) printf '%s' "backend-api" ;;
    gateway | operator | frontend | agent) printf '%s' "$1" ;;
    egress-proxy) printf '%s' "egress-proxy" ;;
    *)
      echo "unsupported component: $1" >&2
      exit 2
      ;;
  esac
}

remove_component_images() {
  local component="$1"
  local name
  local repository
  local images=()
  name="$(image_name "${component}")"
  repository="${host_registry}/${name}"

  mapfile -t images < <(
    docker image ls \
      --filter "reference=${repository}:*" \
      --format '{{.Repository}}:{{.Tag}}'
  )
  if [[ ${#images[@]} -eq 0 ]]; then
    return
  fi

  echo "removing previous ${component} images: ${images[*]}"
  docker image rm "${images[@]}"
}

build_component() {
  local component="$1"
  local name
  local tag
  name="$(image_name "${component}")"
  tag="$(image_tag)"
  local image="${host_registry}/${name}:${tag}"

  remove_component_images "${component}"

  case "${component}" in
    api | gateway)
      docker build \
        --build-arg "COMPONENT=${component}" \
        --file "${root}/backend/Dockerfile" \
        --tag "${image}" \
        "${root}"
      ;;
    operator)
      docker build --file "${root}/operator/Dockerfile" --tag "${image}" "${root}/operator"
      ;;
    frontend)
      docker build --file "${root}/frontend/Dockerfile" --tag "${image}" "${root}/frontend"
      ;;
    agent)
      docker build --file "${root}/images/agent/Dockerfile" --tag "${image}" "${root}/images/agent"
      ;;
    egress-proxy)
      docker build \
        --file "${root}/images/egress-proxy/Dockerfile" \
        --tag "${image}" \
        "${root}/images/egress-proxy"
      ;;
  esac
  docker push "${image}"
}

build_all() {
  local component
  for component in api gateway operator frontend agent egress-proxy; do
    build_component "${component}"
  done
}

restart_component() {
  local component="$1"
  local deployment
  local container
  local image
  case "${component}" in
    api | gateway | operator | frontend)
      deployment="bosun-${component}"
      container="${component}"
      ;;
    egress-proxy)
      deployment="bosun-egress-proxy"
      container="squid"
      ;;
    agent)
      deployment="bosun-operator"
      image="${cluster_registry}/$(image_name "${component}"):$(image_tag)"
      echo "existing agent Pods keep their current image; create a new test session to use the rebuilt agent image"
      kubectl --namespace "${platform_namespace}" patch "deployment/${deployment}" \
        --type=json \
        --patch="[{\"op\":\"replace\",\"path\":\"/spec/template/spec/containers/0/args/2\",\"value\":\"--agent-image=${image}\"}]"
      kubectl --namespace "${platform_namespace}" rollout status "deployment/${deployment}" --timeout=180s
      return
      ;;
  esac

  image="${cluster_registry}/$(image_name "${component}"):$(image_tag)"
  kubectl --namespace "${platform_namespace}" set image \
    "deployment/${deployment}" \
    "${container}=${image}"
  if [[ "${component}" == "operator" ]]; then
    kubectl --namespace "${platform_namespace}" patch "deployment/${deployment}" \
      --type=json \
      --patch='[{"op":"replace","path":"/spec/template/spec/containers/0/args/7","value":"--idle-scan-interval=5s"}]'
  fi
  # Uncommitted local changes reuse the current Git SHA tag. Restart even when
  # the image reference is unchanged so imagePullPolicy=Always fetches the new digest.
  kubectl --namespace "${platform_namespace}" rollout restart "deployment/${deployment}"
  kubectl --namespace "${platform_namespace}" rollout status "deployment/${deployment}" --timeout=180s
}

ensure_platform_secrets() (
  provider_config
  local secret_dir
  secret_dir="$(mktemp -d)"
  trap 'rm -rf "${secret_dir}"' EXIT
  chmod 700 "${secret_dir}"

  kubectl create namespace "${platform_namespace}" --dry-run=client --output=yaml |
    kubectl apply --filename - >/dev/null

  if ! kubectl --namespace "${platform_namespace}" get secret bosun-database >/dev/null 2>&1; then
    local database_password
    database_password="$(openssl rand -hex 24)"
    printf '%s' "${database_password}" > "${secret_dir}/password"
    printf 'postgres://bosun:%s@bosun-postgresql:5432/bosun?sslmode=disable' \
      "${database_password}" > "${secret_dir}/url"
    kubectl --namespace "${platform_namespace}" create secret generic bosun-database \
      --from-file="password=${secret_dir}/password" \
      --from-file="url=${secret_dir}/url"
  fi

  if ! kubectl --namespace "${platform_namespace}" get secret bosun-jwt >/dev/null 2>&1; then
    openssl genpkey -algorithm ED25519 -out "${secret_dir}/private-key" 2>/dev/null
    kubectl --namespace "${platform_namespace}" create secret generic bosun-jwt \
      --from-file="private-key=${secret_dir}/private-key"
  fi

  printf '%s' "${BOSUN_DEV_PROVIDER_API_KEY}" > "${secret_dir}/api-key"
  kubectl --namespace "${platform_namespace}" create secret generic bosun-default-provider \
    --from-file="api-key=${secret_dir}/api-key" \
    --dry-run=client \
    --output=yaml |
    kubectl apply --filename - >/dev/null
)

# 校验本地 registry 已含本次 tag 的全部应用镜像，缺失则立即失败，
# 避免 helm --wait 在 ImagePullBackOff 上空等到 --timeout。dev-reset / dev-deploy
# 不构建镜像，未先跑过 dev-up / dev-build 时会在此明确报错而非空等。
ensure_images_present() {
  local tag component name
  local missing=()
  tag="$(image_tag)"
  for component in api gateway operator frontend agent egress-proxy; do
    name="$(image_name "${component}")"
    if ! curl --fail --silent "http://127.0.0.1:${registry_port}/v2/amsors/${name}/tags/list" 2>/dev/null |
      grep -q "\"${tag}\""; then
      missing+=("amsors/${name}:${tag}")
    fi
  done
  if [[ ${#missing[@]} -gt 0 ]]; then
    echo "local registry lacks images for tag ${tag}: ${missing[*]}" >&2
    echo "run 'make dev-up' or 'make dev-build COMPONENT=all' to build images first" >&2
    exit 1
  fi
}

deploy_chart() {
  ensure_local_context
  ensure_images_present
  ensure_platform_secrets
  # Helm installs files under crds/ only on the first release and deliberately
  # skips CRD upgrades. Apply them explicitly so local schema changes are not
  # left behind when application images are rebuilt.
  kubectl apply --filename "${root}/deploy/chart/crds" >/dev/null
  local tag
  tag="$(image_tag)"
  local provider_name="${BOSUN_DEV_PROVIDER_NAME:-platform-default}"
  local provider_header="${BOSUN_DEV_PROVIDER_AUTH_HEADER:-x-api-key}"
  local provider_scheme="${BOSUN_DEV_PROVIDER_AUTH_SCHEME:-}"
  local helm_conflict_args=()
  # Helm 4 uses server-side apply. Component builds intentionally use
  # kubectl set/patch for fast local rollouts, so Helm must reclaim those
  # chart-owned fields on the next full deployment. Keep Helm 3 compatible.
  if helm upgrade --help | grep -q -- '--force-conflicts'; then
    helm_conflict_args+=(--force-conflicts)
  fi

  "${root}/deploy/scripts/apply-crds.sh"
  echo "installing Bosun and waiting up to 10m for all workloads to become ready"
  helm upgrade --install bosun "${root}/deploy/chart" \
    --namespace "${platform_namespace}" \
    --create-namespace \
    --values "${root}/deploy/local/values-local.yaml" \
    --set-string "global.registry=${cluster_registry}" \
    --set-string "global.imageTag=${tag}" \
    --set-string "gateway.provider=${provider_name}" \
    --set-string "gateway.upstreamURL=${BOSUN_DEV_PROVIDER_URL}" \
    --set-string "gateway.upstreamAuthHeader=${provider_header}" \
    --set-string "gateway.upstreamAuthScheme=${provider_scheme}" \
    "${helm_conflict_args[@]}" \
    --rollback-on-failure \
    --wait \
    --timeout 10m
}

forward_frontend() {
  ensure_local_context
  echo "Bosun is available at http://127.0.0.1:18080; press Ctrl-C to stop forwarding"
  trap 'echo "port-forward stopped by user"; exit 130' INT TERM

  local retry_count=0
  local exit_code
  while true; do
    if [[ ${retry_count} -gt 0 ]]; then
      echo "starting port-forward retry #${retry_count}" >&2
    fi
    if kubectl --namespace "${platform_namespace}" port-forward service/bosun-frontend 18080:8080; then
      exit_code=0
    else
      exit_code=$?
    fi
    retry_count=$((retry_count + 1))
    echo "port-forward exited with code ${exit_code}; retry #${retry_count} in 2s" >&2
    sleep 2
  done
}

run_smoke() (
  : "${BOSUN_E2E_PASSWORD:?must set BOSUN_E2E_PASSWORD}"
  require_commands curl jq uuidgen sha256sum
  ensure_local_context

  local forward_log
  local smoke_a_output
  forward_log="$(mktemp)"
  kubectl --namespace "${platform_namespace}" port-forward service/bosun-frontend 18080:8080 \
    >"${forward_log}" 2>&1 &
  local forward_pid=$!
  trap 'kill "${forward_pid}" >/dev/null 2>&1 || true; rm -f "${forward_log}"' EXIT

  local _
  for _ in {1..30}; do
    if curl --fail --silent http://127.0.0.1:18080/healthz >/dev/null; then
      break
    fi
    if ! kill -0 "${forward_pid}" 2>/dev/null; then
      cat "${forward_log}" >&2
      return 1
    fi
    sleep 1
  done
  curl --fail --silent http://127.0.0.1:18080/healthz >/dev/null

  smoke_a_output="$(
    BOSUN_BASE_URL=http://127.0.0.1:18080 \
      BOSUN_E2E_PASSWORD="${BOSUN_E2E_PASSWORD}" \
      "${root}/e2e/smoke-a.sh"
  )"
  printf '%s\n' "${smoke_a_output}"
  BOSUN_BASE_URL=http://127.0.0.1:18080 \
    BOSUN_E2E_ACCESS_TOKEN="$(jq -r '.accessToken' <<<"${smoke_a_output}")" \
    "${root}/e2e/smoke-b.sh"
)

usage() {
  cat <<'EOF'
usage: local-dev.sh <up|build|deploy|forward|smoke|reset|down> [component]

components: api, gateway, operator, frontend, agent, egress-proxy, all
EOF
}

main() {
  local action="${1:-}"
  require_commands docker k3d kubectl helm openssl git curl

  case "${action}" in
    up)
      provider_config
      create_cluster
      build_all
      deploy_chart
      ;;
    build)
      local component="${2:-all}"
      ensure_registry
      if ! cluster_ready; then
        echo "local cluster is not ready; run make dev-up first" >&2
        exit 1
      fi
      kubectl config use-context "${cluster_context}" >/dev/null
      ensure_local_context
      if [[ "${component}" == "all" ]]; then
        build_all
        deploy_chart
        kubectl --namespace "${platform_namespace}" rollout restart deployment
        kubectl --namespace "${platform_namespace}" rollout status deployment --timeout=180s
      else
        build_component "${component}"
        restart_component "${component}"
      fi
      ;;
    deploy)
      deploy_chart
      kubectl --namespace "${platform_namespace}" rollout restart deployment/bosun-gateway
      kubectl --namespace "${platform_namespace}" rollout status deployment/bosun-gateway --timeout=180s
      ;;
    forward)
      forward_frontend
      ;;
    smoke)
      run_smoke
      ;;
    reset)
      provider_config
      # 按集群名删除即可，不切 kubectl context：残缺集群 context 可能尚未写入。
      if cluster_present; then
        k3d cluster delete "${cluster_name}"
      fi
      create_cluster
      deploy_chart
      ;;
    down)
      # 同 reset：k3d cluster delete 按集群名精确删除，不依赖 kubectl context。
      if cluster_present; then
        k3d cluster delete "${cluster_name}"
      fi
      if docker inspect "${registry_container}" >/dev/null 2>&1; then
        k3d registry delete "${registry_name}"
      fi
      ;;
    *)
      usage >&2
      exit 2
      ;;
  esac
}

main "$@"
