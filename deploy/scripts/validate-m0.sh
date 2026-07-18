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

helm lint "${root}/deploy/chart"
helm template bosun "${root}/deploy/chart" --namespace bosun-platform --include-crds >/dev/null

generated="$(mktemp -d)"
trap 'rm -rf "${generated}"' EXIT
cp -R "${root}/operator/." "${generated}/operator"
(cd "${generated}/operator" && make manifests generate >/dev/null)
diff -ru "${root}/operator/config/crd/bases" "${generated}/operator/config/crd/bases"
diff -u "${root}/operator/api/v1alpha1/zz_generated.deepcopy.go" \
  "${generated}/operator/api/v1alpha1/zz_generated.deepcopy.go"
