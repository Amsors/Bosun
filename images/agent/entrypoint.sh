#!/usr/bin/env bash
set -euo pipefail

if [[ "${1:-}" == "--smoke-test" ]]; then
  test "$(id -u)" -ne 0
  test "$(node --version)" = "v24.14.0"
  test "$(claude --version | awk '{print $1}')" = "${CLAUDE_CODE_VERSION}"
  tmux -V
  exit 0
fi

exec "$@"
