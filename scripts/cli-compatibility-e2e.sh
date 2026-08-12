#!/usr/bin/env bash
# Bidirectional release contract: the current CLI against the last shipped
# server, and the last shipped CLI against the current server. The released
# binary is downloaded exactly as users received it and verified against a
# checksum manifest pinned in the repository.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK="$(mktemp -d)"
OLD_PID=""
CURRENT_PID=""
OLD_PORT="${SHINYHUB_COMPAT_OLD_PORT:-18091}"
CURRENT_PORT="${SHINYHUB_COMPAT_CURRENT_PORT:-18092}"
OLD_HOST="http://127.0.0.1:${OLD_PORT}"
CURRENT_HOST="http://127.0.0.1:${CURRENT_PORT}"
TOKEN="compatibility-e2e-token-0123456789abcdef"
ADMIN_PASSWORD="compatibility-e2e-password"

cleanup() {
  for pid in "${CURRENT_PID}" "${OLD_PID}"; do
    if [ -n "${pid}" ]; then
      kill "${pid}" 2>/dev/null || true
      wait "${pid}" 2>/dev/null || true
    fi
  done
  if [ -n "${E2E_KEEP:-}" ]; then
    echo "E2E_KEEP set; compatibility artifacts kept at ${WORK}" >&2
    return
  fi
  rm -rf "${WORK}"
}
trap cleanup EXIT

fail() {
  echo "CLI COMPATIBILITY E2E FAIL: $*" >&2
  for log in old-server.log current-server.log current-connect.log current-doctor.log current-deploy.log current-recovery.log old-login.log old-whoami.log old-deploy.log; do
    if [ -s "${WORK}/${log}" ]; then
      echo "----- ${log} (last 100 lines) -----" >&2
      tail -100 "${WORK}/${log}" >&2
    fi
  done
  exit 1
}

command -v curl >/dev/null 2>&1 || fail "curl is required"
command -v uv >/dev/null 2>&1 || fail "uv is required"

PREVIOUS_VERSION="$(tr -d '[:space:]' < "${ROOT}/testdata/compatibility/previous-release.txt")"
[ -n "${PREVIOUS_VERSION}" ] || fail "previous-release.txt is empty"
CHECKSUMS="${ROOT}/testdata/compatibility/${PREVIOUS_VERSION}-checksums.txt"
[ -f "${CHECKSUMS}" ] || fail "missing pinned checksum manifest ${CHECKSUMS}"

case "$(uname -s)" in
  Linux) RELEASE_OS="linux" ;;
  Darwin) RELEASE_OS="darwin" ;;
  *) fail "unsupported compatibility platform $(uname -s)" ;;
esac
case "$(uname -m)" in
  x86_64|amd64) RELEASE_ARCH="amd64" ;;
  aarch64|arm64) RELEASE_ARCH="arm64" ;;
  *) fail "unsupported compatibility architecture $(uname -m)" ;;
esac

ASSET="shinyhub_${RELEASE_OS}_${RELEASE_ARCH}.tar.gz"
EXPECTED_SHA="$(awk -v asset="${ASSET}" '$2 == asset {print $1}' "${CHECKSUMS}")"
[ -n "${EXPECTED_SHA}" ] || fail "${ASSET} is not pinned in ${CHECKSUMS}"

echo "==> downloading exact released binary ${PREVIOUS_VERSION} (${RELEASE_OS}/${RELEASE_ARCH})"
curl -fLsS "https://github.com/rvben/shinyhub/releases/download/${PREVIOUS_VERSION}/${ASSET}" \
  -o "${WORK}/${ASSET}" || fail "download ${PREVIOUS_VERSION}"
if command -v sha256sum >/dev/null 2>&1; then
  ACTUAL_SHA="$(sha256sum "${WORK}/${ASSET}" | awk '{print $1}')"
elif command -v shasum >/dev/null 2>&1; then
  ACTUAL_SHA="$(shasum -a 256 "${WORK}/${ASSET}" | awk '{print $1}')"
