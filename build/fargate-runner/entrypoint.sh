#!/bin/sh
# ShinyHub managed-container reference runner - Python/uv.
#
# Env vars injected by the control plane (fargate.replicaEnv):
#   SHINYHUB_CONTROL_PLANE_URL  - base URL of the control plane
#   SHINYHUB_BUNDLE_TOKEN       - short-lived capability token for the bundle fetch
#   SHINYHUB_CONTENT_DIGEST     - expected sha256 digest (format: "sha256:<hex>")
#   SHINYHUB_SLUG               - app slug (informational, used in log output)
#   SHINYHUB_UV_LOCK            - "resolve" when the bundle's uv.lock is stale
#                                 against pyproject.toml; unset otherwise
#
# SHINYHUB_RUNNER_BUNDLE_ZIP and SHINYHUB_RUNNER_BUNDLE_DIR override where the
# bundle is downloaded and unpacked (defaults /tmp/shinyhub-bundle.zip and
# /app/bundle).
#
# Dep-prep mirrors internal/process/uv.go Sync():
#   - If pyproject.toml is present: run "uv sync", with --frozen when the
#     bundle ships a uv.lock so the lock is installed as-is and never re-resolved
#     against this container's index configuration, unless SHINYHUB_UV_LOCK is
#     "resolve"
#   - If only requirements.txt: uv run --with-requirements handles it at start
#
# R runner is a fast-follow (out of scope for this initial image).

set -eu

: "${SHINYHUB_CONTROL_PLANE_URL:?SHINYHUB_CONTROL_PLANE_URL is required}"
: "${SHINYHUB_BUNDLE_TOKEN:?SHINYHUB_BUNDLE_TOKEN is required}"
: "${SHINYHUB_CONTENT_DIGEST:?SHINYHUB_CONTENT_DIGEST is required}"

BUNDLE_ZIP="${SHINYHUB_RUNNER_BUNDLE_ZIP:-/tmp/shinyhub-bundle.zip}"
BUNDLE_DIR="${SHINYHUB_RUNNER_BUNDLE_DIR:-/app/bundle}"

# Step 1: fetch the bundle from the control plane using the capability token.
# The token is passed as a Bearer credential in the Authorization header so it
# does not appear in reverse-proxy request logs (path parameter would be logged).
# Endpoint: GET /internal/runtime-bundle/{digest}
echo "[shinyhub-runner] fetching bundle for ${SHINYHUB_SLUG:-unknown} digest=${SHINYHUB_CONTENT_DIGEST}"
curl -fsSL \
    -H "Authorization: Bearer ${SHINYHUB_BUNDLE_TOKEN}" \
    "${SHINYHUB_CONTROL_PLANE_URL}/internal/runtime-bundle/${SHINYHUB_CONTENT_DIGEST}" \
    -o "${BUNDLE_ZIP}"

# Step 2: verify the SHA-256 digest of the downloaded zip before extracting.
# Format is "sha256:<hex>"; extract the hex part for sha256sum comparison.
EXPECTED_HEX="${SHINYHUB_CONTENT_DIGEST#sha256:}"
if [ "${EXPECTED_HEX}" = "${SHINYHUB_CONTENT_DIGEST}" ]; then
    echo "[shinyhub-runner] ERROR: SHINYHUB_CONTENT_DIGEST must have sha256: prefix, got: ${SHINYHUB_CONTENT_DIGEST}" >&2
    exit 1
fi
if ! sha_out=$(sha256sum "${BUNDLE_ZIP}"); then
    echo "[shinyhub-runner] ERROR: sha256sum failed" >&2
    exit 1
fi
ACTUAL_HEX=$(printf '%s' "$sha_out" | cut -d' ' -f1)
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
# at exec time (see the command override from the control plane). A shipped
# uv.lock is installed with --frozen. The control plane checks the lock against
# pyproject.toml (the runner image carries no such check) and sets
# SHINYHUB_UV_LOCK=resolve for a stale one, which a plain "uv sync" re-resolves,
# as the host build does (see docs/fargate-runner-contract.md).
# Cross-reference: if internal/process/uv.go Sync() changes, update this block.
cd "${BUNDLE_DIR}"
if [ -f pyproject.toml ]; then
    if [ -f uv.lock ] && [ "${SHINYHUB_UV_LOCK:-}" != "resolve" ]; then
        echo "[shinyhub-runner] running uv sync --frozen"
        uv sync --frozen
    else
        if [ -f uv.lock ]; then
            echo "[shinyhub-runner] uv.lock is out of date with pyproject.toml; resolving the dependencies again"
        fi
        echo "[shinyhub-runner] running uv sync"
        uv sync
    fi
fi

# Step 5: exec the launch command supplied by the control plane as the container
# command override. The command is already constructed by deploy.BuildCommand
# with the correct bind host (0.0.0.0) and port from SHINYHUB_REPLICA_INDEX.
echo "[shinyhub-runner] starting app"
exec "$@"
