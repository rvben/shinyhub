# ShinyHub

[![PyPI](https://img.shields.io/pypi/v/shinyhub)](https://pypi.org/project/shinyhub/)
[![CI](https://github.com/rvben/shinyhub/actions/workflows/ci.yml/badge.svg)](https://github.com/rvben/shinyhub/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

Self-hosted platform for deploying and operating [R and Python Shiny](https://shiny.posit.co/)
applications. Push an app from the CLI, get a clean URL behind a built-in
reverse proxy, sign users in with OAuth or OIDC, and let idle apps hibernate
and wake on demand. ShinyHub runs as a single Go binary backed by SQLite, with
no external services to operate.

<p align="center">
  <img src="docs/images/dashboard.png" alt="ShinyHub dashboard showing the app grid with running apps and per-app CPU and memory" width="900">
</p>

## Contents

- [Features](#features)
- [Quick start](#quick-start)
- [Configuration](#configuration)
- [Guides](#guides)
- [Architecture](#architecture)
- [Status](#status)
- [Contributing](#contributing)
- [License](#license)

## Features

- **Deploy from the CLI.** `shinyhub deploy` uploads a bundle and brings the app up.
- **Several servers at once.** One saved credential per server, switched with `shinyhub use <name>` or targeted per command with `--host`. See [docs/hosts.md](docs/hosts.md).
- **Reverse proxy.** One URL per app under `/app/<slug>/`, sticky-session aware.
- **Hibernation.** Idle apps are stopped automatically and restarted on the next request.
- **Authentication.** Username/password, GitHub OAuth, Google OAuth, or generic OIDC (Okta, Azure AD, Keycloak, Auth0).
- **Access control.** Private (members only), shared (every signed-in user), or public apps, with per-app member and IdP-group roles.
- **Env vars and secrets.** Per-app key-value store; secrets encrypted at rest with AES-256-GCM. See [docs/environment.md](docs/environment.md).
- **Persistent data dir.** Each app gets a `data/` directory that survives deploys, with `shinyhub data push|ls|rm` and a UI tab. See [docs/data.md](docs/data.md).
- **Scheduled jobs and shared data.** Per-app cron schedules and read-only cross-app data mounts for fetcher to consumer pipelines. See [docs/schedules.md](docs/schedules.md).
- **Horizontal scaling.** Set `replicas: N` to run multiple load-balanced backends per app, recovered independently on crash. See [docs/scaling.md](docs/scaling.md).
- **Render pacing.** Set `render_seconds` to admit page loads at a rate the host can actually render, so a burst queues on a self-retrying wait page instead of stalling every session at once. See [docs/scaling.md](docs/scaling.md#render-pacing).
- **Fleet reconcile.** Declare a whole set of apps in one `fleet.toml` and converge the server to match, kubectl-apply style. See [docs/fleet.md](docs/fleet.md).
- **Observability.** OpenTelemetry tracing (proxy, app, and control-plane spans), an opt-in Prometheus `/metrics` endpoint, and a structured access log with request-ID and trace correlation. See [docs/tracing.md](docs/tracing.md) and [docs/metrics.md](docs/metrics.md).
- **Container isolation (optional).** Run each app inside a Docker container with CPU and memory limits.
- **Worker isolation.** Per-app session isolation dial: `multiplex` (shared event loop), `grouped` (N clients per worker), or `per_session` (one process per browser client, HOL-free). See [docs/isolation.md](docs/isolation.md).
- **Branding (white-label).** Customize the front door (title, logo, theme, landing page) without forking. See [docs/branding.md](docs/branding.md).
- **Audit log.** Mutating actions recorded for admin review.
- **Single binary, SQLite, no external dependencies.**

## Quick start

The simplest way to run ShinyHub end to end is with
[`uv`](https://docs.astral.sh/uv/): it installs and runs the server in one
command, and it is also the runtime ShinyHub uses to launch Python apps, so once
`uv` is on the host you have everything you need. For an isolated or production
deployment use [Docker](#docker); for a standalone server binary see
[Binary](#binary).

### uv (recommended)

```bash
uv tool install shinyhub
shinyhub serve
```

On the first run, ShinyHub asks for an administrator username and password,
creates a private `shinyhub.yaml` with a cryptographically random secret, prepares
the SQLite database, and then starts normally. Nothing sensitive is echoed. The
database, bundles, and per-app data land under `./data/` by default. Open
`http://localhost:8080` and sign in with the administrator you just created.

Prefer setup and startup as separate steps? Run `shinyhub init`, inspect the
summary, and then run `shinyhub serve`. Both paths are safe to rerun: existing
configuration, secrets, and users are never overwritten.

Then deploy an app (an `app.py` + `requirements.txt`, or an `app.R` +
`renv.lock` - see [Deploy an R Shiny app](docs/recipes/r-shiny.md)) from
another terminal:

```bash
shinyhub connect http://localhost:8080 --name local
shinyhub plan ./my-app --slug demo
shinyhub deploy ./my-app --slug demo --wait   # live at /app/demo/
```

### Connect to a remote ShinyHub

On your workstation, connect once and deploy from any app directory:

```bash
uv tool install shinyhub
shinyhub completion install             # detect your shell; safe to rerun
cd ./my-app
shinyhub doctor . --local
shinyhub run . --check
shinyhub connect https://hub.example.com --name prod
shinyhub doctor .
shinyhub plan .
shinyhub deploy . --wait
```

`connect` verifies that the URL is a healthy ShinyHub, reports its available
app runtimes, and opens a browser authorization page. Sign in with whatever the
server supports—including SSO—and confirm the matching verification code. The
CLI creates its credential locally, so the secret never appears in browser
history or server logs. The resulting personal token expires after 90 days and
is saved in the owner-readable client credentials file.

`shinyhub whoami` shows the active credential's type, name, creation time, last
use, and expiry. `doctor` warns 14 days before expiry without blocking a deploy.
Rotate early with `shinyhub connect --refresh`: it approves a fresh credential
in the browser, verifies and atomically saves it, then revokes the previous API
key. A failed pairing leaves the working local credential byte-for-byte intact.

It also checks the CLI/server API protocol before using the credential. Safe
release drift remains usable with precise upgrade guidance; an unsupported
newer protocol stops before an authenticated request. `shinyhub doctor --remote`
shows the same check at any time. See [CLI completion and compatibility](docs/cli.md).

If you run `shinyhub deploy .` before connecting, an interactive terminal offers
to start this flow in place. Automation never prompts: use
`SHINYHUB_HOST`/`SHINYHUB_TOKEN` or `shinyhub connect <url> --token-file <path>`.
On an SSH session or a terminal without a local browser, add `--no-browser`;
ShinyHub prints a safe pairing URL that you can open on any signed-in device.
Use `shinyhub login` when you specifically want a short-lived password session
or need to refresh one; `connect` is the recommended first-time workstation
flow.

`shinyhub doctor .` checks the complete path before a deploy: bundle and
manifest, local launcher, credential-file safety, server connectivity,
authentication, exact app permission, and remote runtime availability. It
lists every problem with a concrete fix in one run, emits JSON when piped, and
does not change local or remote state. See [Doctor](docs/doctor.md).

`shinyhub plan .` then shows the exact archive, content digest, ignored paths,
launch and manifest effects, remote create-or-update state, permissions, and
start/stop lifecycle—without making a change. Its final line is the exact
deploy command to run. Use `--detailed-exitcode` or `--fail-on-changes` for a CI
gate. See [Deployment plan](docs/deployment-plan.md).

> Tip: run the bundle locally first with `shinyhub run ./my-app`. It keeps your
> source tree untouched, serves the production-shaped `/app/<slug>/` route, and
> swaps in edits only after they pass readiness. Add `--check` for a CI-friendly
> boot smoke test or `--fresh` to rebuild cached dependencies. See
> [Local development](docs/local-development.md).

> `uvx shinyhub <cmd>` runs any subcommand one-shot, without installing first.
> `pip install shinyhub` installs the server too, but native Python apps launch
> via `uv run`, so you still need `uv` on the host to deploy them.

### Docker

The published image runs the ShinyHub control plane, proxy, and dashboard, but it
is distroless and does **not** bundle a Python or R runtime, so it cannot run
apps on its own. To run apps under Docker, use the
[`deploy/docker-compose`](deploy/docker-compose) stack, which wires up the Docker
app runtime (each app in its own container) together with the host networking and
path-parity data root that runtime requires. To run apps without containers, run
the server on a host that has `uv` (the [uv path](#uv-recommended) above).

To start just the control plane (dashboard + API, not app execution) with a
persistent `/data` volume:

```bash
mkdir -p ./data
secret="$(openssl rand -hex 32)"   # generate once; reuse the SAME value on restart

docker run -d \
  --name shinyhub \
  -p 8080:8080 \
  -v "$PWD/data:/data" \
  -e SHINYHUB_AUTH_SECRET="$secret" \
  -e SHINYHUB_ADMIN_USER=admin \
  -e SHINYHUB_ADMIN_PASSWORD='choose-a-strong-password' \
  ghcr.io/rvben/shinyhub:latest
```

The default `./data/...` paths resolve inside the mounted `/data` volume. Open
`http://localhost:8080` and log in with the admin credentials you set. To deploy
apps from here, switch to the [`deploy/docker-compose`](deploy/docker-compose)
stack; for storage and SSO settings, mount a `shinyhub.yaml`
(see [Configuration](#configuration)).

### Binary

For a host without Python, install the standalone server binary:

```bash
curl -fsSL https://raw.githubusercontent.com/rvben/shinyhub/main/scripts/install.sh | sh
# Or download from https://github.com/rvben/shinyhub/releases
shinyhub serve
```

> The binary is the server only. Python apps are launched with `uv`, so install
> `uv` on the host too (or run apps under the Docker runtime).

### From source

```bash
git clone https://github.com/rvben/shinyhub.git
cd shinyhub
go build -o bin/shinyhub ./cmd/shinyhub
bin/shinyhub serve
```

## Configuration

Every key is documented inline in
[`shinyhub.yaml.example`](shinyhub.yaml.example). Environment variables
(prefixed `SHINYHUB_`) override the YAML.

The server resolves its config file in this order: the `--config` flag
(`shinyhub serve --config /path/to/shinyhub.yaml`, also honored by `init`,
`backup`, and `restore`), then the `SHINYHUB_CONFIG` environment variable, then
`./shinyhub.yaml`.

> **`SHINYHUB_CONFIG` has two distinct roles.** On the SERVER it selects the
> `shinyhub.yaml` config file. On CLIENT commands (`deploy`, `env`, `apps`, ...)
> it selects the credentials file (`~/.config/shinyhub/config.json` by default,
> written by `shinyhub connect` or `shinyhub login`). The `--config` flag on client commands likewise
> points to the credentials file, not the server YAML. For CI pipelines the
> simpler approach is to skip the credentials file entirely and supply
> `SHINYHUB_HOST` and `SHINYHUB_TOKEN` directly.

The credentials file holds one entry per server, so you can be signed in to a
local hub and production at the same time and switch with `shinyhub use`. See
[Working with several servers](docs/hosts.md) for how a command picks which
server and which token to use.

For interactive use, `shinyhub init` generates the required `auth.secret` and
stores it in an owner-readable configuration file. For unattended deployments,
provide a stable random 32+ character `SHINYHUB_AUTH_SECRET` together with
`SHINYHUB_ADMIN_USER` and `SHINYHUB_ADMIN_PASSWORD`. Keep the same secret across
restarts: it signs sessions and derives the key used to encrypt app secrets.

### CLI output

Client commands render a table on a terminal and JSON when their output is
piped or redirected, so `shinyhub apps list` is readable by a person and
parseable by a script without either having to ask. `-o table|json|ndjson`
forces one, `-q` suppresses non-essential prose, and `shinyhub schema`
describes every command, flag, and error kind as JSON.

Color and glyphs are decoration only: a status is always spelled out as a word
and a result always carries `✓` or `✗`, so nothing is lost when they are off.
They are off automatically whenever output is not a terminal, and can be
controlled explicitly:

| Setting | Effect |
|---|---|
| `--no-color` | No ANSI color. Wins over everything below. |
| `NO_COLOR=1` | No ANSI color ([no-color.org](https://no-color.org)). |
| `CLICOLOR=0` | No ANSI color. |
| `TERM=dumb` | No color, and no in-place redraw: the deploy progress line becomes one line per step. |
| `CLICOLOR_FORCE=1` | Color even when piped, for a CI log that renders ANSI. `FORCE_COLOR` is an alias. |
| `LANG=C` (any non-UTF-8 locale) | ASCII glyphs (`v`, `x`, `\|/-\`) instead of Unicode. |

## Guides

| Guide | Topic |
|---|---|
| [Working with several servers](docs/hosts.md) | Saving a credential per server, `hosts` / `use` / `--host`, which token a command sends, and the credentials file. |
| [CLI completion and compatibility](docs/cli.md) | One-command shell completion, saved-host suggestions, version-skew diagnostics, and upgrade behavior. |
| [Environment and secrets](docs/environment.md) | Per-app env vars, encrypted secrets, what apps and builds inherit from the server environment, and private package indexes. |
| [Persistent data dir](docs/data.md) | Pushing data, the app-visible path, authorization, quota, and concurrency. |
| [Scheduled jobs and shared data](docs/schedules.md) | Per-app cron schedules and read-only cross-app data mounts. |
| [Horizontal scaling](docs/scaling.md) | Per-app replicas, load balancing, and session admission. |
| [Worker isolation](docs/isolation.md) | Session isolation dial: multiplex, grouped, and per_session modes. |
| [App startup performance](docs/app-performance.md) | Why data lags the page shell, and the startup-scope and caching patterns that fix it. |
| [Fleet reconcile](docs/fleet.md) | Declaring and converging a whole set of apps from one file. |
| [Deploy manifest](docs/manifest.md) | The `shinyhub.toml` bundle manifest. |
| [Local development](docs/local-development.md) | Delightful control-plane and app-author workflows. |
| [Tracing](docs/tracing.md) | OpenTelemetry propagation, app spans, and control-plane spans. |
| [Metrics and logs](docs/metrics.md) | The `/metrics` endpoint, exposed series, and the structured access log. |
| [Branding](docs/branding.md) | White-label title, logo, theme, landing page, and footer links. |
| [Native OIDC login (SSO)](docs/native-oidc.md) | Terminate OpenID Connect SSO in ShinyHub itself - config, claim mapping, group-to-role, sessions/logout, behind-a-proxy, and HA - no external auth proxy. |
| [Identity forwarding to apps](docs/identity.md) | The trusted `X-Shinyhub-*` headers and signed token apps read to know the connected user, with one-call Python/R helpers. |
| [Reverse-proxy auth - Caddy](docs/reverse-proxy/caddy.md) | Authenticate users via Caddy `forward_auth` and forward the identity to ShinyHub. |
| [Reverse-proxy auth - nginx](docs/reverse-proxy/nginx.md) | Authenticate users via nginx `auth_request` and forward the identity to ShinyHub. |
| [CLI/CI behind an auth proxy](docs/reverse-proxy/deploying-behind-a-proxy.md) | Deploy and manage apps from the CLI or CI when ShinyHub is behind an auth proxy that blocks non-browser clients. |
| [OIDC bridge for LDAP/SAML](docs/reverse-proxy/oidc-bridge.md) | Wrap an LDAP or SAML source with an OIDC bridge (Authelia, Authentik, Keycloak) and use ShinyHub's built-in OIDC login. |

## Architecture

```text
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

ShinyHub is one Go binary. `shinyhub serve` runs the HTTP API, the embedded
dashboard UI, the reverse proxy, and the lifecycle watchdog against a single
SQLite database; the same binary provides the developer subcommands (`deploy`,
`login`, `apps`, `env`, `data`, and more). App processes run natively or inside
Docker containers and are proxied per slug.

## Status

Active development. ShinyHub is self-hosted and run in production by the
maintainer, offered with no SLA or support guarantees. It runs single-node on
SQLite by default; an optional high-availability mode (multiple control-plane
instances behind a shared Postgres, with an ownership lease and off-host worker
tiers) is also supported - see
[docs/deployment/ha-data-plane.md](docs/deployment/ha-data-plane.md). See
[CHANGELOG.md](CHANGELOG.md) for the current release.

## Contributing

Issues and pull requests are welcome. See
[CONTRIBUTING.md](CONTRIBUTING.md) for development setup and conventions.

## License

[MIT](LICENSE)
