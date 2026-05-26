#!/usr/bin/env bash
# Poll a log file until it contains a substring, or time out.
# Usage: wait-log.sh <file> <needle> <timeout-seconds>
set -euo pipefail

file="${1:?usage: wait-log.sh <file> <needle> <timeout-seconds>}"
needle="${2:?usage: wait-log.sh <file> <needle> <timeout-seconds>}"
timeout="${3:?usage: wait-log.sh <file> <needle> <timeout-seconds>}"

deadline=$(( $(date +%s) + timeout ))
while [ "$(date +%s)" -lt "${deadline}" ]; do
  if [ -f "${file}" ] && grep -qF "${needle}" "${file}"; then
    exit 0
  fi
  sleep 0.5
done
echo "wait-log: timed out after ${timeout}s waiting for \"${needle}\" in ${file}" >&2
exit 1
