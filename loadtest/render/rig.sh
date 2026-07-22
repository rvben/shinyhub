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
IMAGE="python-3.12-slim-bookworm"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

# RENDER_COST_MS is deliberately NOT read here. up() boots the VM and starts
# ShinyHub before the synthetic app is deployed, so there is no app process
# yet whose environment this script could set; a variable read at this point
# could only be forwarded nowhere. app.py reads RENDER_COST_MS from its own
# process environment (default 1300 ms), and ShinyHub gives every deployed
# app a per-app environment store that is applied at process start. So the
# real, working way to tune the render cost is after deploy, via the
# already-authenticated CLI:
#   ./bin/shiny env set rig RENDER_COST_MS=<ms> --restart
# See loadtest/render/README.md for the empirical check that this reaches
# the app process.

# Host-side scratch directory created by copy_binary for its chunk splitting.
# Script-scope, not local to copy_binary, so up()'s handle_up_exit trap can
# find and remove it if copy_binary aborts mid-transfer without returning
# normally. See the comment in copy_binary for why that case exists.
CHUNK_DIR=""

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
  local src="$1" dest="$2" part
  CHUNK_DIR="$(mktemp -d)"
  # CHUNK_DIR is a script-scope variable rather than a local so up()'s
  # handle_up_exit trap can find and remove it. That matters because this
  # function only cleans up after itself on the two paths below that reach a
  # real "return": falling off the end (success) and the explicit "return 1"
  # on checksum mismatch. A bare command failing under set -e, such as split,
  # husker cp, or husker exec inside the loop below, aborts the whole script
  # immediately instead of returning from this function, so neither inline
  # cleanup runs; handle_up_exit is what removes CHUNK_DIR in that case.

  split -b 1000000 "$src" "$CHUNK_DIR/part_"
  echo "==> copying $(basename "$src") in $(ls "$CHUNK_DIR" | wc -l | tr -d ' ') chunks"
  husker exec "$VM" -- rm -f "$dest"
  for part in "$CHUNK_DIR"/part_*; do
    husker cp "$part" "$VM:/tmp/$(basename "$part")" >/dev/null
    husker exec "$VM" -- sh -c "cat /tmp/$(basename "$part") >> $dest && rm -f /tmp/$(basename "$part")" >/dev/null
  done
  # A chunk whose cat/rm failed mid-loop above would leave its /tmp/part_*
  # behind on the guest, but nothing here sweeps for that: on any failure the
  # whole VM is destroyed by up()'s handle_up_exit trap, taking every stray
  # guest-side /tmp file with it. That makes guest-side leakage moot rather
  # than something this script needs to actively clean up.

  echo "==> verifying transferred binary checksum"
  local src_sum guest_sum
  # macOS has no sha256sum; shasum -a 256 is the equivalent. Both tools print
  # "<hex digest>  <filename>", differently padded, so only the hex field is
  # compared, extracted by pattern rather than by column to stay robust to
  # either tool's exact spacing.
  if command -v sha256sum >/dev/null 2>&1; then
    src_sum="$(sha256sum "$src" | grep -oE '[0-9a-f]{64}' | head -1)" || true
  else
    src_sum="$(shasum -a 256 "$src" | grep -oE '[0-9a-f]{64}' | head -1)" || true
  fi
  guest_sum="$(husker exec "$VM" -o text -- sha256sum "$dest" | grep -oE '[0-9a-f]{64}' | head -1)" || true
  if [ -z "$src_sum" ] || [ "$src_sum" != "$guest_sum" ]; then
    echo "checksum mismatch after transferring $(basename "$src"): host=${src_sum:-<empty>} guest=${guest_sum:-<empty>}" >&2
    rm -rf "$CHUNK_DIR"
    CHUNK_DIR=""
    return 1
  fi
  echo "==> checksum verified ($src_sum)"
  rm -rf "$CHUNK_DIR"
  CHUNK_DIR=""
}

