#!/usr/bin/env bash
# Release-real onboarding dogfood: build and install the Python wheel, pair the
# installed CLI through a real browser, deploy, verify its live logs in the UI,
# revoke the credential, and recover through a second browser pairing without
# signing in again.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK="$(mktemp -d)"
SERVER_PID=""
BROWSER_PID=""
CONNECT_PID=""
PORT="${SHINYHUB_BROWSER_E2E_PORT:-18090}"
HOST="http://127.0.0.1:${PORT}"
ADMIN_USER="browser-admin"
ADMIN_PASSWORD="browser-onboarding-password"
CLI_VERSION="v0.0.0-browser-e2e"
WHEEL_VERSION="0.0.0+browser.e2e"

cleanup() {
  for pid in "${CONNECT_PID}" "${BROWSER_PID}" "${SERVER_PID}"; do
    if [ -n "${pid}" ]; then
      kill "${pid}" 2>/dev/null || true
      wait "${pid}" 2>/dev/null || true
    fi
  done
  if [ -n "${E2E_KEEP:-}" ]; then
    echo "E2E_KEEP set; browser onboarding artifacts kept at ${WORK}" >&2
    return
  fi
  rm -rf "${WORK}"
}
trap cleanup EXIT

# file_mode prints a file's permission bits as octal. The flag that selects the
# mode differs by platform: -c is GNU/uutils, -f is BSD/macOS. GNU is tried
# first because passing its format to BSD stat fails outright, whereas passing
# the BSD format to GNU stat reads '%Lp' as a second operand and prints a whole
# filesystem block on stdout, which a fallback would then concatenate with the
# real answer. Returns non-zero unless the result is octal permission bits, so
# an unreadable or missing file never masquerades as a wrong mode.
file_mode() {
  local path="$1" mode
  mode="$(stat -c '%a' "${path}" 2>/dev/null)" || mode="$(stat -f '%Lp' "${path}" 2>/dev/null)" || return 1
  case "${mode}" in
    [0-7][0-7][0-7] | [0-7][0-7][0-7][0-7]) printf '%s\n' "${mode}" ;;
    *) return 1 ;;
  esac
}

fail() {
  echo "BROWSER ONBOARDING E2E FAIL: $*" >&2
  for log in build.log install.log server.log chrome.log connect-first.log connect-current.json connect-current.log whoami.log doctor.log deploy.log logs-browser.log connect-refresh.log tokens-after-refresh.log revoked.log connect-recovery.log; do
    if [ -s "${WORK}/${log}" ]; then
      echo "----- ${log} (last 100 lines) -----" >&2
      tail -100 "${WORK}/${log}" >&2
    fi
  done
  exit 1
}

wheel_platform() {
  case "$(uname -s):$(uname -m)" in
    Darwin:arm64)  echo "macosx_11_0_arm64" ;;
    Darwin:x86_64) echo "macosx_11_0_x86_64" ;;
    Linux:x86_64)  echo "manylinux_2_17_x86_64" ;;
    Linux:aarch64|Linux:arm64) echo "manylinux_2_17_aarch64" ;;
    *) fail "unsupported wheel test platform $(uname -s)/$(uname -m)" ;;
  esac
}

find_browser() {
  if [ -n "${SHINYHUB_E2E_BROWSER:-}" ]; then
    [ -x "${SHINYHUB_E2E_BROWSER}" ] || fail "SHINYHUB_E2E_BROWSER is not executable: ${SHINYHUB_E2E_BROWSER}"
    echo "${SHINYHUB_E2E_BROWSER}"
    return
  fi
  for candidate in google-chrome google-chrome-stable chromium chromium-browser; do
    if command -v "${candidate}" >/dev/null 2>&1; then
      command -v "${candidate}"
      return
    fi
  done
  if [ -x "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" ]; then
    echo "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
    return
  fi
  fail "Chrome or Chromium is required (or set SHINYHUB_E2E_BROWSER)"
}

wait_for_pairing_url() {
  local log="$1"
  local pid="$2"
  local attempt=0
  local pairing_url=""
  while [ "${attempt}" -lt 300 ]; do
    pairing_url="$(awk '/^  http.*\/tokens\?.*connect_hash=/{print $1; exit}' "${log}" 2>/dev/null || true)"
    if [ -n "${pairing_url}" ]; then
      echo "${pairing_url}"
      return
    fi
    if ! kill -0 "${pid}" 2>/dev/null; then
      return 1
    fi
    attempt=$((attempt + 1))
    sleep 0.1
  done
  return 1
}

