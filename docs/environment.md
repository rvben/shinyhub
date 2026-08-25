---
description: "Per-app environment variables, with values marked secret encrypted at rest under AES-256-GCM and never returned in plaintext."
---

# Environment Variables and Secrets

Every app has its own key-value environment store. Non-secret values are stored
in plaintext; values marked `--secret` are encrypted at rest with AES-256-GCM
(the key is derived from `SHINYHUB_AUTH_SECRET` via HKDF-SHA256) and can never
be read back through the API or UI.

Per-app env vars reach every code path the app controls: the app process, the
host-side dependency build (`uv sync` / `renv::restore`), and post-deploy
hooks. The build and hooks see the same variables the app sees at start, so a
private package-index credential stored as a secret env var works during
dependency resolution. (One exception: the best-effort conversion of a
requirements.txt-only bundle into a uv project sees only the service
environment, not per-app vars.)

## When to use env vars vs persistent data

| You want to... | Use |
|---|---|
| Configure a cloud bucket URL, DB URL, or API endpoint | Env var (non-secret) |
| Pass a password, API key, or private-key string | Env var (secret) |
| Ship a Parquet / DuckDB / SQLite file the app reads | [Persistent data dir](data.md) |
| Let the app write uploads, cache, or session data | [Persistent data dir](data.md) |

## CLI

```bash
shinyhub env set demo AWS_REGION=eu-west-1
shinyhub env set demo AWS_SECRET_ACCESS_KEY --secret --stdin   # value from stdin
shinyhub env set demo LOG_LEVEL=debug --restart                # restart the app after setting
shinyhub env ls demo
shinyhub env rm demo OLD_VAR
```

Keys must match `[A-Z_][A-Z0-9_]*`. Values are capped at 64 KiB each, with at
most 100 keys per app.

## UI

Open an app's **Settings** modal and switch to the **Environment** tab to list,
add, edit, and delete variables. Secret values are masked in the list and are
write-only once created.

## Reserved prefix

Keys starting with `SHINYHUB_` are reserved for platform variables
(`SHINYHUB_APP_DATA`, and future additions) and are rejected with a 422.

## What apps and builds inherit from the server environment

The service's own environment is not passed through wholesale. Every
app-controlled code path - the app process, the dependency build (`uv sync` /
`renv::restore`), and post-deploy hooks - receives an allow-listed subset, so
control-plane secrets (`SHINYHUB_AUTH_SECRET`, cloud credentials, tokens)
never reach deployer-controlled code. Per-app env vars (above) are layered on
top of this inherited base. The allow-list covers, by category:

- **OS/runtime essentials:** `PATH`, `HOME`, `USER`, locale (`LANG`, `LC_*`),
  `TERM`, `TZ`, temp dirs.
- **TLS trust:** `SSL_CERT_FILE`, `SSL_CERT_DIR`, `CURL_CA_BUNDLE`,
  `REQUESTS_CA_BUNDLE`, `NODE_EXTRA_CA_CERTS`.
- **Proxies:** `HTTP_PROXY`, `HTTPS_PROXY`, `NO_PROXY`, `ALL_PROXY` (upper- and
  lower-case).
- **Tool directories:** `XDG_*`, `UV_CACHE_DIR`, `UV_PYTHON_INSTALL_DIR`,
  `PIP_CACHE_DIR`, `R_LIBS*`, `RENV_PATHS_CACHE`.
