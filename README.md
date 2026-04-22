# ShinyHub

Self-hosted platform for deploying and managing [R Shiny](https://shiny.posit.co/)
apps. Deploy with a CLI, route traffic through a reverse proxy, log in with
OAuth or OIDC, and hibernate idle apps automatically.

![Dashboard](docs/images/dashboard.png)

## Features

- **Deploy from CLI:** `shinyhub deploy` uploads a bundle and brings the app up.
- **Reverse proxy:** one URL per app under `/app/<slug>/`.
- **Hibernation:** idle apps are stopped and restarted on demand.
- **Auth:** username/password, GitHub OAuth, Google OAuth, or generic OIDC
  (Okta, Azure AD, Keycloak, Auth0).
- **Access control:** public, private, or shared apps; member roles.
- **Per-app env vars & secrets:** encrypted at rest with AES-256-GCM.
- **Persistent data dir:** each app gets a `data/` directory that survives
  deploys, with `shinyhub data push|ls|rm` and a UI Data tab.
- **Scheduled jobs + shared data:** per-app cron schedules that run as
  short-lived processes against the bundle, independent of whether the app is
  serving traffic. One app's data dir can be mounted read-only into another at
  `data/shared/<source-slug>/` to build fetcher → consumer dashboards. See
  [`docs/schedules.md`](docs/schedules.md).
- **Audit log:** 27 action types recorded for admin review.
- **Container isolation (optional):** run each app inside a Docker container
  with CPU and memory limits.
- **Per-app replicas:** set `replicas: N` and ShinyHub boots N backends for
  the app on the same host, sticky-session load-balanced and recovered
  independently on crash.
- **Single binary, SQLite, no external deps.**

## Quick start

### Install from PyPI

ShinyHub is published to PyPI. If you have `uv` or `pip`:

```bash
uv tool install shinyhub      # installs shinyhub into an isolated tool venv
# or, one-shot without installing:
uvx shinyhub deploy ./my-app --slug demo
# or, with pip:
pip install shinyhub
```

### Docker (recommended)

```bash
mkdir -p ./data
cp shinyhub.yaml.example ./shinyhub.yaml
# Edit shinyhub.yaml: set auth.secret to the output of `openssl rand -hex 32`

docker run -d \
  --name shinyhub \
  -p 8080:8080 \
  -v "$PWD/shinyhub.yaml:/etc/shinyhub/shinyhub.yaml:ro" \
  -v "$PWD/data:/data" \
  -e SHINYHUB_ADMIN_USER=admin \
  -e SHINYHUB_ADMIN_PASSWORD=change-me \
  ghcr.io/rvben/shinyhub:latest
```

The mount target `/data` matches the example YAML's `./data/...` paths
(database, bundles, app data dir all land there).

Open `http://localhost:8080`, log in with the admin credentials you set.

### Binary

```bash
curl -fsSL https://raw.githubusercontent.com/rvben/shinyhub/main/scripts/install.sh | sh
# Or download from https://github.com/rvben/shinyhub/releases

cp shinyhub.yaml.example shinyhub.yaml
# Set auth.secret to a 32-byte random value.

SHINYHUB_ADMIN_USER=admin SHINYHUB_ADMIN_PASSWORD=change-me \
  shinyhub serve --config ./shinyhub.yaml
```

### From source

```bash
git clone https://github.com/rvben/shinyhub.git
cd shinyhub
go build -o bin/shinyhub ./cmd/shinyhub
```

## Configuration

See [`shinyhub.yaml.example`](shinyhub.yaml.example) — every key is documented
inline. Environment variables (prefixed `SHINYHUB_`) override YAML; see the
example file for the full list.

Minimum required:

- `auth.secret` — random 32+ character string. Generate with
  `openssl rand -hex 32`. The server refuses to start with the placeholder
  value.

## Environment variables & secrets

Every app has its own key-value environment store. Non-secret values are
stored plaintext; values marked `--secret` are encrypted at rest with
AES-256-GCM (key derived from `SHINYHUB_AUTH_SECRET` via HKDF-SHA256) and
can never be read back through the API or UI.

### CLI

```
shinyhub env set demo AWS_REGION=eu-west-1
shinyhub env set demo AWS_SECRET_ACCESS_KEY --secret --stdin    # value from stdin
shinyhub env set demo LOG_LEVEL=debug --restart                 # restart the app after setting
shinyhub env ls  demo
shinyhub env rm  demo OLD_VAR
```

Keys must match `[A-Z_][A-Z0-9_]*`. Values are capped at 64 KiB each, with
at most 100 keys per app.

### UI

Open an app's **Settings** modal and switch to the **Environment** tab to
list, add, edit, and delete variables. Secret values are masked in the
list and write-only once created.

### Reserved prefix

Keys starting with `SHINYHUB_` are reserved for platform variables
(`SHINYHUB_APP_DATA`, future additions) and will be rejected with 422.

### Caveat: rotating `SHINYHUB_AUTH_SECRET`

The encryption key is derived from `SHINYHUB_AUTH_SECRET`. Rotating that
secret invalidates every stored secret; the affected apps will fail to
read their secret values until the variables are re-set via the CLI or
UI.

### When to use env vars vs persistent data

| You want to...                                         | Use                                  |
|--------------------------------------------------------|--------------------------------------|
| Configure a cloud bucket URL / DB URL / API endpoint   | Env var (non-secret)                 |
| Pass a password / API key / private key string         | Env var (secret)                     |
| Ship a Parquet / DuckDB / SQLite file the app reads    | Persistent data dir (see below)      |
| Let the app write uploads / cache / session data       | Persistent data dir (see below)      |

## Persistent data dir

Every deployed app gets its own directory at
`<storage.app_data_dir>/<slug>/`. The path is exposed to the app process two
ways:

- `SHINYHUB_APP_DATA` env var (absolute path).
- `./data/` symlink inside the app's working directory (or a Docker bind mount
  to `/app-data` plus a symlink at `<workdir>/data` when running under
  `runtime.mode: docker`).