start_connect() {
  local log="$1"
  "${BIN}" connect "${HOST}" --no-browser --timeout 45s \
    --config "${WORK}/client.json" --output table >"${log}" 2>&1 &
  CONNECT_PID=$!
}

start_refresh() {
  local log="$1"
  "${BIN}" connect --refresh --no-browser --timeout 45s \
    --config "${WORK}/client.json" --output table >"${log}" 2>&1 &
  CONNECT_PID=$!
}

finish_connect() {
  local log="$1"
  if ! wait "${CONNECT_PID}"; then
    CONNECT_PID=""
    fail "browser pairing did not finish"
  fi
  CONNECT_PID=""
  grep -q 'Connected to' "${log}" || fail "connect did not report a clear success state"
  # The backticks are literal CLI guidance.
  # shellcheck disable=SC2016
  grep -q 'Next: run `shinyhub deploy . --open`' "${log}" || fail "connect did not provide the first deploy continuation"
  if grep -q 'shk_' "${log}"; then
    fail "raw CLI credential leaked into connect output"
  fi
}

finish_refresh() {
  local log="$1"
  if ! wait "${CONNECT_PID}"; then
    CONNECT_PID=""
    fail "browser credential refresh did not finish"
  fi
  CONNECT_PID=""
  grep -q 'Refreshed credential for' "${log}" || fail "refresh did not report a clear success state"
  if grep -q 'shk_' "${log}"; then
    fail "raw CLI credential leaked into refresh output"
  fi
}

command -v uv >/dev/null 2>&1 || fail "uv is required"
command -v node >/dev/null 2>&1 || fail "Node 20+ is required"
command -v curl >/dev/null 2>&1 || fail "curl is required"
BROWSER="$(find_browser)"

echo "==> building the distributable Python wheel"
cp -R "${ROOT}/packaging/python" "${WORK}/python-package"
GOWORK=off go build -ldflags "-X main.version=${CLI_VERSION}" \
  -o "${WORK}/python-package/src/shinyhub/_binary/shinyhub" "${ROOT}/cmd/shinyhub" \
  >"${WORK}/build.log" 2>&1 || fail "embedded Go binary build"
SHINYHUB_WHEEL_VERSION="${WHEEL_VERSION}" \
SHINYHUB_WHEEL_PLATFORM="$(wheel_platform)" \
  uv build --wheel "${WORK}/python-package" --out-dir "${WORK}/dist" \
  >>"${WORK}/build.log" 2>&1 || fail "Python wheel build"
WHEEL="$(find "${WORK}/dist" -maxdepth 1 -name 'shinyhub-*.whl' -print -quit)"
[ -n "${WHEEL}" ] || fail "wheel build produced no artifact"

echo "==> installing the wheel as an isolated user-facing tool"
UV_TOOL_DIR="${WORK}/uv-tools" UV_TOOL_BIN_DIR="${WORK}/bin" \
  uv tool install --force --no-index "${WHEEL}" >"${WORK}/install.log" 2>&1 \
  || fail "wheel installation"
BIN="${WORK}/bin/shinyhub"
[ -x "${BIN}" ] || fail "wheel did not install the shinyhub command"
"${BIN}" --version | grep -q "${CLI_VERSION}" || fail "installed command lost the embedded release version"

cp -R "${ROOT}/testdata/e2e-app" "${WORK}/app"
printf '%s\n' "${ADMIN_PASSWORD}" >"${WORK}/admin-password"
chmod 600 "${WORK}/admin-password"

echo "==> initializing and starting a fresh installed server"
(
  cd "${WORK}"
  "${BIN}" init --admin-user "${ADMIN_USER}" \
    --admin-password-file "${WORK}/admin-password" \
    --config "${WORK}/shinyhub.yaml" --output table
) >/dev/null || fail "server init"
(
  cd "${WORK}"
  SHINYHUB_SERVER_HOST=127.0.0.1 \
  SHINYHUB_SERVER_PORT="${PORT}" \
  SHINYHUB_SHUTDOWN_APPS=stop \
    exec "${BIN}" serve --config "${WORK}/shinyhub.yaml"
) >"${WORK}/server.log" 2>&1 &
SERVER_PID=$!
"${ROOT}/scripts/wait-http.sh" "${HOST}/readyz" 30 || fail "server readiness"
kill -0 "${SERVER_PID}" 2>/dev/null || fail "server exited during startup (port ${PORT} may be in use)"