else
  fail "sha256sum or shasum is required to verify the released binary"
fi
[ "${ACTUAL_SHA}" = "${EXPECTED_SHA}" ] || fail "checksum mismatch for ${ASSET}"
mkdir -p "${WORK}/released"
tar -xzf "${WORK}/${ASSET}" -C "${WORK}/released" || fail "extract ${ASSET}"
OLD_BIN="${WORK}/released/shinyhub"
[ -x "${OLD_BIN}" ] || fail "release archive has no executable shinyhub"
"${OLD_BIN}" --version | grep -Fq "${PREVIOUS_VERSION#v}" || fail "released binary version does not match ${PREVIOUS_VERSION}"

CURRENT_VERSION="v$(sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "${ROOT}/package.json" | head -1)"
CURRENT_PROTOCOL="$(sed -n 's/^const CurrentVersion = \([0-9][0-9]*\)$/\1/p' "${ROOT}/internal/protocol/version.go")"
[ -n "${CURRENT_PROTOCOL}" ] || fail "could not read protocol.CurrentVersion"
echo "==> building current CLI/server ${CURRENT_VERSION}"
mkdir -p "${WORK}/current-bin"
GOWORK=off go build -ldflags "-X main.version=${CURRENT_VERSION}" \
  -o "${WORK}/current-bin/shinyhub" "${ROOT}/cmd/shinyhub" || fail "build current binary"
CURRENT_BIN="${WORK}/current-bin/shinyhub"

cp -R "${ROOT}/testdata/e2e-app" "${WORK}/app-current-to-old"
cp -R "${ROOT}/testdata/e2e-app" "${WORK}/app-old-to-current"
printf '%s\n' "${TOKEN}" > "${WORK}/deploy-token"
printf '%s\n' "${ADMIN_PASSWORD}" > "${WORK}/admin-password"
chmod 600 "${WORK}/deploy-token" "${WORK}/admin-password"

echo "==> current CLI -> released ${PREVIOUS_VERSION} server"
mkdir -p "${WORK}/old-server"
(
  cd "${WORK}/old-server"
  SHINYHUB_AUTH_SECRET="compatibility-secret-that-is-long-enough-0123456789" \
  SHINYHUB_ADMIN_USER="compat-admin" \
  SHINYHUB_ADMIN_PASSWORD="${ADMIN_PASSWORD}" \
  SHINYHUB_SERVER_HOST=127.0.0.1 \
  SHINYHUB_SERVER_PORT="${OLD_PORT}" \
  SHINYHUB_SHUTDOWN_APPS=stop \
  SHINYHUB_DEPLOY_TOKEN="${TOKEN}" \
    exec "${OLD_BIN}" serve
) >"${WORK}/old-server.log" 2>&1 &
OLD_PID=$!
"${ROOT}/scripts/wait-http.sh" "${OLD_HOST}/readyz" 30 || fail "released server readiness"
kill -0 "${OLD_PID}" 2>/dev/null || fail "released server exited during startup"
curl -fsS "${OLD_HOST}/api/server-info" -o "${WORK}/old-server-info.json" || fail "released server-info"
if grep -Fq '"protocol_version"' "${WORK}/old-server-info.json"; then
  fail "pinned legacy server unexpectedly advertises protocol_version; update the matrix pin deliberately"
fi

"${CURRENT_BIN}" connect "${OLD_HOST}" --name released \
  --token-file "${WORK}/deploy-token" --config "${WORK}/current-client.json" --output table \
  >"${WORK}/current-connect.log" 2>&1 || fail "current connect to released server"
grep -Fq 'legacy capability negotiation' "${WORK}/current-connect.log" \
  || fail "current connect did not make legacy negotiation visible"
"${CURRENT_BIN}" doctor "${WORK}/app-current-to-old" --slug compat-current-old \
  --config "${WORK}/current-client.json" --output table \
  >"${WORK}/current-doctor.log" 2>&1 || fail "current doctor against released server"
