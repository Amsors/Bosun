#!/usr/bin/env bash
set -euo pipefail

image="${1:-bosun-agent:smoke}"
docker build --tag "${image}" "$(dirname "$0")"
docker run --rm --read-only --tmpfs /tmp:rw,noexec,nosuid,size=64m "${image}" --smoke-test
