#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tools_dir="$(mktemp -d "${TMPDIR:-/tmp}/bosun-ci-tools.XXXXXX")"
postgres_container=""

# 与 .github/workflows/ci.yml 的 setup-go 版本保持一致。
export GOTOOLCHAIN=go1.26.2

cleanup() {
  if [[ -n "${postgres_container}" ]]; then
    docker rm --force "${postgres_container}" >/dev/null 2>&1 || true
  fi
  rm -rf "${tools_dir}"
}
trap cleanup EXIT

step() {
  printf '\n==> %s\n' "$1"
}

require_command() {
  local command_name="$1"

  if ! command -v "${command_name}" >/dev/null 2>&1; then
    echo "缺少 CI 依赖：${command_name}" >&2
    exit 1
  fi
}

start_postgres() {
  local endpoint
  local postgres_port

  if [[ -n "${BOSUN_TEST_DATABASE_URL:-}" ]]; then
    echo "使用 BOSUN_TEST_DATABASE_URL 指定的 PostgreSQL"
    return
  fi

  step "启动 PostgreSQL 测试实例"
  postgres_container="$(
    docker run --detach --rm \
      --env POSTGRES_USER=bosun \
      --env POSTGRES_PASSWORD=bosun \
      --env POSTGRES_DB=bosun \
      --publish 127.0.0.1::5432 \
      postgres:16.10-alpine3.22@sha256:029660641a0cfc575b14f336ba448fb8a75fd595d42e1fa316b9fb4378742297
  )"

  for _ in {1..30}; do
    if docker exec "${postgres_container}" pg_isready -U bosun -d bosun >/dev/null 2>&1; then
      endpoint="$(docker port "${postgres_container}" 5432/tcp)"
      postgres_port="${endpoint##*:}"
      export BOSUN_TEST_DATABASE_URL="postgres://bosun:bosun@127.0.0.1:${postgres_port}/bosun?sslmode=disable"
      return
    fi
    sleep 1
  done

  docker logs "${postgres_container}" >&2
  echo "PostgreSQL 测试实例未能就绪" >&2
  exit 1
}

for command_name in docker git go helm make npm rg shellcheck; do
  require_command "${command_name}"
done

if ! docker info >/dev/null 2>&1; then
  echo "Docker daemon 不可用" >&2
  exit 1
fi

cd "${root}"

step "安装 GitHub CI 固定版本的检查工具"
env GOBIN="${tools_dir}" \
  go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2
env GOBIN="${tools_dir}" \
  go install github.com/rhysd/actionlint/cmd/actionlint@v1.7.12

start_postgres

step "检查 backend"
(
  cd backend
  "${tools_dir}/golangci-lint" run ./...
  go test -p 1 ./...
  go build ./cmd/api ./cmd/gateway
)

step "检查 operator"
(
  cd operator
  "${tools_dir}/golangci-lint" run
  make test
  git diff --exit-code -- config/crd/bases api/v1alpha1/zz_generated.deepcopy.go
)

step "检查 frontend"
(
  cd frontend
  npm ci
  npm run lint
  npm run format:check
  npm test
  npm run build
)

step "检查 agent image"
./images/agent/smoke-test.sh bosun-agent:ci

step "检查 egress image"
docker build --file images/egress-proxy/Dockerfile images/egress-proxy

step "检查交付配置"
"${tools_dir}/actionlint"
./deploy/scripts/validate-delivery.sh
shellcheck -x -P e2e deploy/scripts/*.sh e2e/*.sh
make cluster-reset-data-test

printf '\n所有 CI 检查均已通过。\n'
