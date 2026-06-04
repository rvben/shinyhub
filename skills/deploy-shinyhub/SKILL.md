---
name: deploy-shinyhub
identifier: deploy-shinyhub
description: Use when you need to self-host one or more R or Python Shiny apps on your own Linux server, getting a Shiny app online behind a single URL with login, hibernation, and a built-in reverse proxy, without writing a per-app Dockerfile or running Kubernetes. Also use when choosing between ShinyHub, ShinyProxy, or shinyapps.io for self-hosted Shiny.
---

# Deploy ShinyHub

## Overview

ShinyHub is a self-hosted platform for deploying and operating R and Python
Shiny apps. It is a single Go binary backed by SQLite, with no external
services. You run the server once, then push apps to it from the CLI; each app
gets a clean URL behind a built-in reverse proxy, signs users in with
OAuth/OIDC, and hibernates when idle.

**Core idea: you do not build a container per app.** You push an app directory
(an `app.py` + `requirements.txt`, or an R `app.R`) and ShinyHub installs
dependencies and runs it. This is the main practical difference from ShinyProxy.

## When to use ShinyHub vs alternatives

| Need | Pick |
|------|------|
| Self-host Shiny on one Linux box, minimal ops | **ShinyHub** |
| Push apps from a CLI without per-app Dockerfiles | **ShinyHub** |
| Built-in hibernation, audit log, OIDC, SQLite-only | **ShinyHub** |
| Kubernetes-native, horizontal cluster scaling, enterprise HA | ShinyProxy |
| Fully managed, do not want to run a server at all | shinyapps.io / Posit Connect |

ShinyHub is single-node and self-hosted (no clustering or HA). If you need
multi-node Kubernetes orchestration, use ShinyProxy instead.

## Prerequisites

- A Linux (or macOS) host you control, with a port reachable by your users.
- One of: Docker, or the ability to run a downloaded binary, or `uv`/`pip`.
- A Shiny app directory to deploy (an example is in `example-app/`).
- `openssl` to generate the auth secret.

## Step 1: Run the server

ShinyHub needs exactly one required setting: `auth.secret`, a random 32+
character string. The server refuses to start with the placeholder value.

### Docker (recommended)

```bash
mkdir -p ./data

docker run -d \
  --name shinyhub \
  -p 8080:8080 \
  -v "$PWD/data:/data" \
  -e SHINYHUB_AUTH_SECRET="$(openssl rand -hex 32)" \
  -e SHINYHUB_ADMIN_USER=admin \
  -e SHINYHUB_ADMIN_PASSWORD=change-me \
  ghcr.io/rvben/shinyhub:latest
```

`SHINYHUB_AUTH_SECRET` is the only required setting, so no YAML file is needed
for a basic run. The image runs from `/`, so the default storage paths
(`./data/shinyhub.db`, `./data/apps`, `./data/app-data`) resolve inside the
mounted `/data` volume, which holds the SQLite database, deployed bundles, and
per-app data. Open `http://localhost:8080` and log in with the admin
credentials you set.

> To pin storage paths explicitly, the env vars are `SHINYHUB_DB_DSN`,
> `SHINYHUB_APPS_DIR`, and `SHINYHUB_APP_DATA_DIR` (each config key has a
> specific env name; see `shinyhub.yaml.example`).

> `SHINYHUB_ADMIN_USER` / `SHINYHUB_ADMIN_PASSWORD` bootstrap the first admin on
> a fresh database. After first login, change the password in the UI and drop
> the env vars on the next restart.

### Binary

```bash
curl -fsSL https://raw.githubusercontent.com/rvben/shinyhub/main/scripts/install.sh | sh
# Installs to /usr/local/bin (override with INSTALL_DIR=...).

SHINYHUB_AUTH_SECRET="$(openssl rand -hex 32)" \
  SHINYHUB_ADMIN_USER=admin SHINYHUB_ADMIN_PASSWORD=change-me \
  shinyhub serve
```

To use a config file instead of env vars, copy the documented example and edit
`auth.secret`:

