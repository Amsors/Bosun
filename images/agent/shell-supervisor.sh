#!/usr/bin/env bash
set -uo pipefail

trap 'exit 0' HUP TERM

while true; do
  /bin/bash
  status=$?
  printf '\r\n[Bosun] Shell exited with status %d; starting a new shell.\r\n' "${status}"
  sleep 0.2
done
