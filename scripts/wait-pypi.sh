#!/usr/bin/env bash
# wait-pypi.sh <version>
#
# Blocks until PyPI's simple index, the API pip and uv resolve against, lists
# every file the release publishes for <version>: one wheel per platform and the
# sdist. PyPI's JSON API can report a version before the simple index serves it,
# so a release is installable only once this returns.
#
#   WAIT_PYPI_TIMEOUT   seconds to wait before giving up (default 900)
#   WAIT_PYPI_INTERVAL  seconds between requests (default 20)
#   WAIT_PYPI_INDEX     project index URL (default https://pypi.org/simple/shinyhub/)
set -euo pipefail

if [ "$#" -ne 1 ] || [ -z "$1" ]; then
  echo "usage: $0 <version>" >&2
  exit 2
fi
VERSION="${1#v}"
TIMEOUT="${WAIT_PYPI_TIMEOUT:-900}"
INTERVAL="${WAIT_PYPI_INTERVAL:-20}"
INDEX="${WAIT_PYPI_INDEX:-https://pypi.org/simple/shinyhub/}"

# The platforms of the release workflow's wheels matrix, plus the sdist.
EXPECTED=(
  "shinyhub-${VERSION}-py3-none-manylinux_2_17_x86_64.whl"
  "shinyhub-${VERSION}-py3-none-manylinux_2_17_aarch64.whl"
  "shinyhub-${VERSION}-py3-none-macosx_11_0_x86_64.whl"
  "shinyhub-${VERSION}-py3-none-macosx_11_0_arm64.whl"
  "shinyhub-${VERSION}.tar.gz"
)

BODY="$(mktemp)"
trap 'rm -f "${BODY}"' EXIT

# missing prints each expected file the index does not list, one per line. A
# body that is not simple-index JSON fails, so it never reads as "all listed".
missing() {
  python3 -I -c '
import json, sys
body, expected = sys.argv[1], sys.argv[2:]
with open(body) as f:
    listed = {entry["filename"] for entry in json.load(f)["files"]}
for name in expected:
    if name not in listed:
        print(name)
' "${BODY}" "${EXPECTED[@]}" 2>/dev/null
}

# remaining prints the seconds left before the deadline, at least 1, so every
# request and pause fits inside WAIT_PYPI_TIMEOUT even when the index stalls.
remaining() {
  local left=$(( deadline - $(date +%s) ))
  [ "${left}" -ge 1 ] || left=1
  echo "${left}"
}

deadline=$(( $(date +%s) + TIMEOUT ))
while :; do
  status="$(curl -sS -o "${BODY}" -w '%{http_code}' --max-time "$(remaining)" \
    -H 'Accept: application/vnd.pypi.simple.v1+json' "${INDEX}")" || status="no response"
  if [ "${status}" = 200 ]; then
    absent="$(missing)" \
      || { echo "wait-pypi: ${INDEX} did not answer with a simple-index JSON body" >&2; exit 1; }
    if [ -z "${absent}" ]; then
      echo "wait-pypi: ${INDEX} lists all ${#EXPECTED[@]} files for ${VERSION}"
      exit 0
    fi
  else
    absent="the index answered: ${status}"
  fi
  if [ "$(date +%s)" -ge "${deadline}" ]; then
    echo "wait-pypi: gave up after ${TIMEOUT}s; ${INDEX} still lacks for ${VERSION}:" >&2
    printf '%s\n' "${absent}" | sed 's/^/  /' >&2
    exit 1
  fi
  pause="$(remaining)"
  [ "${INTERVAL}" -ge "${pause}" ] || pause="${INTERVAL}"
  sleep "${pause}"
done
