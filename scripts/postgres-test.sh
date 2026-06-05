#!/usr/bin/env bash
# Spin up a throwaway Postgres 16, run the full Go suite against it, tear down.
# Mirrors the env-gated pattern: the suite reads SHINYHUB_TEST_POSTGRES_DSN.
set -euo pipefail

NAME="shinyhub-pg-test"
PORT="${SHINYHUB_TEST_PG_PORT:-55432}"
PASS="shinyhub"
IMAGE="postgres:16"

cleanup() { docker rm -f "$NAME" >/dev/null 2>&1 || true; }
trap cleanup EXIT

cleanup
docker run -d --name "$NAME" \
  -e POSTGRES_PASSWORD="$PASS" \
  -e POSTGRES_DB=postgres \
  -p "127.0.0.1:${PORT}:5432" \
  "$IMAGE" >/dev/null

echo "waiting for postgres to be ready..."
for i in $(seq 1 60); do
  if docker exec "$NAME" pg_isready -U postgres >/dev/null 2>&1; then
    break
  fi
  sleep 1
  if [ "$i" -eq 60 ]; then echo "postgres did not become ready" >&2; exit 1; fi
done

export SHINYHUB_TEST_POSTGRES_DSN="postgres://postgres:${PASS}@127.0.0.1:${PORT}/postgres?sslmode=disable"
echo "running suite against ${SHINYHUB_TEST_POSTGRES_DSN}"
go test ./... -count=1
