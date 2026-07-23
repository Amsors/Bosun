#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
reset_script="${script_dir}/reset-data.sh"
fake_bin="${script_dir}/testdata/reset-data"
test_root="$(mktemp -d "${TMPDIR:-/tmp}/bosun-reset-data-test.XXXXXX")"

cleanup() {
  rm -rf "${test_root}"
}
trap cleanup EXIT

assert_contains() {
  local output="$1"
  local expected="$2"
  if [[ "${output}" != *"${expected}"* ]]; then
    echo "expected output to contain: ${expected}" >&2
    echo "${output}" >&2
    exit 1
  fi
}

run_case() {
  local scenario="$1"
  local expected_status="$2"
  local expected_output="$3"
  local case_state="${test_root}/${scenario}"
  local output
  local status

  mkdir -p "${case_state}"
  set +e
  output="$(
    PATH="${fake_bin}:${PATH}" \
      FAKE_KUBECTL_SCENARIO="${scenario}" \
      FAKE_KUBECTL_STATE_DIR="${case_state}" \
      RESET_CONTEXT="test-context" \
      RESET_CONFIRM="DELETE ALL BOSUN DATA" \
      "${reset_script}" 2>&1
  )"
  status=$?
  set -e

  if [[ "${status}" -ne "${expected_status}" ]]; then
    echo "${scenario}: exit status ${status}, expected ${expected_status}" >&2
    echo "${output}" >&2
    exit 1
  fi
  assert_contains "${output}" "${expected_output}"
}

run_case happy 0 "Bosun data reset completed successfully"
run_case fail-namespace-inventory 1 "could not inventory user namespaces"
run_case fail-session-inventory 1 "could not count AgentSession resources"
run_case fail-pod-wait 1 "could not verify whether Pods matching app.kubernetes.io/name=bosun-api have stopped"
run_case fail-pv-wait 1 "could not verify whether persistentvolume/pv-workspace was deleted"
run_case fail-final-verification 1 "could not verify that AgentSession resources were deleted"

echo "reset-data tests passed"
