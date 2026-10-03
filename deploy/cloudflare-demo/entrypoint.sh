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
export SHINYHUB_APP_CACHE_DIR=/data/app-cache

mkdir -p /data/apps /data/app-data /data/app-cache

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

# A restored database can retain the previous owner's lease. Liveness alone
# does not admit bootstrap mutations while that control-plane handoff finishes.
attempt=0
until python -c 'import urllib.request; urllib.request.urlopen("http://127.0.0.1:8081/activez", timeout=2)' >/dev/null 2>&1; do
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

# Fleet reconciliation can finish before framework processes accept requests.
# Keep the externally probed port closed until every public application serves.
# Run this on every boot, including restores; no persisted ready marker is used.
python - "$server_pid" /opt/shinyhub-demo/fleet.toml <<'PY_FLEET_READY'
import concurrent.futures
import os
import sys
import time
import tomllib
import urllib.error
import urllib.parse
import urllib.request


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


server_pid = int(sys.argv[1])
with open(sys.argv[2], "rb") as manifest:
    slugs = [app["slug"] for app in tomllib.load(manifest)["app"]]
if not slugs:
    raise SystemExit("Demo fleet has no applications")
app_host = urllib.parse.urlsplit(os.environ.get(
    "SHINYHUB_APP_ORIGIN", "https://apps.demo.shinyhub.dev"
)).netloc
deadline = time.monotonic() + 60


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
            raise SystemExit("ShinyHub exited before its applications became ready")
        pending = [slug for slug, healthy in zip(slugs, pool.map(ready, slugs)) if not healthy]
        if not pending:
            break
        if time.monotonic() >= deadline:
            raise SystemExit("Demo applications did not become ready: " + ", ".join(pending))
        time.sleep(1)
PY_FLEET_READY

caddy run --config /opt/shinyhub-demo/Caddyfile --adapter caddyfile &
proxy_pid=$!
wait "$proxy_pid"