- **Build interpreter:** `UV_PYTHON_PREFERENCE`, `UV_PYTHON`,
  `UV_PYTHON_INSTALL_MIRROR` - see [Build interpreter provisioning](#build-interpreter-provisioning).
- **Package indexes:** see the next section.

Anything else is dropped. To pass an additional variable through, name it in
`SHINYHUB_APP_ENV_ALLOW` (comma-separated) in the service environment:

```ini
Environment="SHINYHUB_APP_ENV_ALLOW=MY_VAR,OTHER_VAR"
```

## Private package indexes

Apps whose dependencies live on a private registry (Nexus, Artifactory, a
private CRAN) are supported by setting the standard tool variables in the
service environment; they pass through to every build:

- **uv:** `UV_DEFAULT_INDEX`, `UV_INDEX`, `UV_INDEX_URL`, `UV_EXTRA_INDEX_URL`,
  `UV_FIND_LINKS`, `UV_INDEX_STRATEGY`, and the per-index credentials
  `UV_INDEX_<NAME>_USERNAME` / `UV_INDEX_<NAME>_PASSWORD`.
- **pip:** `PIP_INDEX_URL`, `PIP_EXTRA_INDEX_URL`.
- **renv:** `RENV_CONFIG_REPOS_OVERRIDE`.

Example (systemd unit):

```ini
Environment="UV_EXTRA_INDEX_URL=https://nexus.example.com/repository/pypi-internal/simple"
```

A bundle can also declare its index self-contained in `pyproject.toml` with
`[[tool.uv.index]]`; the build sandbox does not restrict network egress, so
either approach reaches the index directly or via the configured proxy.

Each build logs its effective index configuration (credentials redacted), and
a "not found in the package registry" failure is annotated with the index
configuration the build actually saw - or with a pointer to this page when
none reached it.

**Credential visibility:** a build executes deployer-controlled code (build
backends, configure scripts), so any index credential a build uses is readable
by that build. Index variables set in the service environment are server-wide:
treat them as visible to everyone who can deploy to the instance. On a
multi-tenant instance, scope credentials to the app instead - store them as
per-app env vars, which reach only that app's builds and hooks:

```bash
shinyhub env set demo UV_INDEX_CORP_USERNAME=svc-demo
shinyhub env set demo UV_INDEX_CORP_PASSWORD --secret --stdin
```

`shinyhub run` mirrors this locally: variables passed via `--env`/`.env` reach
the local dependency build the same way per-app vars reach a server build.

## Build interpreter provisioning

Native Python apps build with [uv](https://docs.astral.sh/uv/). By default uv
provisions the Python interpreter an app's `requires-python` needs by
downloading a managed CPython from GitHub's
[python-build-standalone](https://github.com/astral-sh/python-build-standalone)
releases. On a host whose egress cannot reach GitHub (an air-gapped or
proxy-restricted network), that download is blocked and the deploy fails with
`failure_kind: interpreter_unavailable` and a hint naming the knobs below.

The `build:` section of the server config declares the interpreter policy for
every native build (`uv sync`, and the `uv init`/`uv add` project-synthesis
step for a `requirements.txt`-only app), for serve-time `uv run`, and for
host-side post-deploy hooks. It is the interpreter analogue of the
private-package-index support above, and is **server-scoped**: interpreter
provisioning is a property of the host, not the app, so there is no per-app
knob, and a configured field is **authoritative** - it overrides an app that
sets the same `UV_PYTHON_*` variable as a per-app env var.

```yaml
build:
  # UV_PYTHON_PREFERENCE. One of: only-managed, managed (uv's default),
  # system, only-system. Use only-system on a host that cannot download a
  # managed CPython, to build against a preinstalled interpreter.
  python_preference: only-system
  # UV_PYTHON. An explicit interpreter: a version ("3.12") or an absolute path.
  # Leave empty to let each app's requires-python decide.
  python: ""
  # UV_PYTHON_INSTALL_MIRROR. Base URL of an internal mirror of the
  # python-build-standalone releases, for hosts allowed to download managed
  # interpreters only from an approved host.
  python_install_mirror: ""
```

Each field maps one-to-one onto uv's own environment variable. The policy is
recorded in memory at startup and applied as the outermost layer of every
native uv invocation, so it reaches the paths that have no injectable env seam
and wins over any per-app value. It is deliberately not written to the process
environment: a zero-downtime re-exec hands the successor the current
environment, so an exported value could never be un-set by emptying a `build:`
field; recomputing from the freshly loaded config lets a removed key take
effect on the next handoff. Equivalent env-var overrides
(`SHINYHUB_BUILD_PYTHON_PREFERENCE`, `SHINYHUB_BUILD_PYTHON`,
`SHINYHUB_BUILD_PYTHON_INSTALL_MIRROR`) take precedence over the YAML.

Setting `UV_PYTHON_PREFERENCE` directly in the service environment also works -
it is allow-listed and reaches every build - but it is not host-authoritative:
it sits in the scrubbed base, so a per-app value overrides it. Prefer the
`build:` section: it is validated at startup (a typo'd `python_preference` fails
the load instead of every app's build), authoritative over per-app env,
documented, and portable in a fleet manifest.

This does not solve reachability. It selects a preinstalled interpreter or an
approved mirror; a host with no suitable interpreter and no reachable source
still cannot build. The Docker and Fargate runtimes are unaffected - they bake
the interpreter into the image.

### Supported interpreter builds

ShinyHub's production native path supports the standard, GIL-enabled CPython
build selected by the application's `requires-python` constraint. ShinyHub does
not maintain a second compatibility promise for free-threaded (`cp313t`,
`cp314t`, and later `t`-ABI) interpreters today. They are experimental: the
control plane does not reject them, but deployment success depends on every
binary dependency in the application publishing a compatible wheel or building
successfully from source.

This boundary is deliberately app-level. ShinyHub cannot infer thread safety
from a successful install, and should not claim fleet support while core app
dependencies remain unavailable. Operators evaluating a free-threaded build
must resolve with source builds disabled first, run the application's complete
test suite, and load-test real sessions. Standard CPython remains the supported
default.

## Caveat: rotating `SHINYHUB_AUTH_SECRET`

The encryption key is derived from `SHINYHUB_AUTH_SECRET`. Rotating that secret
invalidates every stored secret value: the affected apps fail to read their
secrets until the variables are re-set via the CLI or UI.
