#!/usr/bin/env bash
# Run scripts/install.sh against a release served from a local directory laid
# out like a real GitHub release: a tarball per platform, and a checksums.txt
# that also lists each tarball's SBOM, which the installer never downloads.
# The installer must install from a matching release and must refuse, without
# installing anything, a tarball whose hash does not match or is not listed.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK="$(mktemp -d)"
SERVER_PID=""

cleanup() {
  if [ -n "${SERVER_PID}" ]; then
    kill "${SERVER_PID}" 2>/dev/null || true
  fi
  if [ -n "${E2E_KEEP:-}" ]; then
    echo "E2E_KEEP set; install artifacts kept at ${WORK}" >&2
    return
  fi
  rm -rf "${WORK}"
}
trap cleanup EXIT

fail() {
  echo "INSTALL E2E FAIL: $*" >&2
  exit 1
}

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

case "$(uname -s)" in
  Linux) OS=linux ;;
  Darwin) OS=darwin ;;
  *) fail "unsupported OS $(uname -s)" ;;
esac
case "$(uname -m)" in
  x86_64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) fail "unsupported architecture $(uname -m)" ;;
esac
TARBALL="shinyhub_${OS}_${ARCH}.tar.gz"

# release <version> <checksums mode>: build a release directory under the
# served root. Mode "match" lists the tarball's real hash, "tampered" lists a
# different one, "unlisted" omits the tarball entirely.
release() {
  local version="$1" mode="$2" dir="${WORK}/srv/${1}" stage="${WORK}/stage-${1}"
  mkdir -p "${dir}" "${stage}"
  printf '#!/bin/sh\necho "fixture shinyhub %s"\n' "${version}" > "${stage}/shinyhub"
  chmod 755 "${stage}/shinyhub"
  tar -czf "${dir}/${TARBALL}" -C "${stage}" shinyhub
  printf '{"spdxVersion":"SPDX-2.3"}\n' > "${dir}/${TARBALL}.sbom.spdx.json"

  local hash
  hash="$(sha256 "${dir}/${TARBALL}")"
  if [ "${mode}" = tampered ]; then
    hash="$(printf '%s' "${hash}" | tr '0-9a-f' '1-9a-f0')"
  fi
  {
    if [ "${mode}" != unlisted ]; then
      printf '%s  %s\n' "${hash}" "${TARBALL}"
    fi
    printf '%s  %s\n' "$(sha256 "${dir}/${TARBALL}.sbom.spdx.json")" "${TARBALL}.sbom.spdx.json"
  } > "${dir}/checksums.txt"
}

release v9.9.1 match
release v9.9.2 tampered
release v9.9.3 unlisted

python3 - "${WORK}/srv" "${WORK}/port" <<'PY' &
import functools, http.server, os, sys

class Quiet(http.server.SimpleHTTPRequestHandler):
    def log_message(self, *args):
        pass

handler = functools.partial(Quiet, directory=sys.argv[1])
server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), handler)
with open(sys.argv[2] + ".tmp", "w") as f:
    f.write(str(server.server_address[1]))
os.rename(sys.argv[2] + ".tmp", sys.argv[2])
server.serve_forever()
PY
SERVER_PID=$!
for _ in $(seq 1 100); do
  [ -s "${WORK}/port" ] && break
  sleep 0.05
done
[ -s "${WORK}/port" ] || fail "fixture server did not start"
BASE="http://127.0.0.1:$(cat "${WORK}/port")"

# install <version>: run the installer into a fresh directory, recording its
# exit status and output.
install() {
  local version="$1"
  INSTALL_TO="${WORK}/bin-${version}"
  mkdir -p "${INSTALL_TO}"
  set +e
  SHINYHUB_VERSION="${version}" SHINYHUB_DOWNLOAD_URL="${BASE}" INSTALL_DIR="${INSTALL_TO}" \
    sh "${ROOT}/scripts/install.sh" > "${WORK}/out-${version}" 2>&1
  INSTALL_STATUS=$?
  set -e
}

install v9.9.1
[ "${INSTALL_STATUS}" -eq 0 ] || fail "a matching release did not install (exit ${INSTALL_STATUS}):
$(cat "${WORK}/out-v9.9.1")"
[ "$("${INSTALL_TO}/shinyhub")" = "fixture shinyhub v9.9.1" ] || fail "installed binary is not the release's"

install v9.9.2
[ "${INSTALL_STATUS}" -ne 0 ] || fail "a tarball whose hash does not match was installed"
[ ! -e "${INSTALL_TO}/shinyhub" ] || fail "a tarball whose hash does not match left a binary behind"
grep -q "FAILED" "${WORK}/out-v9.9.2" \
  || fail "a mismatched tarball was refused by something other than the checksum:
$(cat "${WORK}/out-v9.9.2")"

install v9.9.3
[ "${INSTALL_STATUS}" -ne 0 ] || fail "a tarball missing from checksums.txt was installed"
[ ! -e "${INSTALL_TO}/shinyhub" ] || fail "a tarball missing from checksums.txt left a binary behind"
grep -q "no checksum for ${TARBALL}" "${WORK}/out-v9.9.3" \
  || fail "an unlisted tarball was refused without saying why:
$(cat "${WORK}/out-v9.9.3")"

echo "install e2e: ok (${TARBALL})"
