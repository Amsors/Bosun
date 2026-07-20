#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

if rg --glob='*.yaml' --glob='Dockerfile' 'image:.*:latest|FROM .*:latest' "${root}"; then
  echo "mutable latest image reference found" >&2
  exit 1
fi

if rg --hidden --glob='!**/.git/**' --glob='!**/node_modules/**' \
  --glob='!deploy/scripts/validate-m0.sh' \
  '(sk-ant-[A-Za-z0-9_-]{16,}|BEGIN (RSA |OPENSSH |EC )?PRIVATE KEY|kubeconfig:)' "${root}"; then
  echo "likely secret material found" >&2
  exit 1
fi

helm lint "${root}/deploy/chart" --kube-version 1.36.0
rendered="$(helm template bosun "${root}/deploy/chart" \
  --namespace bosun-platform \
  --kube-version 1.36.0 \
  --include-crds)"

if ! grep -A4 'resources: \["clusterroles"\]' <<<"${rendered}" |
  grep -A2 'resourceNames: \["bosun-user-backend-terminal"\]' |
  grep -q 'verbs: \["bind"\]'; then
  echo "operator is missing the scoped ClusterRole bind permission" >&2
  exit 1
fi

if ! grep -A2 'resources: \["agentsessions"\]' <<<"${rendered}" |
  grep -q 'verbs: \["patch", "update"\]'; then
  echo "operator is missing AgentSession metadata update permissions" >&2
  exit 1
fi

generated="$(mktemp -d)"
trap 'rm -rf "${generated}"' EXIT
mkdir -p "${generated}/operator"
(cd "${root}/operator" && tar --exclude='./bin' --exclude='./cover.out' -cf - .) |
  (cd "${generated}/operator" && tar -xf -)
(cd "${generated}/operator" && make manifests generate >/dev/null)
diff -ru "${root}/operator/config/crd/bases" "${generated}/operator/config/crd/bases"
diff -u "${root}/operator/api/v1alpha1/zz_generated.deepcopy.go" \
  "${generated}/operator/api/v1alpha1/zz_generated.deepcopy.go"
