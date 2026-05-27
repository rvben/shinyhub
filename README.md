# ShinyHub

[![PyPI](https://img.shields.io/pypi/v/shinyhub)](https://pypi.org/project/shinyhub/)
[![CI](https://github.com/rvben/shinyhub/actions/workflows/ci.yml/badge.svg)](https://github.com/rvben/shinyhub/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

Self-hosted platform for deploying and operating [R and Python Shiny](https://shiny.posit.co/)
applications. Push an app from the CLI, get a clean URL behind a built-in
reverse proxy, sign users in with OAuth or OIDC, and let idle apps hibernate
and wake on demand. ShinyHub runs as a single Go binary backed by SQLite, with
no external services to operate.

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
- **Reverse proxy.** One URL per app under `/app/<slug>/`, sticky-session aware.
- **Hibernation.** Idle apps are stopped automatically and restarted on the next request.
- **Authentication.** Username/password, GitHub OAuth, Google OAuth, or generic OIDC (Okta, Azure AD, Keycloak, Auth0).
- **Access control.** Public, private, or shared apps, with per-app member roles.
- **Env vars and secrets.** Per-app key-value store; secrets encrypted at rest with AES-256-GCM. See [docs/environment.md](docs/environment.md).
- **Persistent data dir.** Each app gets a `data/` directory that survives deploys, with `shinyhub data push|ls|rm` and a UI tab. See [docs/data.md](docs/data.md).
- **Scheduled jobs and shared data.** Per-app cron schedules and read-only cross-app data mounts for fetcher to consumer pipelines. See [docs/schedules.md](docs/schedules.md).
- **Horizontal scaling.** Set `replicas: N` to run multiple load-balanced backends per app, recovered independently on crash. See [docs/scaling.md](docs/scaling.md).
- **Fleet reconcile.** Declare a whole set of apps in one `shinyhub-fleet.toml` and converge the server to match, kubectl-apply style. See [docs/fleet.md](docs/fleet.md).
- **Observability.** OpenTelemetry tracing (proxy, app, and control-plane spans), an opt-in Prometheus `/metrics` endpoint, and a structured access log with request-ID and trace correlation. See [docs/tracing.md](docs/tracing.md) and [docs/metrics.md](docs/metrics.md).
- **Container isolation (optional).** Run each app inside a Docker container with CPU and memory limits.
- **Branding (white-label).** Customize the front door (title, logo, theme, landing page) without forking. See [docs/branding.md](docs/branding.md).
- **Audit log.** Mutating actions recorded for admin review.
- **Single binary, SQLite, no external dependencies.**

## Quick start

### Docker (recommended)

```bash
mkdir -p ./data
cp shinyhub.yaml.example ./shinyhub.yaml
# Edit shinyhub.yaml: set auth.secret to the output of `openssl rand -hex 32`.

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
(database, bundles, and app data dir all land there). Open
`http://localhost:8080` and log in with the admin credentials you set.

### PyPI

ShinyHub is published to PyPI, so `uv` or `pip` can install the CLI and server:

```bash
uv tool install shinyhub        # isolated tool venv
uvx shinyhub deploy ./my-app --slug demo   # one-shot, without installing
pip install shinyhub            # or via pip
```

### Binary

```bash
curl -fsSL https://raw.githubusercontent.com/rvben/shinyhub/main/scripts/install.sh | sh
# Or download from https://github.com/rvben/shinyhub/releases

cp shinyhub.yaml.example shinyhub.yaml
# Set auth.secret to a 32-byte random value.

SHINYHUB_CONFIG=./shinyhub.yaml \
  SHINYHUB_ADMIN_USER=admin SHINYHUB_ADMIN_PASSWORD=change-me \
  shinyhub serve
```

### From source

```bash
git clone https://github.com/rvben/shinyhub.git
cd shinyhub
go build -o bin/shinyhub ./cmd/shinyhub
```

## Configuration

Every key is documented inline in
[`shinyhub.yaml.example`](shinyhub.yaml.example). Environment variables
(prefixed `SHINYHUB_`) override the YAML.

The server resolves its config file in this order: the `--config` flag
(`shinyhub serve --config /path/to/shinyhub.yaml`, also honored by `backup` and
`restore`), then the `SHINYHUB_CONFIG` environment variable, then
`./shinyhub.yaml`.

The one required setting is `auth.secret`: a random 32+ character string
(`openssl rand -hex 32`). The server refuses to start with the placeholder
value.

## Guides

| Guide | Topic |
|---|---|
| [Environment and secrets](docs/environment.md) | Per-app env vars, encrypted secrets, and when to use them instead of files. |
| [Persistent data dir](docs/data.md) | Pushing data, the app-visible path, authorization, quota, and concurrency. |
| [Scheduled jobs and shared data](docs/schedules.md) | Per-app cron schedules and read-only cross-app data mounts. |
| [Horizontal scaling](docs/scaling.md) | Per-app replicas, load balancing, and session admission. |
| [Fleet reconcile](docs/fleet.md) | Declaring and converging a whole set of apps from one file. |
| [Deploy manifest](docs/manifest.md) | The `shinyhub.toml` bundle manifest. |
| [Tracing](docs/tracing.md) | OpenTelemetry propagation, app spans, and control-plane spans. |
| [Metrics and logs](docs/metrics.md) | The `/metrics` endpoint, exposed series, and the structured access log. |
| [Branding](docs/branding.md) | White-label title, logo, theme, landing page, and footer links. |

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

Active development. ShinyHub is single-node and self-hosted (no clustering or
HA) and is run in production by the maintainer, offered with no SLA or support
guarantees. See [CHANGELOG.md](CHANGELOG.md) for the current release.

## Contributing

Issues and pull requests are welcome. See
[CONTRIBUTING.md](CONTRIBUTING.md) for development setup and conventions.

## License

[MIT](LICENSE)
