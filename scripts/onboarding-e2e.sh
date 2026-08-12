#!/usr/bin/env bash
# Clean-room onboarding dogfood: a real binary, fresh server and credentials,
# real local app boot, real deploy, and an intentionally broken redeploy whose
# app log must be surfaced by the CLI without a second command.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK="$(mktemp -d)"
SERVER_PID=""
PORT="${SHINYHUB_ONBOARDING_E2E_PORT:-18089}"
HOST="http://127.0.0.1:${PORT}"
TOKEN="onboarding-e2e-token-0123456789abcdef"

cleanup() {
  if [ -n "${SERVER_PID}" ]; then
    kill "${SERVER_PID}" 2>/dev/null || true
    wait "${SERVER_PID}" 2>/dev/null || true
  fi
  if [ -n "${E2E_KEEP:-}" ]; then
    echo "E2E_KEEP set; onboarding artifacts kept at ${WORK}" >&2
    return
  fi
  rm -rf "${WORK}"
}
trap cleanup EXIT

fail() {
  echo "ONBOARDING E2E FAIL: $*" >&2
  for log in server.log local.log connect.log doctor.log plan.log deploy.log broken.log; do
    if [ -s "${WORK}/${log}" ]; then
      echo "----- ${log} (last 80 lines) -----" >&2
      tail -80 "${WORK}/${log}" >&2
    fi
  done
  exit 1
}

command -v uv >/dev/null 2>&1 || fail "uv is required"
command -v curl >/dev/null 2>&1 || fail "curl is required"

echo "==> building an isolated binary"
GOWORK=off go build -o "${WORK}/shinyhub" "${ROOT}/cmd/shinyhub" || fail "build"
BIN="${WORK}/shinyhub"

cp -R "${ROOT}/testdata/e2e-app" "${WORK}/app"
printf '%s\n' 'onboarding-only-password' > "${WORK}/admin-password"
printf '%s\n' "${TOKEN}" > "${WORK}/deploy-token"
chmod 600 "${WORK}/admin-password" "${WORK}/deploy-token"

echo "==> initializing a fresh server"
(
  cd "${WORK}"
  "${BIN}" init --admin-user onboarding-admin \
    --admin-password-file "${WORK}/admin-password" \
    --config "${WORK}/shinyhub.yaml" --output table
) >/dev/null || fail "server init"

echo "==> starting the fresh server"
(
  cd "${WORK}"
  SHINYHUB_SERVER_HOST=127.0.0.1 \
  SHINYHUB_SERVER_PORT="${PORT}" \
  SHINYHUB_SHUTDOWN_APPS=stop \
  SHINYHUB_DEPLOY_TOKEN="${TOKEN}" \
    exec "${BIN}" serve --config "${WORK}/shinyhub.yaml"
) >"${WORK}/server.log" 2>&1 &
SERVER_PID=$!
"${ROOT}/scripts/wait-http.sh" "${HOST}/readyz" 30 || fail "server readiness"
kill -0 "${SERVER_PID}" 2>/dev/null || fail "server exited during startup (port ${PORT} may already be in use)"

echo "==> checking and booting the app locally from fresh state"
"${BIN}" doctor "${WORK}/app" --local --output table >"${WORK}/doctor.log" 2>&1 \
  || fail "local doctor"
"${BIN}" run "${WORK}/app" --check \
  --state-dir "${WORK}/local-state" --data-dir "${WORK}/local-data" \
  >"${WORK}/local.log" 2>&1 || fail "local boot"

echo "==> connecting through the headless token-file flow"
"${BIN}" connect "${HOST}" --name clean-room \
  --token-file "${WORK}/deploy-token" --config "${WORK}/client.json" \
  --output table >"${WORK}/connect.log" 2>&1 || fail "connect"
test "$(stat -f '%Lp' "${WORK}/client.json" 2>/dev/null || stat -c '%a' "${WORK}/client.json")" = "600" \
  || fail "client credentials are not mode 0600"

echo "==> verifying the complete deploy path"
"${BIN}" doctor "${WORK}/app" --config "${WORK}/client.json" --output table \
  >"${WORK}/doctor.log" 2>&1 || fail "complete doctor"
grep -q 'READY' "${WORK}/doctor.log" || fail "doctor did not report READY"

echo "==> planning the new app without changing the server"
if "${BIN}" plan "${WORK}/app" --visibility public --detailed-exitcode \
  --config "${WORK}/client.json" --output json \
  >"${WORK}/plan.log" 2>&1; then
  fail "new-app plan should exit 2 under --detailed-exitcode"
else
  plan_code=$?
fi
test "${plan_code}" = "2" || fail "new-app plan exited ${plan_code}, want 2"
grep -q '"action":"create"' "${WORK}/plan.log" || fail "new-app plan did not report create"
grep -q '"exit_code":2' "${WORK}/plan.log" || fail "new-app JSON did not mirror exit code 2"

echo "==> deploying the first app and waiting for health"
"${BIN}" deploy "${WORK}/app" --visibility public --wait \
  --config "${WORK}/client.json" --output table \
  >"${WORK}/deploy.log" 2>&1 || fail "first deploy"
body="$(curl -fsS "${HOST}/app/app/" || true)"
echo "${body}" | grep -q 'shinyhub remote-worker E2E' || fail "deployed app did not serve its body"

echo "==> resolving the deployed app through the headless open flow"
open_result="$("${BIN}" apps open app --no-browser --config "${WORK}/client.json" --output json)" \
  || fail "headless app open"
echo "${open_result}" | grep -Fq '"url":"'"${HOST}"'/app/app/"' \
  || fail "app open did not return the canonical URL"
echo "${open_result}" | grep -Fq '"opened":false' \
  || fail "headless app open did not report opened=false"

echo "==> proving the deployed bundle now plans as unchanged"
"${BIN}" plan "${WORK}/app" --detailed-exitcode \
  --config "${WORK}/client.json" --output table \
  >"${WORK}/plan.log" 2>&1 || fail "unchanged deployment plan"
grep -q 'No content change' "${WORK}/plan.log" || fail "plan did not report unchanged content"
grep -q 'would still record a deployment' "${WORK}/plan.log" \
  || fail "unchanged plan did not explain redeploy behavior"

echo "==> verifying exact update permission"
"${BIN}" doctor --remote --slug app --config "${WORK}/client.json" --output table \
  >"${WORK}/doctor.log" 2>&1 || fail "existing-app doctor"
grep -q 'may deploy updates to existing app' "${WORK}/doctor.log" \
  || fail "doctor did not confirm update permission"

echo "==> proving a broken redeploy is self-diagnosing"
printf '\nraise RuntimeError("onboarding e2e deliberate failure")\n' >> "${WORK}/app/app.py"
if "${BIN}" deploy "${WORK}/app" --config "${WORK}/client.json" --output table \
  >"${WORK}/broken.log" 2>&1; then
  fail "deliberately broken redeploy unexpectedly succeeded"
fi
grep -q 'onboarding e2e deliberate failure' "${WORK}/broken.log" \
  || fail "broken deploy did not surface the app log inline"

echo "ONBOARDING E2E PASS"