grep -Fq 'READY' "${WORK}/current-doctor.log" || fail "current doctor did not report READY against released server"
"${CURRENT_BIN}" deploy "${WORK}/app-current-to-old" --slug compat-current-old \
  --visibility public --wait --config "${WORK}/current-client.json" --output table \
  >"${WORK}/current-deploy.log" 2>&1 || fail "current deploy to released server"
curl -fsS "${OLD_HOST}/app/compat-current-old/" | grep -Fq 'shinyhub remote-worker E2E' \
  || fail "app deployed by current CLI is not serving on released server"

sed "s/${TOKEN}/shk_0000000000000000000000000000000000000000000000000000000000000000/g" \
  "${WORK}/current-client.json" > "${WORK}/revoked-client.json"
if "${CURRENT_BIN}" whoami --config "${WORK}/revoked-client.json" --output table \
  >"${WORK}/current-recovery.log" 2>&1; then
  fail "invalid credential unexpectedly authenticated against released server"
fi
grep -Fq 'expired or been revoked' "${WORK}/current-recovery.log" \
  || fail "released-server credential failure did not explain the likely cause"
grep -Fq 'shinyhub connect' "${WORK}/current-recovery.log" \
  || fail "released-server credential failure did not explain how to recover"

kill "${OLD_PID}" 2>/dev/null || true
wait "${OLD_PID}" 2>/dev/null || true
OLD_PID=""

echo "==> released ${PREVIOUS_VERSION} CLI -> current server"
mkdir -p "${WORK}/current-server"
(
  cd "${WORK}/current-server"
  "${CURRENT_BIN}" init --admin-user compat-admin \
    --admin-password-file "${WORK}/admin-password" \
    --config "${WORK}/current-server/shinyhub.yaml" --output table
) >/dev/null || fail "current server init"
(
  cd "${WORK}/current-server"
  SHINYHUB_SERVER_HOST=127.0.0.1 \
  SHINYHUB_SERVER_PORT="${CURRENT_PORT}" \
  SHINYHUB_SHUTDOWN_APPS=stop \
  SHINYHUB_DEPLOY_TOKEN="${TOKEN}" \
    exec "${CURRENT_BIN}" serve --config "${WORK}/current-server/shinyhub.yaml"
) >"${WORK}/current-server.log" 2>&1 &
CURRENT_PID=$!
"${ROOT}/scripts/wait-http.sh" "${CURRENT_HOST}/readyz" 30 || fail "current server readiness"
kill -0 "${CURRENT_PID}" 2>/dev/null || fail "current server exited during startup"
curl -fsS "${CURRENT_HOST}/api/server-info" -o "${WORK}/current-server-info.json" || fail "current server-info"
grep -Fq "\"protocol_version\":${CURRENT_PROTOCOL}" "${WORK}/current-server-info.json" \
  || fail "current server did not advertise protocol version ${CURRENT_PROTOCOL}"

"${OLD_BIN}" login --host "${CURRENT_HOST}" --name current --token "${TOKEN}" \
  --config "${WORK}/old-client.json" --output table \
  >"${WORK}/old-login.log" 2>&1 || fail "released CLI login to current server"
"${OLD_BIN}" whoami --config "${WORK}/old-client.json" --output table \
  >"${WORK}/old-whoami.log" 2>&1 || fail "released CLI whoami against current server"
grep -Fq '__deploy__' "${WORK}/old-whoami.log" || fail "released CLI did not preserve its identity against current server"
"${OLD_BIN}" deploy "${WORK}/app-old-to-current" --slug compat-old-current \
  --visibility public --wait --config "${WORK}/old-client.json" --output table \
  >"${WORK}/old-deploy.log" 2>&1 || fail "released CLI deploy to current server"
curl -fsS "${CURRENT_HOST}/app/compat-old-current/" | grep -Fq 'shinyhub remote-worker E2E' \
  || fail "app deployed by released CLI is not serving on current server"

echo "CLI COMPATIBILITY E2E PASS (${PREVIOUS_VERSION} <-> ${CURRENT_VERSION})"