echo "==> launching an isolated headless browser"
mkdir -p "${WORK}/chrome-profile"
browser_args=(
  --headless=new
  --disable-background-networking
  --disable-component-update
  --disable-dev-shm-usage
  --disable-gpu
  --no-default-browser-check
  --no-first-run
  --remote-debugging-port=0
  --user-data-dir="${WORK}/chrome-profile"
)
if [ "$(id -u)" = "0" ]; then browser_args+=(--no-sandbox); fi
browser_args+=(about:blank)
"${BROWSER}" "${browser_args[@]}" >"${WORK}/chrome.log" 2>&1 &
BROWSER_PID=$!
attempt=0
while [ ! -s "${WORK}/chrome-profile/DevToolsActivePort" ] && [ "${attempt}" -lt 300 ]; do
  kill -0 "${BROWSER_PID}" 2>/dev/null || fail "browser exited before exposing DevTools"
  attempt=$((attempt + 1))
  sleep 0.1
done
[ -s "${WORK}/chrome-profile/DevToolsActivePort" ] || fail "browser DevTools endpoint did not become ready"
DEBUG_PORT="$(sed -n '1p' "${WORK}/chrome-profile/DevToolsActivePort")"
DEBUG_URL="http://127.0.0.1:${DEBUG_PORT}"

echo "==> pairing the redirected CLI through real sign-in and approval UI"
start_connect "${WORK}/connect-first.log"
PAIRING_URL="$(wait_for_pairing_url "${WORK}/connect-first.log" "${CONNECT_PID}")" \
  || fail "CLI did not print a pairing URL"
TOKEN_NAME="$(node --experimental-websocket "${ROOT}/scripts/browser-onboarding-cdp.mjs" approve \
  "${DEBUG_URL}" "${PAIRING_URL}" "${ADMIN_USER}" "${ADMIN_PASSWORD}" "${WORK}/paired.png")" \
  || fail "browser sign-in and approval"
finish_connect "${WORK}/connect-first.log"
[ -n "${TOKEN_NAME}" ] || fail "browser approval did not identify its token"
MODE="$(file_mode "${WORK}/client.json")" \
  || fail "could not read the mode of ${WORK}/client.json; connect may not have written it"
[ "${MODE}" = "600" ] || fail "client credentials are mode ${MODE}, want 0600"

echo "==> proving ordinary connect is a credential-preserving no-op"
cp "${WORK}/client.json" "${WORK}/client-before-current.json"
BEFORE_CREDENTIAL_ID="$(
  "${BIN}" whoami --config "${WORK}/client.json" --output json |
    node -p 'JSON.parse(require("fs").readFileSync(0, "utf8")).credential.id'
)" || fail "read credential ID before idempotent connect"
"${BIN}" connect "${HOST}" --config "${WORK}/client.json" --output json \
  >"${WORK}/connect-current.json" 2>"${WORK}/connect-current.log" \
  || fail "idempotent second connect"
grep -Fq '"status":"current"' "${WORK}/connect-current.json" \
  || fail "second connect did not report status current"
if grep -Eq '/tokens\?.*connect_hash=|Authorize this CLI|Waiting for approval' \
  "${WORK}/connect-current.log"; then
  fail "second connect unexpectedly entered browser authorization"
fi
cmp -s "${WORK}/client-before-current.json" "${WORK}/client.json" \
  || fail "second connect rewrote the credentials file"
CURRENT_CREDENTIAL_ID="$(
  node -p 'JSON.parse(require("fs").readFileSync(0, "utf8")).credential.id' \
    <"${WORK}/connect-current.json"
)" || fail "read credential ID after idempotent connect"
[ "${CURRENT_CREDENTIAL_ID}" = "${BEFORE_CREDENTIAL_ID}" ] \
  || fail "second connect changed credential ID (${BEFORE_CREDENTIAL_ID} -> ${CURRENT_CREDENTIAL_ID})"

echo "==> proving the paired wheel can diagnose and deploy an app"
"${BIN}" whoami --config "${WORK}/client.json" --output table >"${WORK}/whoami.log" 2>&1 \
  || fail "paired whoami"
grep -q "${ADMIN_USER}" "${WORK}/whoami.log" || fail "whoami did not report the paired identity"
"${BIN}" doctor "${WORK}/app" --config "${WORK}/client.json" --output table \
  >"${WORK}/doctor.log" 2>&1 || fail "remote doctor"
grep -q 'READY' "${WORK}/doctor.log" || fail "doctor did not report READY"
"${BIN}" deploy "${WORK}/app" --visibility public --wait \
  --config "${WORK}/client.json" --output table >"${WORK}/deploy.log" 2>&1 \
  || fail "first deploy"