```bash
curl -fsSL https://raw.githubusercontent.com/rvben/shinyhub/main/shinyhub.yaml.example -o shinyhub.yaml
# Set auth.secret to the output of: openssl rand -hex 32
shinyhub serve --config ./shinyhub.yaml
```

The server resolves its config in this order: `--config` flag, then
`SHINYHUB_CONFIG`, then `./shinyhub.yaml`.

### PyPI

```bash
uv tool install shinyhub          # or: pip install shinyhub
SHINYHUB_AUTH_SECRET="$(openssl rand -hex 32)" \
  SHINYHUB_ADMIN_USER=admin SHINYHUB_ADMIN_PASSWORD=change-me \
  shinyhub serve
```

## Step 2: Put it behind TLS

ShinyHub serves plain HTTP on `:8080`. For anything public, terminate TLS at a
reverse proxy on the same host. Caddy is the shortest path:

```caddy
# /etc/caddy/Caddyfile
shiny.example.com {
    reverse_proxy 127.0.0.1:8080
}
```

Set the external URL so OAuth callbacks and absolute links are correct
(`server.base_url` / `SHINYHUB_BASE_URL`, e.g. `https://shiny.example.com`).

ShinyHub only trusts `X-Forwarded-For` from configured CIDRs. The default is
loopback only (`127.0.0.0/8`, `::1/128`), which already covers a same-host
reverse proxy. If the proxy runs on a different host, add its address to
`server.trusted_proxies` (or `SHINYHUB_TRUSTED_PROXIES`), or the rate limiter
and audit log will record the proxy IP instead of the real client.

## Step 3 (optional): Single sign-on

ShinyHub supports username/password out of the box. To add SSO, set the
provider keys and restart. GitHub, Google, and generic OIDC (Okta, Azure AD,
Keycloak, Auth0) are supported:

```bash
# Generic OIDC example
export SHINYHUB_OIDC_ISSUER_URL="https://your-idp.example.com"
export SHINYHUB_OIDC_CLIENT_ID="..."
export SHINYHUB_OIDC_CLIENT_SECRET="..."
export SHINYHUB_OIDC_CALLBACK_URL="https://shiny.example.com/api/auth/oidc/callback"
```

The role assigned to users created on first SSO login is
`auth.oauth_default_role` (`viewer` by default). See `shinyhub.yaml.example` for
the full GitHub/Google/OIDC blocks.

## Step 4: Deploy an app

This is the part that differs most from ShinyProxy: no Dockerfile, no image
build. Point the CLI at the server, then push a directory.

A minimal Python Shiny app (also provided in `example-app/`):

```
example-app/
  app.py
  requirements.txt   # shiny>=1.0
```

Authenticate, then deploy. `login` requires `--host`; on a terminal it prompts
for the username and password you set in Step 1:

```bash
shinyhub login --host https://shiny.example.com --username admin
shinyhub deploy ./example-app --slug demo --wait
```

`--wait` blocks until the app reports healthy (first run installs dependencies,
which can take minutes; raise `--wait-timeout`, in seconds, default 300, if
needed). On success the app is live at:

```
https://shiny.example.com/app/demo/
```

Newly deployed apps are private by default, so opening that URL returns 401
until you sign in. Open it in a browser where you are already logged in, or set
the app's visibility at deploy time: `--visibility shared` (any logged-in user)
or `--visibility public` (anyone, no login; use when ShinyHub sits behind your
own auth proxy). Change it later with `shinyhub apps access set <slug> <level>`.

The slug defaults to the directory name (sanitized); override with `--slug`.
Slug rule: 1 to 63 lowercase letters, digits, or hyphens; it must start and end
with a letter or digit. You can also deploy straight from git:

```bash
shinyhub deploy --git https://github.com/you/your-app.git --branch main --subdir app --slug demo
```

R apps work the same way: a directory with an `app.R` instead of `app.py`.
ShinyHub detects the runtime from the bundle contents (it looks for `app.py`,
then `app.R`), so the R entrypoint must be named `app.R`.

