---
description: "Install ShinyHub as a Python package, a standalone binary, or a container image, starting from one uvx command for a local evaluation."
---

# Installation

ShinyHub is distributed as a Python package, a standalone server binary, and a
container image. Start with one `uvx` command for a local evaluation; install it
once, use Docker, or use the standalone binary for a long-lived server.

## What it needs to run

ShinyHub itself is small. It is one binary with an embedded database and no
external services, and an idle server with a handful of applications registered
sits in the tens of MiB resident. Size the host for the applications instead:
every running replica is its own R or Python process, and those dominate both
memory and CPU.

- **Memory.** Budget per replica, not per application: replicas multiplied by
  what one worker of that application costs, plus headroom. The platform's own
  floor assumes a Shiny worker in the 150 to 350 MiB range and refuses to
  allocate a new elastic worker when free memory would fall below
  `server.min_available_memory_mb` (256 MiB by default). A single-application
  evaluation host is comfortable well under 1 GiB; a real fleet is sized by
  adding up its workers.
- **CPU.** The platform reserves no cores for itself. Concurrency comes from
  replicas and workers, so cores are sized against the application processes
  you intend to run at once. See [Scaling](../scaling.md).
- **Disk.** The dependency trees are what grow, not the platform. A minimal R
  application whose only dependency is `shiny` occupies about 60 MiB once its
  library is restored, and each application keeps
  `storage.version_retention` deployments (5 by default) plus its logs. A
  single bundle upload is capped at `storage.max_bundle_mb`, 128 MiB by
  default.

Hibernation is what lets a modest host hold many applications: one that has
been idle past its timeout (30 minutes by default) is stopped and its memory
released, and the next request starts it again. See
[App performance](../app-performance.md) for the trade-off against first-hit
latency.

## Try it without installing

```bash
uvx shinyhub serve
```

On the first interactive run, ShinyHub creates a private loopback-only server,
opens the dashboard, and deploys a real Python Shiny example with bundled data.
The configuration, SQLite database, bundles, and app data persist in the current
directory, even though `uvx` manages the executable for you.

## Install with `uv`

[`uv`](https://docs.astral.sh/uv/) installs the ShinyHub CLI and server in an
isolated tool environment. It is also the runtime ShinyHub uses for Python apps.

```bash
uv tool install shinyhub
shinyhub serve
```

This provides the same first-run experience without resolving the tool on each
invocation. ShinyHub asks for an administrator username and password, creates a
private `shinyhub.yaml`, stores its SQLite database and application data under
`./data/`, and opens `http://127.0.0.1:8080`.

Use `shinyhub init` when you prefer to create and inspect the configuration
before starting the server.

## Install with pip

```bash
python -m pip install shinyhub
shinyhub serve
```

The package includes the server binary. Install `uv` separately before deploying
Python applications.

## Install the standalone binary

```bash
curl -fsSL https://raw.githubusercontent.com/rvben/shinyhub/main/scripts/install.sh | sh
shinyhub serve
```

The standalone binary does not include R or Python. Install the application
runtimes on the host, or configure ShinyHub's Docker runtime.

## Run with Docker

The published image contains the control plane, dashboard, API, and proxy. Use
the reference [Docker Compose deployment](../deployment/docker-compose.md) when
you also want ShinyHub to start applications as containers.

```bash
docker pull ghcr.io/rvben/shinyhub:latest
```

## Build from source

```bash
git clone https://github.com/rvben/shinyhub.git
cd shinyhub
go build -o bin/shinyhub ./cmd/shinyhub
bin/shinyhub serve
```

ShinyHub requires the Go version declared in `go.mod`.

## Next step

Continue with the [quick start](quickstart.md) to explore the included live app
and deploy your own through the same workflow used against a remote server.
