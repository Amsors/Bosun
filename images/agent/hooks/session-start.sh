#!/usr/bin/env bash
set -euo pipefail

jq -e '.hook_event_name == "SessionStart"' >/dev/null
test "${ANTHROPIC_BASE_URL:-}" = "http://127.0.0.1:8080"
test -d /workspace
test -w /workspace