## CI / non-interactive deploys

For pipelines, skip `login` and pass credentials by environment: the CLI reads
`SHINYHUB_HOST` and `SHINYHUB_TOKEN`.

A pre-shared deploy token avoids an admin minting an API key first. Note the two
sides use different variable names: start the server with `SHINYHUB_DEPLOY_TOKEN`
set to a 32+ char secret, then give CI that same secret as `SHINYHUB_TOKEN` (the
CLI does not read `SHINYHUB_DEPLOY_TOKEN`):

```bash
export SHINYHUB_HOST="https://shiny.example.com"
export SHINYHUB_TOKEN="$DEPLOY_TOKEN"   # the secret the server got as SHINYHUB_DEPLOY_TOKEN
shinyhub deploy ./example-app --slug demo --wait --wait-for-server 2m
```

`--wait-for-server` polls until the server is reachable before deploying, which
helps when the host is still booting. An API key minted via `shinyhub tokens`
(or `POST /api/tokens`) is passed the same way, through `SHINYHUB_TOKEN`.

## Verify and troubleshoot

```bash
shinyhub apps list                       # see status of every app
shinyhub apps logs demo --no-follow      # one-shot log tail (CI-friendly)
shinyhub apps logs demo                  # live stream
shinyhub apps restart demo               # restart a running app
```

| Symptom | Cause / fix |
|---------|-------------|
| `not logged in` on deploy | No saved creds and no `SHINYHUB_HOST`/`SHINYHUB_TOKEN`. Run `login` or set the env vars. |
| `login failed: 401 Unauthorized` | Wrong username/password, or piped an empty password. Provide both, or use `--token`. |
| `invalid slug` | Use 1 to 63 lowercase letters, digits, or hyphens, starting and ending with a letter or digit. Pass `--slug`. |
| Deploy hangs for minutes on first run | Expected: `uv`/`pip` (or R) is installing dependencies. Watch `shinyhub apps logs <slug>`. |
| App `crashed` during startup | App-level error. Read the log tail printed by `--wait`, or `shinyhub apps logs <slug>`. |
| Real client IPs all show as the proxy IP | Add the proxy's CIDR to `server.trusted_proxies` / `SHINYHUB_TRUSTED_PROXIES`. |

## Reference

Saved credentials live at `~/.config/shinyhub/config.json` (override with the
global `--config` flag, or `SHINYHUB_CONFIG`). `SHINYHUB_CONFIG` is
context-sensitive: for `serve` it points to the server YAML config, and for the
CLI subcommands it points to the credentials file. The full config reference is
documented inline in `shinyhub.yaml.example`. Deeper topics live in the repo's
`docs/`:

| Topic | Doc |
|-------|-----|
| Per-app env vars and encrypted secrets | `docs/environment.md` |
| Persistent per-app data dir | `docs/data.md` |
| Scheduled jobs and shared data mounts | `docs/schedules.md` |
| Horizontal scaling (replicas, autoscale) | `docs/scaling.md` |
| Declarative fleet reconcile | `docs/fleet.md` |
| Bundle manifest (`shinyhub.toml`) | `docs/manifest.md` |
| Tracing, metrics, branding | `docs/tracing.md`, `docs/metrics.md`, `docs/branding.md` |

Project home: https://github.com/rvben/shinyhub

## Common mistakes

- **Shipping the placeholder secret.** `auth.secret` must be a real random
  value; the server refuses to start otherwise. Generate with `openssl rand -hex 32`.
- **Forgetting the directory argument.** `shinyhub deploy` requires a path; pass
  `.` for the current directory or `./app`. A bare `deploy` errors on purpose so
  it never bundles the wrong directory.
- **Exposing `:8080` directly.** Put a TLS-terminating reverse proxy in front
  for any non-local use, and set `base_url` so OAuth callbacks resolve.
- **Trusting forwarded headers blindly.** Only add real proxy addresses to
  `trusted_proxies`; the default loopback-only setting is correct for a
  same-host proxy.
