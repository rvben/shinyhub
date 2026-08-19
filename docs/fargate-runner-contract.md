---
description: "The interface a managed container runner image must implement so ECS, Fargate, and Scaleway Serverless can host apps the control plane schedules."
---

# Managed Runner Image Contract

This document defines the interface between the ShinyHub control plane and a
managed container runner image. AWS ECS/Fargate and Scaleway Serverless
Containers use the same fetch, verify, prepare, and exec protocol. Any custom
runner image must implement it exactly. The reference Python image lives at
`build/fargate-runner/` in this repository and is the canonical implementation.

## Environment variables injected by the control plane

The control plane injects the following environment variables into every task
via ECS container overrides (`fargate.replicaEnv`). Runner entrypoints must
read these; additional variables from the task definition and operator-supplied
app env vars are passed through unchanged.

| Variable | Required | Description |
|----------|----------|-------------|
| `SHINYHUB_SLUG` | Yes | The app slug. Always present, even when empty (Start validates non-empty before calling replicaEnv). |
| `SHINYHUB_CONTROL_PLANE_URL` | Yes* | Base URL of the ShinyHub control plane, reachable from inside the task network (e.g. `https://shiny.internal:8080`). Used to build the bundle fetch URL. *Required when `runtime.fargate.control_plane_url` is set; the server enforces this. |
| `SHINYHUB_BUNDLE_TOKEN` | Yes* | Short-lived capability token (format `v1.<exp-unix>.<base64url-hmac>`) authorising a fetch of the bundle identified by `SHINYHUB_CONTENT_DIGEST`. Valid for `runtime.fargate.bundle_token_ttl` (default 10 minutes). *Injected when BundleTokenKey is set and ContentDigest is non-empty. |
| `SHINYHUB_CONTENT_DIGEST` | Yes* | Content digest of the app bundle zip in `sha256:<hex>` format. Used to verify the downloaded bundle and to build the fetch URL. *Injected when non-empty. |
| `SHINYHUB_REPLICA_INDEX` | Yes | Zero-based index of this replica within the app's pool, as a decimal string. Always injected, including `"0"` for the first (or only) replica. |
| `SHINYHUB_DEPLOYMENT_ID` | No | Numeric deployment ID as a decimal string. Injected when the deployment ID is known (non-zero). Used for log correlation. |
| `SHINYHUB_APP_VERSION` | No | App version string. Injected when non-empty. Used for labeling. |

App-specific environment variables set by operators via `shinyhub env set` are
prepended to the list (before the platform vars), so the `SHINYHUB_*` platform
vars are always authoritative for their reserved prefix even if an app env var
collides.

**Note on port and app data:** The app port is NOT a separate environment
variable. It is baked into the container command override that the control plane
constructs and passes as the ECS `ContainerOverride.Command`. The runner
entrypoint receives the launch command via `$@` (the exec arguments) and must
pass it through as-is. Similarly, `SHINYHUB_APP_DATA` is not injected by the
Fargate runtime because `HostProvidesAppData()` returns false; the task
provisions its own data storage.

## Bundle fetch protocol

1. Build the fetch URL:
   `${SHINYHUB_CONTROL_PLANE_URL}/internal/runtime-bundle/${SHINYHUB_CONTENT_DIGEST}`.
   `/internal/fargate-bundle/` remains a compatibility alias for older images.
2. Fetch the bundle zip with an HTTP GET and the header
   `Authorization: Bearer ${SHINYHUB_BUNDLE_TOKEN}`.
3. Verify the downloaded content against `SHINYHUB_CONTENT_DIGEST`.
   The digest is in `sha256:<hex>` format; strip the `sha256:` prefix before
   passing to `sha256sum`. Abort and exit non-zero if the digest does not match:
   a mismatch indicates a proxy substitution or transmission error.
4. Unzip into a working directory (e.g. `/app/bundle`).

The fetch endpoint is `GET /internal/runtime-bundle/{digest}` on the control
plane's main listener. It is NOT under `/api/`, so it is not subject to the
30-second API timeout middleware; large bundle streams will not be cut off.

Security note: `SHINYHUB_BUNDLE_TOKEN` is a short-lived bearer credential
scoped to one content digest. Fargate routes app secrets through Secrets
Manager when configured; Scaleway sends them as secret environment variables,
which are write-only after submission. Restrict provider IAM access to the
control plane and keep its API secret outside configuration files.

Token format: `v1.<exp-unix-seconds>.<base64url(HMAC-SHA256(derived-key, "v1|<exp>|<digest>"))>`.
The key is derived via HKDF-SHA256 from `auth.secret` with info string
`shinyhub-fargate-bundle-v1`. Verification is stateless; there is no revocation.

## Dependency preparation

Because `HostPreparesDeps()` returns `false` for Fargate tasks, the control
plane never runs dependency installation before launching the task. The runner
entrypoint must perform dependency preparation itself after unpacking the bundle.

### Python apps

The reference image mirrors `internal/process/uv.go Sync()`:

- If `pyproject.toml` is present: run `uv sync` (no extra flags).
- If only `requirements.txt` is present: do NOT run uv sync. The launch
  command uses `uv run --with-requirements` which installs at exec time.

```sh
# Keep in sync with internal/process/uv.go Sync() when the host prep changes.
cd /app/bundle
if [ -f pyproject.toml ]; then
    uv sync
fi
```

The reference image uses `ghcr.io/astral-sh/uv:python3.12-bookworm-slim` as
its base so `uv` is available on PATH.

### R apps

The R runner is a planned fast-follow (not yet published). It would mirror
`internal/process/renv.go PrepareEnv`:

```sh
# Keep in sync with internal/process/renv.go PrepareEnv when the host prep changes.
cd /app/bundle
Rscript -e 'renv::restore(prompt=FALSE)' 2>&1
```

## Launch command

After dependency preparation the runner execs the launch command supplied by
the control plane as the container `Command` override. The entrypoint receives
it as `$@` and must exec it unchanged:

```sh
exec "$@"
```

The command is constructed by the deploy path with the correct bind host
(`0.0.0.0`) and port. The app process must bind on `0.0.0.0` (not `127.0.0.1`)
so the control plane can reach it on the task's awsvpc interface.

Example (Python Shiny):

```sh
uv run python -m shiny run --host 0.0.0.0 --port 8080 app.py
```

## Exit codes and restart behavior

The control plane watchdog does not directly observe Fargate task exit codes. A
task that exits is stopped from ECS's perspective. The watchdog detects the
missing inventory entry and triggers restart per the normal crash-restart budget
(`lifecycle.restart_max_attempts`).

## Custom images

To build a custom runner image:

1. Implement the env-var contract above (all "Required: Yes" variables must be
   consumed).
2. Implement the `GET /internal/runtime-bundle/` fetch-verify-unzip sequence.
3. Implement dep-prep equivalent to the reference image for your app type.
4. Exec the app command via `exec "$@"` on the `0.0.0.0` bind host.
5. Set `runtime.fargate.task_definition` to a task definition whose container
   uses your image, and set `runtime.fargate.container_name` to that
   container's name.

The control plane does not care which base image or language toolchain you use
as long as the fetch protocol and command-exec contract are satisfied.
