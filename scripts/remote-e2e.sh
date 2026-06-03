#!/usr/bin/env bash
# End-to-end test for remote Docker worker scaling and recovery.
#
# Launches a control plane and a separate `shinyhub worker` process against the
# local Docker daemon, deploys a Shiny app onto the remote tier with two
# replicas, and asserts the full data path plus the recovery behaviors:
#
#   - the worker joins over mTLS (trusting the control plane's internal CA),
#   - a deploy onto the remote tier pulls the bundle exactly once across both
#     replicas (the bundle cache dedups the second replica's start),
#   - user traffic routes through the agent mTLS tunnel to the container,
#   - a control-plane restart re-adopts the running replica via agent inventory,
#   - killing the worker transitions its replica to lost and drops routing.
#
# Fully local and hermetic; CI runs it via `make test-remote-e2e`. Requires a
# working Docker daemon (the remote tier launches real containers).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORKDIR="$(mktemp -d)"

CP_PID=""
WORKER_PID=""

cleanup() {
  [ -n "${WORKER_PID}" ] && kill "${WORKER_PID}" 2>/dev/null || true
  [ -n "${CP_PID}" ] && kill "${CP_PID}" 2>/dev/null || true
  docker ps -aq --filter label=shinyhub.managed=true | xargs -r docker rm -f >/dev/null 2>&1 || true
  # E2E_KEEP preserves the work directory (logs, config, db) for debugging.
  [ -n "${E2E_KEEP:-}" ] && { echo "E2E_KEEP set; logs kept at ${WORKDIR}" >&2; return; }
  rm -rf "${WORKDIR}"
}
trap cleanup EXIT

fail() { echo "E2E FAIL: $*" >&2; exit 1; }

command -v docker >/dev/null 2>&1 || fail "docker is required for the remote-worker E2E"
docker info >/dev/null 2>&1 || fail "docker daemon is not reachable"

# Ports for the three listeners: the user/API server, the control plane's
# worker-facing mTLS API, and the worker's data-plane tunnel.
CP_PORT=8099
WORKER_API_PORT=8443
DATA_PORT=9443

# Advertise the worker on a non-loopback address when one is available so the
# control-plane -> worker tunnel traverses the host network stack rather than
# loopback; fall back to loopback so the test still runs on hosts without a
# routable interface. The container port stays bound to 127.0.0.1 on the worker
# either way, so the tunnel and the container publish path remain distinct.
ADVERTISE_IP="$(ipconfig getifaddr en0 2>/dev/null || true)"
[ -n "${ADVERTISE_IP}" ] || ADVERTISE_IP="$(hostname -I 2>/dev/null | awk '{print $1}')"
[ -n "${ADVERTISE_IP}" ] || ADVERTISE_IP="127.0.0.1"

DEPLOY_TOKEN="e2e-deploy-token-0123456789abcdef"
export SHINYHUB_HOST="http://127.0.0.1:${CP_PORT}"
export SHINYHUB_TOKEN="${DEPLOY_TOKEN}"

# 1. Build the binary once.
GOWORK=off go build -o "${WORKDIR}/shinyhub" "${ROOT}/cmd/shinyhub" || fail "build"
BIN="${WORKDIR}/shinyhub"

# 2. Write a control-plane config with a remote tier and a join token.
echo "e2e-join-token-0123456789abcdef" > "${WORKDIR}/join-token"
cat > "${WORKDIR}/shinyhub.yaml" <<YAML
server:
  host: 127.0.0.1
  port: ${CP_PORT}
database:
  dsn: ${WORKDIR}/shinyhub.db
auth:
  secret: e2e-secret-please-change-me-0123456789
storage:
  apps_dir: ${WORKDIR}/apps
  app_data_dir: ${WORKDIR}/app-data
runtime:
  mode: native
  tiers:
    - name: local
      runtime: native
    - name: remote
      runtime: remote_docker
worker:
  enabled: true
  join_token_file: ${WORKDIR}/join-token
  ca_dir: ${WORKDIR}/ca
  listen_addr: 127.0.0.1:${WORKER_API_PORT}
  advertise_hosts:
    - 127.0.0.1
YAML

start_cp() {
  SHINYHUB_DEPLOY_TOKEN="${DEPLOY_TOKEN}" "${BIN}" serve --config "${WORKDIR}/shinyhub.yaml" \
    >>"${WORKDIR}/cp.log" 2>&1 &
  CP_PID=$!
  "${ROOT}/scripts/wait-http.sh" "http://127.0.0.1:${CP_PORT}/healthz" 30 || fail "control plane did not start"
}

# 3. Start the control plane (this also generates the CA under ca_dir).
start_cp

