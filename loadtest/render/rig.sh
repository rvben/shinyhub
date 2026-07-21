#!/usr/bin/env bash
# Lifecycle for the render-saturation rig VM.
#
# Boots a husker microVM pinned to RIG_CPUS vCPUs running ShinyHub plus the
# synthetic Shiny app. The browser driver runs on the host, outside this VM,
# so it never consumes the cores under measurement.
set -euo pipefail

VM="${RIG_VM_NAME:-shinyhub-render-rig}"
CPUS="${RIG_CPUS:-2}"
MEM="${RIG_MEMORY_MB:-4096}"
HOST_PORT="${RIG_HOST_PORT:-18080}"
COST_MS="${RENDER_COST_MS:-1300}"
IMAGE="python-3.12-slim-bookworm"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

# The daemon host address is supplied by the environment, never committed.
# Set RIG_DAEMON_HOST to the address the host machine uses to reach the
# husker daemon, e.g. RIG_DAEMON_HOST=203.0.113.10
need_daemon_host() {
  if [ -z "${RIG_DAEMON_HOST:-}" ]; then
    echo "RIG_DAEMON_HOST is required (address the host uses to reach the husker daemon)" >&2
    exit 1
  fi
}

# husker cp on this daemon enforces a 1 MiB max_file_write_bytes policy,
# well under the cross-compiled shinyhub binary (roughly 90 MiB), so the
# binary is split into chunks, copied individually, and reassembled in the
# guest with cat.
copy_binary() {
  local src="$1" dest="$2" chunk_dir part
  chunk_dir="$(mktemp -d)"
  split -b 1000000 "$src" "$chunk_dir/part_"
  echo "==> copying $(basename "$src") in $(ls "$chunk_dir" | wc -l | tr -d ' ') chunks"
  husker exec "$VM" -- rm -f "$dest"
  for part in "$chunk_dir"/part_*; do
    husker cp "$part" "$VM:/tmp/$(basename "$part")" >/dev/null
    husker exec "$VM" -- sh -c "cat /tmp/$(basename "$part") >> $dest && rm -f /tmp/$(basename "$part")"
  done
  rm -rf "$chunk_dir"
}

up() {
  need_daemon_host
  command -v husker >/dev/null 2>&1 || { echo "husker not found on PATH" >&2; exit 1; }

  echo "==> cross-compiling shinyhub for the guest (linux/amd64)"
  ( cd "$REPO_ROOT" && GOWORK=off CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
      go build -o /tmp/shinyhub-rig ./cmd/shinyhub )

  echo "==> booting VM $VM (${CPUS} vCPU, ${MEM} MiB)"
  # --disk-size 8G: the catalog image's default rootfs (270 MiB) has no room
  # for the shinyhub binary (~90 MiB) plus the pip-installed shiny package.
  husker run --name "$VM" --cpus "$CPUS" --memory "$MEM" --disk-size 8G "$IMAGE" >/dev/null
  husker wait "$VM" >/dev/null

  echo "==> provisioning"
  husker exec "$VM" -- mkdir -p /opt/rig/app /opt/rig/data
  copy_binary /tmp/shinyhub-rig /opt/rig/shinyhub
  husker cp "$REPO_ROOT/loadtest/render/app/app.py" "$VM:/opt/rig/app/app.py"
  husker cp "$REPO_ROOT/loadtest/render/app/requirements.txt" "$VM:/opt/rig/app/requirements.txt"
  husker exec "$VM" -- chmod +x /opt/rig/shinyhub
  husker exec "$VM" -- pip install --quiet -r /opt/rig/app/requirements.txt

  echo "==> starting shinyhub"
  # Isolated data dir so the admin bootstrap provisions admin/admin on a
  # clean DB. An existing admin row is never password-reset by the bootstrap.
  husker exec "$VM" -- sh -c "
    cd /opt/rig &&
    SHINYHUB_DB_DSN=/opt/rig/data/shinyhub.db \
    SHINYHUB_APPS_DIR=/opt/rig/data/apps \
    SHINYHUB_AUTH_SECRET=rig-secret-not-for-production-only \
    SHINYHUB_ADMIN_USER=admin SHINYHUB_ADMIN_PASSWORD=admin \
    SHINYHUB_HOST=0.0.0.0 SHINYHUB_PORT=8080 \
    nohup /opt/rig/shinyhub serve > /opt/rig/shinyhub.log 2>&1 &
    sleep 1
  "

  husker port-forward "$VM" add --bind 0.0.0.0 "$HOST_PORT" 8080 >/dev/null

  local base="http://${RIG_DAEMON_HOST}:${HOST_PORT}"
  echo "==> waiting for shinyhub to answer at $base"
  for _ in $(seq 1 60); do
    if curl -fsS -m 3 "$base/api/server-info" >/dev/null 2>&1; then
      echo "$base"
      return 0
    fi
    sleep 2
  done
  echo "shinyhub did not become reachable at $base" >&2
  husker exec "$VM" -- tail -n 40 /opt/rig/shinyhub.log >&2 || true
  exit 1
}

url() {
  need_daemon_host
  echo "http://${RIG_DAEMON_HOST}:${HOST_PORT}"
}

status() {
  husker info "$VM" -o json 2>/dev/null || { echo "VM $VM not found"; exit 1; }
}

down() {
  husker destroy "$VM" --yes >/dev/null 2>&1 || true
  echo "destroyed $VM"
}

case "${1:-}" in
  up) up ;;
  down) down ;;
  url) url ;;
  status) status ;;
  *) echo "usage: rig.sh {up|down|url|status}" >&2; exit 2 ;;
esac
