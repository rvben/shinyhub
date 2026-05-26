#!/usr/bin/env bash
# Poll an HTTP URL until it returns 2xx, or time out.
# Usage: wait-http.sh <url> <timeout-seconds>
set -euo pipefail

url="${1:?usage: wait-http.sh <url> <timeout-seconds>}"
timeout="${2:?usage: wait-http.sh <url> <timeout-seconds>}"

deadline=$(( $(date +%s) + timeout ))
while [ "$(date +%s)" -lt "${deadline}" ]; do
  if curl -fsS -o /dev/null "${url}" 2>/dev/null; then
    exit 0
  fi
  sleep 0.5
done
echo "wait-http: timed out after ${timeout}s waiting for ${url}" >&2
exit 1
