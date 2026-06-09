#!/usr/bin/env bash
# Spin up a throwaway Postgres 16, run the kill-the-active HA integration test
# against it (two real shinyhub processes + SIGKILL), tear down. Distinct
# container name/port from postgres-test.sh so the two can run concurrently.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
NAME="shinyhub-pg-ha-test"
PORT="${SHINYHUB_HA_TEST_PG_PORT:-55433}"
PASS="shinyhub"
IMAGE="postgres:16"

cleanup() { docker rm -f "$NAME" >/dev/null 2>&1 || true; }
trap cleanup EXIT

cleanup
docker run -d --name "$NAME" \
  -e POSTGRES_PASSWORD="$PASS" \
  -e POSTGRES_DB=postgres \
  -p "127.0.0.1:${PORT}:5432" \
  "$IMAGE" postgres -c max_connections=500 >/dev/null

echo "waiting for postgres to be ready..."
for i in $(seq 1 60); do
  if docker exec "$NAME" pg_isready -U postgres >/dev/null 2>&1; then break; fi
  sleep 1
  if [ "$i" -eq 60 ]; then echo "postgres did not become ready" >&2; exit 1; fi
done

export SHINYHUB_TEST_POSTGRES_DSN="postgres://postgres:${PASS}@127.0.0.1:${PORT}/postgres?sslmode=disable"
export GOWORK=off
echo "running kill-the-active test against ${SHINYHUB_TEST_POSTGRES_DSN}"
go test -tags=integration -run TestKillTheActive "$ROOT/cmd/shinyhub/..." -count=1 -v -timeout 5m
