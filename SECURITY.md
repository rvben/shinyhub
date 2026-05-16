# Security

## Reporting a vulnerability

Please report security issues privately. Do not open a public GitHub issue
for a suspected vulnerability.

- Preferred: GitHub private vulnerability reporting ("Report a vulnerability"
  on the repository Security tab).
- Alternative: email ruben.jongejan@gmail.com with a description, affected
  version, and reproduction steps.

You will get an acknowledgement and a coordinated-disclosure timeline. Fixes
ship in a tagged release; the advisory is published once a fixed version is
available.

## Threat model

ShinyHub is a self-hosted control plane that deploys and runs operator-supplied
Shiny applications. Its security posture depends on which runtime you choose.

### Native runtime executes app code on the host (trusted-code model)

With `runtime.mode: native` (the default), ShinyHub runs each deployed app, and
its dependency build steps, **as a subprocess on the host under the ShinyHub
service account**. This includes:

- `Rscript -e 'renv::restore()'` and equivalent Python dependency installs,
  which execute arbitrary code from the app's lockfile / dependency tree at
  deploy time.
- The app process itself (`shinyApp`, Shiny for Python, etc.), which runs with
  the full privileges of the ShinyHub user for the life of the process.

There is no sandbox in native mode. **Anyone who can deploy an app can run
arbitrary code on the host as the ShinyHub user.** Treat deploy access as
equivalent to shell access. The native runtime is appropriate only when every
principal able to deploy (interactive users and any holder of a deploy token)
is a trusted operator.

Recommended hardening for native mode:

- Run the ShinyHub service as a dedicated unprivileged user with no sudo, a
  restricted shell, and ownership limited to its own data directories.
- Put it on a dedicated host or VM, not one that also holds unrelated secrets
  or production workloads.
- Restrict who can obtain a deploy token or an interactive `developer`/
  `operator`/`admin` account.

### Docker runtime for lower-trust scenarios

If app authors are **not** fully trusted operators, use `runtime.mode: docker`.
Each app then runs in its own container rather than directly on the host. This
is the recommended configuration for multi-tenant or semi-trusted deployments.
Containers are not a complete security boundary by themselves; combine with a
hardened Docker daemon (rootless or a dedicated unprivileged host/user),
resource limits, and a network policy appropriate to your environment.

## Secret handling

All server secrets are sourced from the environment and are never written to
the database or app-visible state.

| Secret | Source | Notes |
|--------|--------|-------|
| `auth.secret` | `SHINYHUB_AUTH_SECRET` | Session/JWT signing key. Must be at least 32 characters and not the example placeholder; the server refuses to start otherwise. Generate with `openssl rand -hex 32`. |
| OAuth client secrets | `SHINYHUB_GITHUB_CLIENT_SECRET`, `SHINYHUB_GOOGLE_CLIENT_SECRET`, `SHINYHUB_OIDC_CLIENT_SECRET` | Only the configured providers need a value. |
| Deploy token | `SHINYHUB_DEPLOY_TOKEN` (+ `SHINYHUB_DEPLOY_TOKEN_ROLE`) | Pre-shared CI bearer credential. At least 32 characters. Not persisted. |

Server secrets are kept out of the environment exposed to deployed app
subprocesses, so app code cannot read the signing key or OAuth secrets from its
own process environment.

## Deploy-token rotation

Two kinds of deploy credentials exist:

- **Env deploy token** (`SHINYHUB_DEPLOY_TOKEN`): a single pre-shared token for
  CI. It is not stored anywhere. Rotation is: set a new value in the
  environment and restart the server. The old token stops working immediately
  on restart.
- **API-minted tokens** (`POST /api/tokens`): per-token credentials carrying an
  `shk_` prefix, stored hashed. Rotate by minting a replacement, updating the
  consumer, then revoking the old token. Revocation takes effect immediately
  without a restart.

Scope every token to the least role that works (`viewer`, `developer`,
`operator`, `admin`). The env deploy token defaults to `developer`.

## Network trust

If ShinyHub runs behind a reverse proxy, set `server.trusted_proxies` to the
proxy addresses so client-IP attribution (used for rate limiting and logging)
cannot be spoofed via forwarded headers. Do not set it when ShinyHub is
directly internet-facing.

In-process rate limiting is per-process and in-memory; it is a single-node
abuse control, not a distributed quota. Front multi-node deployments with a
shared edge rate limiter if you need a global ceiling.

## Backup, restore, and recovery drill

`shinyhub backup --out <archive>` writes a snapshot of the database plus the
apps and app-data trees. The database is captured point-in-time consistent via
SQLite `VACUUM INTO`. The apps and app-data trees are then walked while the
server may still be running, so those trees are a best-effort, *not* a
point-in-time-consistent, copy: a deploy, prune, upload, or app-written file
that lands during the walk may be partially or inconsistently captured
relative to the database snapshot.

This is acceptable for routine periodic backups (a deploy mid-backup is rare
and the next backup converges). For a strictly consistent cross-tree snapshot,
either stop the server for the duration of the backup, or take the backup from
a filesystem-level snapshot (LVM/ZFS/cloud volume snapshot) so the database and
trees are captured at the same instant.

- **RPO (recovery point objective):** the snapshot is point-in-time, so your
  worst-case data loss is the interval between scheduled backups. Run it from
  cron at the frequency your tolerated loss window allows, and copy the archive
  off-host.
- **RTO (recovery time objective):** `shinyhub restore <archive>` is offline
  (stop the server first). It refuses archives from a newer schema, moves the
  current database, apps, and app-data aside with a `.pre-restore-<timestamp>`
  suffix (it never deletes, so that copy is your rollback path), then extracts
  in place. Recovery time is dominated by archive size, typically minutes.

Rehearse the restore drill before you need it. Recommended periodic drill:

1. Take a backup from the live server.
2. On a scratch host or directory, run `shinyhub restore` against it.
3. Start the server and confirm apps list, deploy, and serve correctly.
4. Confirm the `.pre-restore-*` copies exist and are discardable.

A backup you have never restored is an untested backup.