body="$(curl -fsS "${HOST}/app/app/" || true)"
echo "${body}" | grep -q 'shinyhub remote-worker E2E' || fail "deployed app did not serve its body"
open_result="$("${BIN}" apps open app --no-browser --config "${WORK}/client.json" --output json)" \
  || fail "headless app open"
echo "${open_result}" | grep -Fq '"url":"'"${HOST}"'/app/app/"' \
  || fail "app open did not return the canonical URL"
echo "${open_result}" | grep -Fq '"opened":false' \
  || fail "headless app open did not report opened=false"

echo "==> reading real application output in the packaged dashboard"
node --experimental-websocket "${ROOT}/scripts/browser-onboarding-cdp.mjs" logs \
  "${DEBUG_URL}" "${HOST}/apps/app/logs" "shinyhub browser logs E2E" "" "${WORK}/logs.png" \
  >"${WORK}/logs-browser.log" 2>&1 || fail "app-detail logs browser contract"
grep -q 'LOGS PASS' "${WORK}/logs-browser.log" || fail "logs browser contract did not report success"

echo "==> proactively rotating the healthy credential through browser approval"
start_refresh "${WORK}/connect-refresh.log"
REFRESH_URL="$(wait_for_pairing_url "${WORK}/connect-refresh.log" "${CONNECT_PID}")" \
  || fail "refresh CLI did not print a pairing URL"
REFRESH_TOKEN_NAME="$(node --experimental-websocket "${ROOT}/scripts/browser-onboarding-cdp.mjs" approve \
  "${DEBUG_URL}" "${REFRESH_URL}" "" "" "${WORK}/refreshed.png")" \
  || fail "returning-browser refresh approval"
finish_refresh "${WORK}/connect-refresh.log"
[ -n "${REFRESH_TOKEN_NAME}" ] || fail "refresh approval did not identify its token"
[ "${REFRESH_TOKEN_NAME}" != "${TOKEN_NAME}" ] || fail "refresh unexpectedly reused the old credential"
"${BIN}" tokens list --config "${WORK}/client.json" --output table \
  >"${WORK}/tokens-after-refresh.log" 2>&1 || fail "list tokens after refresh"
grep -q "${REFRESH_TOKEN_NAME}" "${WORK}/tokens-after-refresh.log" || fail "refreshed token is missing from inventory"
if grep -q "${TOKEN_NAME}" "${WORK}/tokens-after-refresh.log"; then
  fail "refresh did not revoke the previous API key"
fi

echo "==> revoking in the browser and checking actionable CLI recovery"
node --experimental-websocket "${ROOT}/scripts/browser-onboarding-cdp.mjs" revoke \
  "${DEBUG_URL}" "${REFRESH_TOKEN_NAME}" "" "" "${WORK}/revoked.png" \
  || fail "browser token revocation"
if "${BIN}" whoami --config "${WORK}/client.json" --output table >"${WORK}/revoked.log" 2>&1; then
  fail "revoked credential unexpectedly remained valid"
fi
grep -q 'expired or been revoked' "${WORK}/revoked.log" || fail "revoked credential did not explain the likely cause"
grep -q 'shinyhub connect' "${WORK}/revoked.log" || fail "revoked credential did not explain how to recover"

echo "==> reconnecting in the existing browser session"
start_connect "${WORK}/connect-recovery.log"
RECOVERY_URL="$(wait_for_pairing_url "${WORK}/connect-recovery.log" "${CONNECT_PID}")" \
  || fail "recovery CLI did not print a pairing URL"
RECOVERY_TOKEN_NAME="$(node --experimental-websocket "${ROOT}/scripts/browser-onboarding-cdp.mjs" approve \
  "${DEBUG_URL}" "${RECOVERY_URL}" "" "" "${WORK}/reconnected.png")" \
  || fail "returning-browser approval"
finish_connect "${WORK}/connect-recovery.log"
[ -n "${RECOVERY_TOKEN_NAME}" ] || fail "recovery approval did not identify its token"
[ "${RECOVERY_TOKEN_NAME}" != "${REFRESH_TOKEN_NAME}" ] || fail "recovery unexpectedly reused the revoked credential"
"${BIN}" whoami --config "${WORK}/client.json" --output table >"${WORK}/whoami.log" 2>&1 \
  || fail "recovered whoami"
grep -q "${ADMIN_USER}" "${WORK}/whoami.log" || fail "recovered identity was not preserved"

echo "BROWSER ONBOARDING E2E PASS"