# 4. Start the worker, trusting the control plane's CA and advertising its
#    data-plane address. The worker connects to the control plane's worker-API
#    listener over mTLS.
"${BIN}" worker \
  --server "https://127.0.0.1:${WORKER_API_PORT}" \
  --token "$(cat "${WORKDIR}/join-token")" \
  --advertise-addr "${ADVERTISE_IP}:${DATA_PORT}" \
  --tier remote \
  --ca-file "${WORKDIR}/ca/ca-cert.pem" \
  --data-dir "${WORKDIR}/worker-data" \
  >"${WORKDIR}/worker.log" 2>&1 &
WORKER_PID=$!

# 5. Assert: the worker joins the control plane.
"${ROOT}/scripts/wait-log.sh" "${WORKDIR}/worker.log" "worker joined control plane" 30 \
  || fail "worker did not join"

# 5b. Assert: the worker's data-plane listener is bound and it is up (routable).
#     Registration marks a worker "joining" (not routable); its first heartbeat,
#     sent only after the listener binds, promotes it to up and emits this signal.
#     Gating the deploy on readiness (not just "joined") avoids dialing the
#     worker before its listener exists -- the connection-refused race this test
#     used to hit deterministically.
"${ROOT}/scripts/wait-log.sh" "${WORKDIR}/worker.log" "worker data-plane ready" 30 \
  || fail "worker data-plane did not become ready"

# 6. Create the app (public) and place two replicas on the remote tier before
#    deploying, so the only deploy lands on the worker.
curl -fsS -X POST "${SHINYHUB_HOST}/api/apps" \
  -H "Authorization: Token ${DEPLOY_TOKEN}" -H "Content-Type: application/json" \
  -d '{"slug":"e2eapp","name":"e2eapp","access":"public"}' >/dev/null || fail "create app"
curl -fsS -X PATCH "${SHINYHUB_HOST}/api/apps/e2eapp" \
  -H "Authorization: Token ${DEPLOY_TOKEN}" -H "Content-Type: application/json" \
  -d '{"placement":{"remote":2}}' >/dev/null || fail "set placement"

# 7. Deploy the app bundle. --wait blocks until both replicas are healthy
#    (first-run dependency installs in the container can take minutes).
"${BIN}" deploy "${ROOT}/testdata/e2e-app" --slug e2eapp --wait --wait-timeout 300 \
  >"${WORKDIR}/deploy.log" 2>&1 || { cat "${WORKDIR}/deploy.log" >&2; fail "deploy"; }

# 8. Assert: the bundle was pulled exactly once across both replicas (the cache
#    dedups the second replica's start).
pulls="$(grep -c 'bundle: pulled' "${WORKDIR}/worker.log" || true)"
test "${pulls}" = "1" || fail "expected exactly one bundle pull, got ${pulls}"

# 9. Assert: user traffic routes through the agent mTLS tunnel to the container.
body="$(curl -fsS "http://127.0.0.1:${CP_PORT}/app/e2eapp/")" || fail "routing through tunnel"
echo "${body}" | grep -q "shinyhub remote-worker E2E" || fail "tunnel did not return the app body"

# 10. Assert: a control-plane restart re-adopts the running replica via agent
#     inventory, and routing survives the restart.
kill "${CP_PID}"; wait "${CP_PID}" 2>/dev/null || true; CP_PID=""
start_cp
"${ROOT}/scripts/wait-log.sh" "${WORKDIR}/cp.log" "recovery: re-adopted remote replica" 30 \
  || fail "control plane did not re-adopt the replica after restart"
curl -fsS "http://127.0.0.1:${CP_PORT}/app/e2eapp/" >/dev/null || fail "routing after control-plane restart"

# 11. Assert: killing the worker transitions its replica to lost and drops it
#     from routing. The control plane marks a worker down once its heartbeat is
#     stale (90s timeout, 30s sweep), so allow generous time. Once the pool has
#     no live replicas the proxy serves its loading page (HTTP 200), so assert
#     the app body is no longer served rather than expecting a non-2xx status.
kill "${WORKER_PID}"; wait "${WORKER_PID}" 2>/dev/null || true; WORKER_PID=""
"${ROOT}/scripts/wait-log.sh" "${WORKDIR}/cp.log" "lose replica" 150 \
  || fail "replica was not marked lost after the worker died"
lost_body="$(curl -fsS "http://127.0.0.1:${CP_PORT}/app/e2eapp/" || true)"
if echo "${lost_body}" | grep -q "shinyhub remote-worker E2E"; then
  fail "lost replica is still serving the app body"
fi

echo "E2E PASS"
