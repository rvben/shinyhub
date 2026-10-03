#!/bin/sh
set -eu

SHINYHUB_DEMO_BOOT_STARTED=$(python -c 'import time; print(time.monotonic())')
export SHINYHUB_DEMO_BOOT_STARTED
boot_phase=initialize
boot_ready=false
boot_failure_reason=command_failed

boot_log() {
  python - "$1" "$boot_phase" "$2" "$boot_failure_reason" <<'PY_BOOT_LOG'
import json
import os
import sys
import time

print(json.dumps({
    "event": sys.argv[1],
    "phase": sys.argv[2],
    "elapsed_ms": round((time.monotonic() - float(os.environ["SHINYHUB_DEMO_BOOT_STARTED"])) * 1000),
    "exit_code": int(sys.argv[3]),
    "reason": sys.argv[4] if sys.argv[1] == "demo_boot_failed" else None,
}), flush=True)
PY_BOOT_LOG
}

cleanup() {
  boot_exit=$?
  trap - EXIT INT TERM
  if [ "$boot_ready" = false ]; then
    boot_log demo_boot_failed "$boot_exit" || true
  fi
  if [ -n "${proxy_pid:-}" ]; then
    kill -TERM "$proxy_pid" 2>/dev/null || true
    wait "$proxy_pid" 2>/dev/null || true
  fi
  if [ -n "${server_pid:-}" ]; then
    kill -TERM "$server_pid" 2>/dev/null || true
    wait "$server_pid" 2>/dev/null || true
  fi
  exit "$boot_exit"
}
trap cleanup EXIT
trap 'boot_failure_reason=interrupted; exit 130' INT
trap 'boot_failure_reason=interrupted; exit 143' TERM
boot_log demo_boot_started 0 || true

random_hex() {
  bytes=$1
  od -An -N "$bytes" -tx1 /dev/urandom | tr -d ' \n'
}

export SHINYHUB_AUTH_SECRET="${SHINYHUB_AUTH_SECRET:-$(random_hex 32)}"
export SHINYHUB_DEPLOY_TOKEN="${SHINYHUB_DEPLOY_TOKEN:-shk_$(random_hex 32)}"
export SHINYHUB_DEPLOY_TOKEN_ROLE=admin
export SHINYHUB_APPS_DIR=/data/apps
export SHINYHUB_APP_DATA_DIR=/data/app-data
export SHINYHUB_APP_CACHE_DIR=/data/app-cache

mkdir -p /data/apps /data/app-data /data/app-cache

bootstrap_admin="bootstrap-$(random_hex 12)"
export SHINYHUB_ADMIN_USER="$bootstrap_admin"
export SHINYHUB_ADMIN_PASSWORD="${SHINYHUB_BOOTSTRAP_ADMIN_PASSWORD:-$(random_hex 32)}"
shinyhub init --config "$SHINYHUB_CONFIG" --admin-user "$bootstrap_admin" --quiet
unset SHINYHUB_ADMIN_USER SHINYHUB_ADMIN_PASSWORD bootstrap_admin

shinyhub serve &
server_pid=$!

# A restored database can retain the previous owner's lease. Liveness alone
# does not admit bootstrap mutations while that control-plane handoff finishes.
attempt=0
boot_phase=control_plane
until python -c 'import urllib.request; urllib.request.urlopen("http://127.0.0.1:8081/activez", timeout=2)' >/dev/null 2>&1; do
  attempt=$((attempt + 1))
  if [ "$attempt" -ge 60 ]; then
    boot_failure_reason=timeout
    echo "ShinyHub did not become ready" >&2
    exit 1
  fi
  if ! kill -0 "$server_pid" 2>/dev/null; then
    boot_failure_reason=server_exited
    wait "$server_pid"
    exit 1
  fi
  sleep 1
done

viewer_password=${SHINYHUB_DEMO_VIEWER_PASSWORD:-explore-shinyhub-demo}
boot_phase=viewer
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

boot_phase=fleet_apply
env -u SHINYHUB_CONFIG \
SHINYHUB_HOST=http://127.0.0.1:8081 \
SHINYHUB_TOKEN="$SHINYHUB_DEPLOY_TOKEN" \
  shinyhub fleet apply --prune --yes --file /opt/shinyhub-demo/fleet.toml

# Fleet reconciliation can finish before framework processes accept requests.
# Keep the externally probed port closed until every public application serves.
# Run this on every boot, including restores; no persisted ready marker is used.
boot_phase=fleet_readiness
python - "$server_pid" /opt/shinyhub-demo/fleet.toml <<'PY_FLEET_READY'
import concurrent.futures
import json
import os
import sys
import time
import tomllib
import urllib.error
import urllib.parse
import urllib.request

gate_started = time.monotonic()
boot_started = float(os.environ.get("SHINYHUB_DEMO_BOOT_STARTED", gate_started))


def log_event(event, **fields):
    now = time.monotonic()
    print(json.dumps({"event": event, "elapsed_ms": round((now - boot_started) * 1000),
                      "fleet_wait_ms": round((now - gate_started) * 1000), **fields}), flush=True)


def fail(reason, message, pending=None):
    log_event("demo_fleet_readiness_failed", reason=reason, pending=pending or [])
    raise SystemExit(message)


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


server_pid = int(sys.argv[1])
with open(sys.argv[2], "rb") as manifest:
    slugs = [app["slug"] for app in tomllib.load(manifest)["app"]]
if not slugs:
    fail("empty_fleet", "Demo fleet has no applications")
app_host = urllib.parse.urlsplit(os.environ.get(
    "SHINYHUB_APP_ORIGIN", "https://apps.demo.shinyhub.dev"
)).netloc
deadline = gate_started + 60


def ready(slug):
    request = urllib.request.Request(
        "http://127.0.0.1:8081/app/" + urllib.parse.quote(slug, safe="") + "/",
        headers={"Host": app_host, "X-Forwarded-Proto": "https", "Accept": "application/octet-stream",
                 "User-Agent": "demo-startup-check/1"},
    )
    try:
        # Legacy/deploying browser wait pages can return 200. Reject their
        # shared shell as well as the proxy's explicit rejection header.
        with urllib.request.build_opener(NoRedirect()).open(request, timeout=2) as response:
            return (response.status == 200
                    and not response.headers.get("X-Shinyhub-Reject")
                    and b'id="shinyhub-box"' not in response.read(262144))
    except urllib.error.HTTPError as error:
        error.close()
        return False
    except (urllib.error.URLError, TimeoutError, OSError):
        return False


with concurrent.futures.ThreadPoolExecutor(max_workers=len(slugs)) as pool:
    while True:
        try:
            os.kill(server_pid, 0)
        except ProcessLookupError:
            fail("server_exited", "ShinyHub exited before its applications became ready", slugs)
        pending = [slug for slug, healthy in zip(slugs, pool.map(ready, slugs)) if not healthy]
        if not pending:
            log_event("demo_fleet_ready", application_count=len(slugs))
            break
        if time.monotonic() >= deadline:
            fail("timeout", "Demo applications did not become ready: " + ", ".join(pending), pending)
        time.sleep(1)
PY_FLEET_READY
boot_phase=proxy

caddy run --config /opt/shinyhub-demo/Caddyfile --adapter caddyfile &
proxy_pid=$!
boot_ready=true
wait "$proxy_pid"
