#!/usr/bin/env bash
# Cross-release contract with two deliberately different baselines:
#
#   * legacy: the last release before /api/server-info exposed protocol_version;
#     this lane keeps capability-negotiation fallback alive.
#   * previous: the immediate predecessor release; this lane proves the normal
#     bidirectional upgrade path users actually take.
#
# Every released binary is downloaded exactly as published and verified against
# a checksum manifest pinned in the repository.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK="$(mktemp -d)"
RELEASED_PID=""
CURRENT_PID=""
LEGACY_PORT="${SHINYHUB_COMPAT_LEGACY_PORT:-18090}"
PREVIOUS_PORT="${SHINYHUB_COMPAT_PREVIOUS_PORT:-${SHINYHUB_COMPAT_OLD_PORT:-18091}}"
CURRENT_PORT="${SHINYHUB_COMPAT_CURRENT_PORT:-18092}"
CURRENT_HOST="http://127.0.0.1:${CURRENT_PORT}"
TOKEN="compatibility-e2e-token-0123456789abcdef"
ADMIN_PASSWORD="compatibility-e2e-password"

cleanup() {
  for pid in "${CURRENT_PID}" "${RELEASED_PID}"; do
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
  for log_path in "${WORK}"/*.log; do
    if [ -s "${log_path}" ]; then
      echo "----- $(basename "${log_path}") (last 100 lines) -----" >&2
      tail -100 "${log_path}" >&2
    fi
  done
  exit 1
}

command -v curl >/dev/null 2>&1 || fail "curl is required"
command -v uv >/dev/null 2>&1 || fail "uv is required"

LEGACY_VERSION="$(tr -d '[:space:]' < "${ROOT}/testdata/compatibility/legacy-release.txt")"
PREVIOUS_VERSION="$(tr -d '[:space:]' < "${ROOT}/testdata/compatibility/previous-release.txt")"
[ -n "${LEGACY_VERSION}" ] || fail "legacy-release.txt is empty"
[ -n "${PREVIOUS_VERSION}" ] || fail "previous-release.txt is empty"
[ "${LEGACY_VERSION}" != "${PREVIOUS_VERSION}" ] || fail "legacy and previous release pins must be distinct"
[ "${LEGACY_PORT}" != "${PREVIOUS_PORT}" ] || fail "legacy and previous ports must be distinct"
[ "${LEGACY_PORT}" != "${CURRENT_PORT}" ] || fail "legacy and current ports must be distinct"
[ "${PREVIOUS_PORT}" != "${CURRENT_PORT}" ] || fail "previous and current ports must be distinct"

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

download_release() {
  local lane="$1"
  local release_version="$2"
  local checksums="${ROOT}/testdata/compatibility/${release_version}-checksums.txt"
  local archive="${WORK}/${lane}-${ASSET}"
  local release_dir="${WORK}/released-${lane}"
  local expected_sha actual_sha

  [ -f "${checksums}" ] || fail "missing pinned checksum manifest ${checksums}"
  expected_sha="$(awk -v asset="${ASSET}" '$2 == asset {print $1}' "${checksums}")"
  [ -n "${expected_sha}" ] || fail "${ASSET} is not pinned in ${checksums}"

  echo "==> downloading ${lane} release ${release_version} (${RELEASE_OS}/${RELEASE_ARCH})"
  curl -fLsS "https://github.com/rvben/shinyhub/releases/download/${release_version}/${ASSET}" \
    -o "${archive}" || fail "download ${release_version}"
  if command -v sha256sum >/dev/null 2>&1; then
    actual_sha="$(sha256sum "${archive}" | awk '{print $1}')"
  elif command -v shasum >/dev/null 2>&1; then
    actual_sha="$(shasum -a 256 "${archive}" | awk '{print $1}')"
  else
    fail "sha256sum or shasum is required to verify released binaries"
  fi
  [ "${actual_sha}" = "${expected_sha}" ] || fail "checksum mismatch for ${release_version}/${ASSET}"

  mkdir -p "${release_dir}"
  tar -xzf "${archive}" -C "${release_dir}" || fail "extract ${release_version}/${ASSET}"
  [ -x "${release_dir}/shinyhub" ] || fail "${release_version} archive has no executable shinyhub"
  "${release_dir}/shinyhub" --version | grep -Fq "${release_version#v}" \
    || fail "released binary version does not match ${release_version}"
}

download_release legacy "${LEGACY_VERSION}"
download_release previous "${PREVIOUS_VERSION}"
LEGACY_BIN="${WORK}/released-legacy/shinyhub"
PREVIOUS_BIN="${WORK}/released-previous/shinyhub"

CURRENT_VERSION="v$(sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "${ROOT}/package.json" | head -1)"
CURRENT_PROTOCOL="$(sed -n 's/^const CurrentVersion = \([0-9][0-9]*\)$/\1/p' "${ROOT}/internal/protocol/version.go")"
[ -n "${CURRENT_PROTOCOL}" ] || fail "could not read protocol.CurrentVersion"
echo "==> building current CLI/server ${CURRENT_VERSION}"
mkdir -p "${WORK}/current-bin"
GOWORK=off go build -ldflags "-X main.version=${CURRENT_VERSION}" \
  -o "${WORK}/current-bin/shinyhub" "${ROOT}/cmd/shinyhub" || fail "build current binary"
CURRENT_BIN="${WORK}/current-bin/shinyhub"

printf '%s\n' "${TOKEN}" > "${WORK}/deploy-token"
printf '%s\n' "${ADMIN_PASSWORD}" > "${WORK}/admin-password"
chmod 600 "${WORK}/deploy-token" "${WORK}/admin-password"

exercise_current_against_released_server() {
  local lane="$1"
  local release_version="$2"
  local released_bin="$3"
  local port="$4"
  local protocol_mode="$5"
  local host="http://127.0.0.1:${port}"
  local slug="compat-current-${lane}"
  local server_dir="${WORK}/${lane}-server"
  local app_dir="${WORK}/app-current-to-${lane}"
  local config="${WORK}/current-${lane}-client.json"
  local server_info="${WORK}/${lane}-server-info.json"

  echo "==> current CLI -> ${lane} ${release_version} server"
  cp -R "${ROOT}/testdata/e2e-app" "${app_dir}"
  mkdir -p "${server_dir}"
  (
    cd "${server_dir}"
    SHINYHUB_AUTH_SECRET="compatibility-secret-that-is-long-enough-0123456789" \
    SHINYHUB_ADMIN_USER="compat-admin" \
    SHINYHUB_ADMIN_PASSWORD="${ADMIN_PASSWORD}" \
    SHINYHUB_SERVER_HOST=127.0.0.1 \
    SHINYHUB_SERVER_PORT="${port}" \
    SHINYHUB_SHUTDOWN_APPS=stop \
    SHINYHUB_DEPLOY_TOKEN="${TOKEN}" \
      exec "${released_bin}" serve
  ) >"${WORK}/${lane}-server.log" 2>&1 &
  RELEASED_PID=$!
  "${ROOT}/scripts/wait-http.sh" "${host}/readyz" 30 || fail "${lane} server readiness"
  kill -0 "${RELEASED_PID}" 2>/dev/null || fail "${lane} server exited during startup"
  curl -fsS "${host}/api/server-info" -o "${server_info}" || fail "${lane} server-info"

  case "${protocol_mode}" in
    absent)
      if grep -Fq '"protocol_version"' "${server_info}"; then
        fail "legacy server unexpectedly advertises protocol_version; update the legacy pin deliberately"
      fi
      ;;
    present)
      grep -Eq '"protocol_version"[[:space:]]*:[[:space:]]*[1-9][0-9]*' "${server_info}" \
        || fail "immediate previous server did not advertise a non-zero protocol_version"
      ;;
    *) fail "unknown protocol mode ${protocol_mode}" ;;
  esac

  "${CURRENT_BIN}" connect "${host}" --name "${lane}" \
    --token-file "${WORK}/deploy-token" --config "${config}" --output table \
    >"${WORK}/current-${lane}-connect.log" 2>&1 || fail "current connect to ${lane} server"
  if [ "${protocol_mode}" = "absent" ]; then
    # Only the legacy lane owns this fallback assertion. The immediate previous
    # lane proves the ordinary protocol-advertising upgrade path independently.
    grep -Fq 'capability-gated' "${WORK}/current-${lane}-connect.log" \
      || fail "current connect did not make legacy capability negotiation visible"
  fi

  "${CURRENT_BIN}" doctor "${app_dir}" --slug "${slug}" \
    --config "${config}" --output table \
    >"${WORK}/current-${lane}-doctor.log" 2>&1 || fail "current doctor against ${lane} server"
  grep -Fq 'READY' "${WORK}/current-${lane}-doctor.log" \
    || fail "current doctor did not report READY against ${lane} server"
  "${CURRENT_BIN}" deploy "${app_dir}" --slug "${slug}" \
    --visibility public --wait --config "${config}" --output table \
    >"${WORK}/current-${lane}-deploy.log" 2>&1 || fail "current deploy to ${lane} server"
  curl -fsS "${host}/app/${slug}/" | grep -Fq 'shinyhub remote-worker E2E' \
    || fail "app deployed by current CLI is not serving on ${lane} server"

  if [ "${lane}" = "previous" ]; then
    # Follow the predecessor's advertised capability instead of assuming every
    # immediate previous release predates deploy-trigger convergence. This keeps
    # the rolling baseline valid both when a capability is new and after it has
    # shipped in consecutive releases.
    if grep -Eq '"schedule_deploy_convergence"[[:space:]]*:[[:space:]]*true' "${server_info}"; then
      "${CURRENT_BIN}" schedule add "${slug}" --name supported-trigger \
        --cron '0 5 * * *' --cmd 'python producer.py' --deploy-trigger bundle_change \
        --config "${config}" --output table \
        >"${WORK}/current-previous-trigger-add.log" 2>&1 \
        || fail "current CLI rejected deploy_trigger advertised by the immediate previous server"
      "${CURRENT_BIN}" schedule ls "${slug}" --config "${config}" --output json \
        >"${WORK}/current-previous-schedules.json" 2>"${WORK}/current-previous-schedules.log" \
        || fail "list schedules after supported predecessor mutation"
      grep -Fq 'supported-trigger' "${WORK}/current-previous-schedules.json" \
        || fail "supported deploy_trigger command did not create a schedule on the immediate previous server"
    else
      # Older servers accept unknown JSON fields, so sending deploy_trigger
      # without a capability check can appear successful while silently
      # creating a lifetime-gated schedule. Prove rejection happens before POST.
      if "${CURRENT_BIN}" schedule add "${slug}" --name unsupported-trigger \
        --cron '0 5 * * *' --cmd 'python producer.py' --deploy-trigger bundle_change \
        --config "${config}" --output table \
        >"${WORK}/current-previous-trigger-add.log" 2>&1; then
        fail "current CLI silently sent deploy_trigger to an unsupported immediate previous server"
      fi
      grep -Fq 'does not support --deploy-trigger=bundle_change' \
        "${WORK}/current-previous-trigger-add.log" \
        || fail "unsupported deploy_trigger failure did not identify the missing predecessor capability"
      grep -Fq 'no schedule was changed' "${WORK}/current-previous-trigger-add.log" \
        || fail "unsupported deploy_trigger failure did not promise pre-mutation rejection"
      "${CURRENT_BIN}" schedule ls "${slug}" --config "${config}" --output json \
        >"${WORK}/current-previous-schedules.json" 2>"${WORK}/current-previous-schedules.log" \
        || fail "list schedules after rejected predecessor mutation"
      if grep -Fq 'unsupported-trigger' "${WORK}/current-previous-schedules.json"; then
        fail "rejected deploy_trigger command still created a schedule on the immediate previous server"
      fi
    fi
  fi

  sed "s/${TOKEN}/shk_0000000000000000000000000000000000000000000000000000000000000000/g" \
    "${config}" > "${WORK}/current-${lane}-revoked-client.json"
  if "${CURRENT_BIN}" whoami --config "${WORK}/current-${lane}-revoked-client.json" --output table \
    >"${WORK}/current-${lane}-recovery.log" 2>&1; then
    fail "invalid credential unexpectedly authenticated against ${lane} server"
  fi
  grep -Fq 'expired or been revoked' "${WORK}/current-${lane}-recovery.log" \
    || fail "${lane}-server credential failure did not explain the likely cause"
  grep -Fq 'shinyhub connect' "${WORK}/current-${lane}-recovery.log" \
    || fail "${lane}-server credential failure did not explain how to recover"

  kill "${RELEASED_PID}" 2>/dev/null || true
  wait "${RELEASED_PID}" 2>/dev/null || true
  RELEASED_PID=""
}

exercise_current_against_released_server legacy "${LEGACY_VERSION}" "${LEGACY_BIN}" "${LEGACY_PORT}" absent
exercise_current_against_released_server previous "${PREVIOUS_VERSION}" "${PREVIOUS_BIN}" "${PREVIOUS_PORT}" present

echo "==> released CLIs -> current ${CURRENT_VERSION} server"
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

exercise_released_cli_against_current_server() {
  local lane="$1"
  local release_version="$2"
  local released_bin="$3"
  local slug="compat-${lane}-current"
  local app_dir="${WORK}/app-${lane}-to-current"
  local config="${WORK}/${lane}-client.json"

  echo "==> ${lane} ${release_version} CLI -> current server"
  cp -R "${ROOT}/testdata/e2e-app" "${app_dir}"
  "${released_bin}" login --host "${CURRENT_HOST}" --name current --token "${TOKEN}" \
    --config "${config}" --output table \
    >"${WORK}/${lane}-login.log" 2>&1 || fail "${lane} CLI login to current server"
  "${released_bin}" whoami --config "${config}" --output table \
    >"${WORK}/${lane}-whoami.log" 2>&1 || fail "${lane} CLI whoami against current server"
  grep -Fq '__deploy__' "${WORK}/${lane}-whoami.log" \
    || fail "${lane} CLI did not preserve its identity against current server"
  "${released_bin}" deploy "${app_dir}" --slug "${slug}" \
    --visibility public --wait --config "${config}" --output table \
    >"${WORK}/${lane}-deploy.log" 2>&1 || fail "${lane} CLI deploy to current server"
  curl -fsS "${CURRENT_HOST}/app/${slug}/" | grep -Fq 'shinyhub remote-worker E2E' \
    || fail "app deployed by ${lane} CLI is not serving on current server"
}

exercise_released_cli_against_current_server legacy "${LEGACY_VERSION}" "${LEGACY_BIN}"
exercise_released_cli_against_current_server previous "${PREVIOUS_VERSION}" "${PREVIOUS_BIN}"

echo "CLI COMPATIBILITY E2E PASS (legacy ${LEGACY_VERSION}; previous ${PREVIOUS_VERSION}; current ${CURRENT_VERSION})"
