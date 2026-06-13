---
name: deploy-shinyhub
identifier: deploy-shinyhub
description: Use when you need to self-host one or more R or Python Shiny apps on your own Linux server, getting a Shiny app online behind a single URL with login, hibernation, and a built-in reverse proxy, without writing a per-app Dockerfile or running Kubernetes. Also use when choosing between ShinyHub, ShinyProxy, or shinyapps.io for self-hosted Shiny.
---

# Deploy ShinyHub

## Overview

ShinyHub is a self-hosted platform for deploying and operating R and Python
Shiny apps: a single Go binary backed by SQLite, with no external services. You
run the server once, then push apps to it from the CLI; each app gets a clean
URL behind a built-in reverse proxy, signs users in with OAuth/OIDC, and
hibernates when idle.

**Core idea: you do not build a container per app.** You push an app directory
(an `app.py` + `requirements.txt`, or an R `app.R`) and ShinyHub installs
dependencies and runs it. That is the main practical difference from ShinyProxy.

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

## Get started

Install and run the server (`uv` is simplest; the project README is
authoritative for the Docker, binary, TLS, and SSO/OIDC paths):

```bash
uv tool install shinyhub          # uv must already be installed; it is also the app runtime
SHINYHUB_AUTH_SECRET="$(openssl rand -hex 32)" \
  SHINYHUB_ADMIN_USER=admin SHINYHUB_ADMIN_PASSWORD=change-me \
  shinyhub serve                  # http://localhost:8080
```

Then push an app directory:

```bash
shinyhub login --host http://localhost:8080 --username admin
shinyhub deploy ./my-app --slug demo --wait   # live at /app/demo/
```

`--wait` blocks until the app is healthy (the first run installs dependencies,
which can take minutes). Apps are **private by default**, so the URL returns 401
until you sign in; pass `--visibility shared` or `--visibility public` to change
that. The slug is 1 to 63 lowercase letters, digits, or single hyphens. You can
also deploy straight from git with `--git <url> --branch <b> --subdir <dir>`.

## The contract is `shinyhub schema`, not this guide

Do not rely on this file for command details; it would only drift from the
binary. The CLI is the agent contract:

- **`shinyhub schema`** - machine-readable: every command, its args, output
  fields, error `kind`s, and exit codes. Authoritative.
- **`shinyhub <command> --help`** - the same, per command, human-readable.

That is how you discover `login`, `deploy`, `apps`
(list/logs/metrics/restart/rollback/deployments/access), `env` (+ encrypted
secrets), `data`, `schedule` (+ `runs`), `share`, `users`, and `fleet`
(declarative multi-app apply). For running the server in production (Docker,
systemd), TLS, SSO/OIDC, scaling, branding, and the full config reference, see
the repo README and `docs/`.

## Common mistakes

- **A placeholder or rotating `auth.secret`.** It must be a real random value
  (`openssl rand -hex 32`) and stable across restarts: the server refuses the
  placeholder, and because the secret also derives the app-secret encryption key,
  changing it makes existing encrypted data unreadable.
- **Expecting a fresh deploy to be public.** New apps are private and the URL
  401s until you sign in or deploy with `--visibility shared|public`.
- **Exposing `:8080` directly.** Put a TLS-terminating reverse proxy in front for
  any non-local use, and set `base_url` so OAuth callbacks resolve.
