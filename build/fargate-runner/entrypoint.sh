#!/bin/sh
# ShinyHub Fargate reference runner - Python/uv.
#
# Env vars injected by the control plane (fargate.replicaEnv):
#   SHINYHUB_CONTROL_PLANE_URL  - base URL of the control plane
#   SHINYHUB_BUNDLE_TOKEN       - short-lived capability token for the bundle fetch
#   SHINYHUB_CONTENT_DIGEST     - expected sha256 digest (format: "sha256:<hex>")
#   SHINYHUB_SLUG               - app slug (informational, used in log output)
#
# Dep-prep mirrors internal/process/uv.go Sync():
#   - If pyproject.toml is present: run "uv sync" (same command, no flags)
#   - If only requirements.txt: uv run --with-requirements handles it at start
#
# R runner is a fast-follow (out of scope for this initial image).

set -e

: "${SHINYHUB_CONTROL_PLANE_URL:?SHINYHUB_CONTROL_PLANE_URL is required}"
: "${SHINYHUB_BUNDLE_TOKEN:?SHINYHUB_BUNDLE_TOKEN is required}"
: "${SHINYHUB_CONTENT_DIGEST:?SHINYHUB_CONTENT_DIGEST is required}"

BUNDLE_ZIP=/tmp/shinyhub-bundle.zip
BUNDLE_DIR=/app/bundle

# Step 1: fetch the bundle from the control plane using the capability token.
# The token is passed as a Bearer credential in the Authorization header so it
# does not appear in reverse-proxy request logs (path parameter would be logged).
# Endpoint: GET /internal/fargate-bundle/{digest}
echo "[shinyhub-runner] fetching bundle for ${SHINYHUB_SLUG:-unknown} digest=${SHINYHUB_CONTENT_DIGEST}"
curl -fsSL \
    -H "Authorization: Bearer ${SHINYHUB_BUNDLE_TOKEN}" \
    "${SHINYHUB_CONTROL_PLANE_URL}/internal/fargate-bundle/${SHINYHUB_CONTENT_DIGEST}" \
    -o "${BUNDLE_ZIP}"

# Step 2: verify the SHA-256 digest of the downloaded zip before extracting.
# Format is "sha256:<hex>"; extract the hex part for sha256sum comparison.
EXPECTED_HEX="${SHINYHUB_CONTENT_DIGEST#sha256:}"
if [ "${EXPECTED_HEX}" = "${SHINYHUB_CONTENT_DIGEST}" ]; then
    echo "[shinyhub-runner] ERROR: SHINYHUB_CONTENT_DIGEST must have sha256: prefix, got: ${SHINYHUB_CONTENT_DIGEST}" >&2
    exit 1
fi
ACTUAL_HEX=$(sha256sum "${BUNDLE_ZIP}" | cut -d' ' -f1)
if [ "${ACTUAL_HEX}" != "${EXPECTED_HEX}" ]; then
    echo "[shinyhub-runner] ERROR: digest mismatch: expected ${EXPECTED_HEX}, got ${ACTUAL_HEX}" >&2
    exit 1
fi
echo "[shinyhub-runner] digest verified ok"

# Step 3: unzip into the bundle directory.
mkdir -p "${BUNDLE_DIR}"
unzip -q "${BUNDLE_ZIP}" -d "${BUNDLE_DIR}"
rm -f "${BUNDLE_ZIP}"

# Step 4: prepare dependencies.
# Mirrors internal/process/uv.go Sync(): run "uv sync" only when pyproject.toml
# is present; requirements.txt-only projects rely on "uv run --with-requirements"
# at exec time (see the command override from the control plane).
# Cross-reference: if internal/process/uv.go Sync() changes, update this block.
cd "${BUNDLE_DIR}"
if [ -f pyproject.toml ]; then
    echo "[shinyhub-runner] running uv sync"
    uv sync
fi

# Step 5: exec the launch command supplied by the control plane as the container
# command override. The command is already constructed by deploy.BuildCommand
# with the correct bind host (0.0.0.0) and port from SHINYHUB_REPLICA_INDEX.
echo "[shinyhub-runner] starting app"
exec "$@"
