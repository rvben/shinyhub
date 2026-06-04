#!/usr/bin/env bash
# Smoke test for the deploy-shinyhub skill.
#
# Stands up a ShinyHub server and deploys the skill's example app exactly as
# skills/deploy-shinyhub/SKILL.md documents, then asserts the app is served
# through the proxy. This is the anti-rot guard: if the CLI flags, env vars, or
# deploy flow drift from what the skill claims, this fails.
#
# The example app installs `shiny` via `uv run --with-requirements` on first
# start, so this needs `uv`, a Python, and network access. When `uv` is absent
# the test SKIPs (exit 0) so offline/local `make` runs stay green; CI installs
# uv so it runs for real. This mirrors how the Docker/Fargate e2e tests gate on
# their prerequisite (a Docker daemon / a real ECS cluster) being present.
#
# Hermetic: a temp working directory holds the db, bundles, config, and creds,
# and the server is configured with shutdown_apps=stop so it reaps the app
# process on exit. SKILL_SMOKE_KEEP preserves the workdir for debugging.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
APP_DIR="${ROOT}/skills/deploy-shinyhub/example-app"
PORT="${SKILL_SMOKE_PORT:-8137}"
SLUG="skill-smoke-demo"
ADMIN_USER="admin"
ADMIN_PASS="skill-smoke-admin-pw"
HOST="http://127.0.0.1:${PORT}"

skip() { echo "SKILL-SMOKE SKIP: $*" >&2; exit 0; }
fail() { echo "SKILL-SMOKE FAIL: $*" >&2; exit 1; }

command -v uv >/dev/null 2>&1 || skip "uv not installed; the example app needs uv to start (https://docs.astral.sh/uv/). Offline-safe skip."
[ -f "${APP_DIR}/app.py" ] || fail "example app missing at ${APP_DIR}/app.py"

WORKDIR="$(mktemp -d)"
BIN="${WORKDIR}/shinyhub"
CONFIG="${WORKDIR}/shinyhub.yaml"
CREDS="${WORKDIR}/creds.json"
LOG="${WORKDIR}/server.log"
CP_PID=""

cleanup() {
  if [ -n "${CP_PID}" ]; then
    # shutdown_apps=stop makes the server stop the app on SIGTERM; the explicit
    # apps-stop is belt-and-suspenders so no uv/shiny child is left orphaned.
    "${BIN}" apps stop "${SLUG}" --config "${CREDS}" >/dev/null 2>&1 || true
    kill "${CP_PID}" 2>/dev/null || true
    wait "${CP_PID}" 2>/dev/null || true
  fi
  if [ -n "${SKILL_SMOKE_KEEP:-}" ]; then
    echo "SKILL_SMOKE_KEEP set; artifacts kept at ${WORKDIR}" >&2
  else
    rm -rf "${WORKDIR}"
  fi
}
trap cleanup EXIT

if curl -fsS -o /dev/null "${HOST}/api/server-info" 2>/dev/null; then
  fail "something is already listening on ${HOST} (set SKILL_SMOKE_PORT to a free port)"
fi

echo "==> build"
GOWORK=off go build -o "${BIN}" "${ROOT}/cmd/shinyhub" || fail "build"

echo "==> write server config"
cat >"${CONFIG}" <<YAML
database:
  driver: sqlite
  dsn: ${WORKDIR}/shinyhub.db
server:
  host: 127.0.0.1
  port: ${PORT}
  shutdown_apps: stop
auth:
  secret: skill-smoke-secret-0123456789abcdef0123456789
storage:
  apps_dir: ${WORKDIR}/apps
  app_data_dir: ${WORKDIR}/app-data
YAML

echo "==> start server"
SHINYHUB_ADMIN_USER="${ADMIN_USER}" SHINYHUB_ADMIN_PASSWORD="${ADMIN_PASS}" \
  "${BIN}" serve --config "${CONFIG}" >"${LOG}" 2>&1 &
CP_PID=$!

echo "==> wait for server ready"
"${ROOT}/scripts/wait-http.sh" "${HOST}/api/server-info" 30 || { cat "${LOG}" >&2; fail "server did not become ready"; }

echo "==> login (as documented: --host, username/password)"
"${BIN}" login --host "${HOST}" --username "${ADMIN_USER}" --password "${ADMIN_PASS}" --config "${CREDS}" \
  || fail "login"

echo "==> deploy the skill's example app"
"${BIN}" deploy "${APP_DIR}" --slug "${SLUG}" --wait --wait-timeout 300 --config "${CREDS}" \
  || { "${BIN}" apps logs "${SLUG}" --no-follow --config "${CREDS}" >&2 2>/dev/null || true; fail "deploy"; }

echo "==> assert app is running"
"${BIN}" apps list --config "${CREDS}" | grep -Eq "^${SLUG}[[:space:]]+running" \
  || { "${BIN}" apps list --config "${CREDS}" >&2; fail "app not running"; }

echo "==> assert private app returns 401 unauthenticated (access control)"
code="$(curl -s -o /dev/null -w '%{http_code}' "${HOST}/app/${SLUG}/")"
[ "${code}" = "401" ] || fail "expected 401 for private app unauthenticated, got ${code}"

echo "==> authenticate via /api/auth/session and assert the proxy serves 200"
JAR="${WORKDIR}/jar.txt"
curl -fsS -c "${JAR}" -X POST "${HOST}/api/auth/session" \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"${ADMIN_USER}\",\"password\":\"${ADMIN_PASS}\"}" -o /dev/null \
  || fail "session login"
BODY="${WORKDIR}/body.html"
code="$(curl -s -b "${JAR}" -o "${BODY}" -w '%{http_code}' "${HOST}/app/${SLUG}/")"
[ "${code}" = "200" ] || { cat "${LOG}" >&2; fail "expected 200 for authenticated app, got ${code}"; }
grep -qi "shiny" "${BODY}" || fail "served page does not look like a Shiny app"

echo "SKILL-SMOKE PASS: ${SLUG} deployed and served HTTP 200 through the proxy"
