#!/bin/sh
set -eu

random_hex() {
  bytes=$1
  od -An -N "$bytes" -tx1 /dev/urandom | tr -d ' \n'
}

export SHINYHUB_AUTH_SECRET="${SHINYHUB_AUTH_SECRET:-$(random_hex 32)}"
export SHINYHUB_DEPLOY_TOKEN="${SHINYHUB_DEPLOY_TOKEN:-shk_$(random_hex 32)}"
export SHINYHUB_DEPLOY_TOKEN_ROLE=admin
export SHINYHUB_APPS_DIR=/data/apps
export SHINYHUB_APP_DATA_DIR=/data/app-data

mkdir -p /data/apps /data/app-data

bootstrap_admin="bootstrap-$(random_hex 12)"
export SHINYHUB_ADMIN_USER="$bootstrap_admin"
export SHINYHUB_ADMIN_PASSWORD="${SHINYHUB_BOOTSTRAP_ADMIN_PASSWORD:-$(random_hex 32)}"
shinyhub init --config "$SHINYHUB_CONFIG" --admin-user "$bootstrap_admin" --quiet
unset SHINYHUB_ADMIN_USER SHINYHUB_ADMIN_PASSWORD bootstrap_admin

shinyhub serve &
server_pid=$!

cleanup() {
  if [ -n "${proxy_pid:-}" ]; then
    kill -TERM "$proxy_pid" 2>/dev/null || true
    wait "$proxy_pid" 2>/dev/null || true
  fi
  kill -TERM "$server_pid" 2>/dev/null || true
  wait "$server_pid" 2>/dev/null || true
}
trap cleanup INT TERM EXIT

attempt=0
until python -c 'import urllib.request; urllib.request.urlopen("http://127.0.0.1:8081/healthz", timeout=2)' >/dev/null 2>&1; do
  attempt=$((attempt + 1))
  if [ "$attempt" -ge 60 ]; then
    echo "ShinyHub did not become ready" >&2
    exit 1
  fi
  if ! kill -0 "$server_pid" 2>/dev/null; then
    wait "$server_pid"
  fi
  sleep 1
done

viewer_password=${SHINYHUB_DEMO_VIEWER_PASSWORD:-explore-shinyhub-demo}
if ! create_output=$(env -u SHINYHUB_CONFIG \
  SHINYHUB_HOST=http://127.0.0.1:8081 \
  SHINYHUB_TOKEN="$SHINYHUB_DEPLOY_TOKEN" \
  shinyhub users create \
    --username demo-viewer \
    --password "$viewer_password" \
    --role viewer 2>&1); then
  echo "$create_output" | grep -qi 'already exists' || {
    echo "$create_output" >&2
    exit 1
  }
fi

# The identity demo should exercise populated, signed claims rather than show
# placeholders. This demo-only bootstrap is idempotent across container wakes.
python /opt/shinyhub-demo/bootstrap-viewer.py /data/shinyhub.db

env -u SHINYHUB_CONFIG \
SHINYHUB_HOST=http://127.0.0.1:8081 \
SHINYHUB_TOKEN="$SHINYHUB_DEPLOY_TOKEN" \
  shinyhub fleet apply --prune --yes --file /opt/shinyhub-demo/fleet.toml

caddy run --config /opt/shinyhub-demo/Caddyfile --adapter caddyfile &
proxy_pid=$!
wait "$proxy_pid"