# EXIT trap installed by up() once the VM exists on the shared daemon (see
# below). It is disarmed via "trap - EXIT" right before a successful return,
# so in practice this only ever runs on a provisioning failure. It reuses
# down() instead of calling husker destroy directly, so the true outcome
# (destroyed, already absent, or genuinely failed) is reported rather than
# assumed. The triggering exit status is captured first, as the very first
# statement, and re-applied with an explicit "exit" at the end: without that,
# the trap's own commands (down, rm) would determine the script's final exit
# status instead of the provisioning failure that caused the trap to fire.
handle_up_exit() {
  local exit_status=$?
  echo "==> provisioning failed, destroying $VM" >&2
  local destroy_out
  if destroy_out="$(down 2>&1)"; then
    echo "$destroy_out" >&2
  else
    echo "WARNING: automatic teardown of $VM failed; run 'husker destroy $VM --yes' manually to avoid leaving it running on the shared daemon" >&2
    echo "$destroy_out" >&2
  fi
  if [ -n "$CHUNK_DIR" ]; then
    rm -rf "$CHUNK_DIR"
    CHUNK_DIR=""
  fi
  exit "$exit_status"
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
  # Guard the assignment with if/else, not a bare "run_out=$(...); run_status=$?"
  # pair: under set -e a bare failing assignment aborts the script before
  # run_status=$? ever runs. A leftover VM of this name from a prior `up`
  # (never destroyed by `down`) is a real, observed failure mode here, and
  # husker's raw JSON error for it is worth translating into a plain message
  # that points at the fix, rather than leaking husker internals.
  local run_out run_status
  if run_out="$(husker run --name "$VM" --cpus "$CPUS" --memory "$MEM" --disk-size 8G "$IMAGE" 2>&1)"; then
    :
  else
    run_status=$?
    if printf '%s' "$run_out" | grep -q '"kind":"vm_already_exists"'; then
      echo "VM $VM already exists on the daemon; run '$0 down' first, or 'husker resume $VM' if it is suspended" >&2
    else
      echo "failed to boot VM $VM: $run_out" >&2
    fi
    exit "$run_status"
  fi

  # The VM now exists on the shared daemon. Any failure from here on must not
  # leave it running: arm handle_up_exit, disarmed only once this function
  # reaches its successful return below. down/url/status never call up(), so
  # this trap is never installed on those code paths, and it cannot fire
  # during them.
  trap handle_up_exit EXIT

  husker wait "$VM" >/dev/null

  echo "==> provisioning"
  husker exec "$VM" -- mkdir -p /opt/rig/app /opt/rig/data
  copy_binary /tmp/shinyhub-rig /opt/rig/shinyhub
  husker cp "$REPO_ROOT/loadtest/render/app/app.py" "$VM:/opt/rig/app/app.py"
  husker cp "$REPO_ROOT/loadtest/render/app/requirements.txt" "$VM:/opt/rig/app/requirements.txt"
  husker exec "$VM" -- chmod +x /opt/rig/shinyhub
  # ShinyHub's native build sandbox always deploys Python apps via
  # "uv run"/"uv sync", never bare system Python, and its /api/server-info
  # runtime probe (internal/api/serverinfo.go computeRuntimes()) reports the
  # Python runtime as available strictly via "exec.LookPath(\"uv\")". The
  # catalog image has python3 but not uv, so without this line every deploy
  # of the synthetic app fails with "Python runtime not found on the server
  # (uv/python3 is not in PATH)" even though the pip install below succeeds.
  husker exec "$VM" -- pip install --quiet uv
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
      trap - EXIT
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
  local out status
  # Guard the assignment with if/else, not a bare "out=$(...); status=$?"
  # pair: under set -e a bare failing assignment aborts the script before
  # status=$? ever runs, so the failure branch below would never be reached.
  if out="$(husker destroy "$VM" --yes -o json 2>&1)"; then
    echo "destroyed $VM"
    return 0
  else
    status=$?
  fi
  if printf '%s' "$out" | grep -q '"kind":"vm_not_found"'; then
    echo "no such VM $VM, nothing to do"
    return 0
  fi
  echo "failed to destroy $VM: $out" >&2
  return "$status"
}

case "${1:-}" in
  up) up ;;
  down) down ;;
  url) url ;;
  status) status ;;
  *) echo "usage: rig.sh {up|down|url|status}" >&2; exit 2 ;;
esac
