#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

command -v kubectl >/dev/null 2>&1 || {
  printf 'kubectl is required\n' >&2
  exit 1
}

kubectl apply \
  --field-manager=bosun-deploy \
  --filename "${root}/deploy/chart/crds/bosun.io_agentsessions.yaml"
kubectl wait \
  --for=condition=Established \
  customresourcedefinition/agentsessions.bosun.io \
  --timeout=60s
