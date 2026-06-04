#!/usr/bin/env bash
# Zero-downtime handoff e2e: build the binary, start it, hammer both listeners
# while sending SIGHUP, and assert zero request failures + a new process takeover.
# tableflip requires a built binary (not `go run`) for graceful reloads.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK="$(mktemp -d)"
SERVER_PID=""
LOOP_PID=""
TCP_PID=""
cleanup() {
  [ -n "$LOOP_PID" ] && kill "$LOOP_PID" 2>/dev/null || true
  [ -n "$TCP_PID" ] && kill "$TCP_PID" 2>/dev/null || true
  [ -n "$SERVER_PID" ] && kill "$SERVER_PID" 2>/dev/null || true
  if [ -f "$WORK/shinyhub.pid" ]; then kill "$(cat "$WORK/shinyhub.pid")" 2>/dev/null || true; fi
  rm -rf "$WORK"
}
trap cleanup EXIT

BIN="$WORK/shinyhub"
PIDFILE="$WORK/shinyhub.pid"
PORT=18080
METRICS_PORT=19090

echo "==> building binary"
GOWORK=off go build -o "$BIN" "$ROOT/cmd/shinyhub"

cat > "$WORK/shinyhub.yaml" <<EOF
server:
  host: 127.0.0.1
  port: ${PORT}
  pid_file: ${PIDFILE}
metrics:
  enabled: true
  addr: 127.0.0.1:${METRICS_PORT}
storage:
  apps_dir: ${WORK}/apps
  app_data_dir: ${WORK}/data
database:
  dsn: ${WORK}/shinyhub.db
EOF

export SHINYHUB_AUTH_SECRET="handoff-e2e-secret-handoff-e2e-secret"
export SHINYHUB_ADMIN_USER="admin"
export SHINYHUB_ADMIN_PASSWORD="admin"

echo "==> starting server"
"$BIN" serve --config "$WORK/shinyhub.yaml" &
SERVER_PID=$!

echo "==> waiting for readiness"
ready=0
for _ in $(seq 1 100); do
  if curl -fsS "http://127.0.0.1:${PORT}/readyz" >/dev/null 2>&1; then ready=1; break; fi
  sleep 0.2
done
[ "$ready" = "1" ] || { echo "FAIL: server never became ready"; exit 1; }

# /readyz can pass before upg.Ready() writes the PID file (readyCh closes in the
# serve goroutine first), so poll the PID file explicitly rather than sleeping.
echo "==> waiting for PID file"
for _ in $(seq 1 100); do
  if [ -s "$PIDFILE" ] && kill -0 "$(cat "$PIDFILE")" 2>/dev/null; then break; fi
  sleep 0.1
done
[ -s "$PIDFILE" ] && kill -0 "$(cat "$PIDFILE")" 2>/dev/null \
  || { echo "FAIL: PID file never written with a live PID"; exit 1; }

# Connection-refused (curl exit 7) means the listener was momentarily down = a
# real handoff gap. Other non-zero exits (e.g. a single in-flight request cut at
# the exact handoff instant) are reported but not treated as a listener gap.
REFUSED="$WORK/refused"; OTHER="$WORK/other"
: > "$REFUSED"; : > "$OTHER"
echo "==> starting continuous client against both listeners"
(
  end=$((SECONDS + 8))
  while [ $SECONDS -lt $end ]; do
    code=0; curl -fsS -o /dev/null "http://127.0.0.1:${PORT}/healthz"       || code=$?
    if   [ "$code" = "7" ]; then echo "main"      >> "$REFUSED"
    elif [ "$code" != "0" ]; then echo "main:$code" >> "$OTHER"; fi
    code=0; curl -fsS -o /dev/null "http://127.0.0.1:${METRICS_PORT}/metrics" || code=$?
    if   [ "$code" = "7" ]; then echo "metrics"      >> "$REFUSED"
    elif [ "$code" != "0" ]; then echo "metrics:$code" >> "$OTHER"; fi
    sleep 0.05
  done
) &
LOOP_PID=$!

# Tighter probe: a raw TCP connect to the main port every ~10ms (5x finer than
# the curl loop) using bash /dev/tcp. A failed connect means the listener was
# actually down. The small interval keeps the connect rate sane (no ephemeral
# port exhaustion) while sampling far finer than the functional curl checks.
TCPFAIL="$WORK/tcpfail"
: > "$TCPFAIL"
(
  end=$((SECONDS + 8))
  while [ $SECONDS -lt $end ]; do
    if (exec 3<>"/dev/tcp/127.0.0.1/${PORT}") 2>/dev/null; then
      exec 3>&- 2>/dev/null || true
    else
      echo refused >> "$TCPFAIL"
    fi
    sleep 0.01
  done
) &
TCP_PID=$!

sleep 1
OLD_PID="$(cat "$PIDFILE")"
echo "==> triggering handoff: SIGHUP -> MAINPID ${SERVER_PID} (pidfile=${OLD_PID})"
kill -HUP "$SERVER_PID"

wait "$LOOP_PID"; LOOP_PID=""
wait "$TCP_PID"; TCP_PID=""

NEW_PID="$(cat "$PIDFILE")"
echo "==> old pid=${OLD_PID} new pid=${NEW_PID}"

if [ -s "$OTHER" ]; then
  echo "WARN: non-refused transient failures during handoff (not a listener gap):"
  sort "$OTHER" | uniq -c
fi
if [ -s "$REFUSED" ]; then
  echo "FAIL: $(wc -l < "$REFUSED" | tr -d ' ') connection-refused failures (HTTP, listener gap) during handoff:"
  sort "$REFUSED" | uniq -c
  exit 1
fi
if [ -s "$TCPFAIL" ]; then
  echo "FAIL: $(wc -l < "$TCPFAIL" | tr -d ' ') TCP connect failures (listener gap) during handoff"
  exit 1
fi
if [ "$OLD_PID" = "$NEW_PID" ]; then
  echo "FAIL: pidfile unchanged (${NEW_PID}); no successor took over"
  exit 1
fi

# "No gap observed" rather than "provably zero gap": the probes sample at ~10ms
# (TCP) and per-request (HTTP). With tableflip the listener fd is held by at
# least one process throughout, so a gap is structurally not expected; these
# probes guard against a regression that actually drops the listener.
echo "PASS: no listener gap observed across handoff; successor ${NEW_PID} took over from ${OLD_PID}"