The data dir survives deploys, restarts, and rollbacks. It is removed only
when the app itself is deleted. Recreating an app with the same slug starts
with a fresh, empty data dir.

Deploy bundles must not contain a `data/` entry — the server rejects bundles
where the first segment is a file, directory, or symlink named `data` (a 422
with the offending path). Push data in separately:

```
shinyhub data push <slug> ./seed.parquet
shinyhub data push <slug> ./big.csv --dest datasets/2026.csv --restart
shinyhub data ls   <slug>
shinyhub data rm   <slug> stale.csv
```

The same operations are available from the UI under **Settings → Data**.

### Auth

`PUT` and `DELETE` on `/api/apps/:slug/data/*path` require app `manager`
rights or platform `admin` / `operator`.

`GET /api/apps/:slug/data` requires the app's owner, an explicit member
(any role), or a platform admin / operator. **Public or shared visibility
alone is not enough** — file listings can leak business intent
(`q4-revenue.parquet`) and are kept off the public surface even when the
app itself is public.

### Quota

`storage.app_quota_mb` caps the combined on-disk footprint of the app's
deploy bundles plus the data dir. The check runs on every `PUT` and is
overwrite-aware: replacing a 100 MB file with a 50 MB one always succeeds.
Set to `0` to disable.

### Concurrent writes

The persistent dir is safe for any number of concurrent **readers**. For
concurrent **writers**, use a real database (Postgres/MySQL); local SQLite
or DuckDB in read-write mode does not survive multi-process writes.

## Architecture

![Deployment history](docs/images/deployment-history.png)

```
┌────────────┐    HTTPS    ┌──────────────────┐
│  Browser   │────────────▶│    ShinyHub      │
└────────────┘             │                  │
                           │  ┌────────────┐  │
┌────────────┐    CLI      │  │  API + UI  │  │
│  shinyhub  │────────────▶│  ├────────────┤  │
│  deploy    │             │  │   Proxy    │──┼──▶  app processes
└────────────┘             │  ├────────────┤  │     (native or Docker)
                           │  │   SQLite   │  │
                           │  └────────────┘  │
                           └──────────────────┘
```

Components:

- `cmd/shinyhub` — single binary: `shinyhub serve` (HTTP + proxy + lifecycle) and developer subcommands (`deploy`, `login`, `apps`, `env`, `data`, …).
- `internal/cli` — developer subcommand implementations.
- `internal/api` — chi-routed HTTP handlers.
- `internal/process` — native or Docker app process lifecycle.
- `internal/proxy` — reverse proxy.
- `internal/db` — SQLite store.

## Status

v0.2.x line — single-node, self-hosted. Used in production by the maintainer.
No SLA. Issues and PRs welcome. See [CHANGELOG.md](CHANGELOG.md) for the
current release.

## Links

- [Changelog](CHANGELOG.md)
- [Contributing](CONTRIBUTING.md)
- [License (MIT)](LICENSE)
- [Issues](https://github.com/rvben/shinyhub/issues)
