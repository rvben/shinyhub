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

Open an app's **Configuration** tab to list, add, edit, and delete variables.
Secret values are masked in the list and are write-only once created.

Saving or deleting a variable preserves current sessions by default. Existing
processes keep their environment; newly started processes read the saved values.
Use **Apply saved changes** to apply the whole saved environment, or explicitly
select **Apply now** when saving an individual variable. Both actions drain
existing sessions before restarting the app. Sessions still active after
`server.drain_timeout` (default `60s`) disconnect. Stopped and sleeping apps stay
stopped or asleep and use the new values when they next start.

`env set --restart`, `env rm --restart`, and `env apply --restart` use the same
draining application path. Bulk updates apply once after all edits, including
batches that only delete variables. The API exposes it as
`POST /api/apps/<slug>/env/apply`; individual PUT/DELETE requests may also request
`?restart=true`. Saving succeeds independently of application: a failed restart
is reported so the operator can retry without re-entering secret values.

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

### Index options in `requirements.txt`

A `requirements.txt`-only bundle can name its index the way pip reads it:

```text
--index-url https://__token__:${PRIVATE_INDEX_TOKEN}@pypi.corp.example/simple
--extra-index-url https://mirror.corp.example/simple
--find-links ./wheels
internal-package==2.1
```

ShinyHub honours `-i`/`--index-url`, `--extra-index-url`, `-f`/`--find-links`
and `--no-index`, including in files pulled in with `-r`/`-c`, at every step
that installs the bundle's dependencies: the conversion into a uv project, the
`uv sync` that installs it, and a launch that installs dependencies itself
(`uv run --with-requirements`, used off-host). The uv commands involved ignore
these lines on their own, so without this a private package name would resolve
from PyPI.

- **Precedence:** the bundle's `--index-url` replaces the server's and the
  app's default index (`UV_DEFAULT_INDEX` / `UV_INDEX_URL`), as it would for
  pip. The deploy reports that as a warning naming the replaced setting, with
  the URL redacted. `--extra-index-url` and `--find-links` entries are added
  after the configured ones, so those keep their priority: extra indexes go
  last in `UV_EXTRA_INDEX_URL`, which uv consults after `UV_INDEX`. A relative
  `--find-links` path is relative to the bundle root, where uv runs, even in
  an included file. `UV_FIND_LINKS` is comma-separated, so a find-links URL
  containing a comma fails the build; percent-encode it as `%2C`.
- **Credentials:** `${NAME}` references (upper-case names) expand from the
  app's env vars, so a token stored with `shinyhub env set --secret` never
  needs to be in the bundle. The resolved options reach uv as environment
  variables, never as command-line arguments, so they do not appear in process
  listings or traces. uv writes the index into the generated `pyproject.toml`
  and `uv.lock` without the credential. When a dependency step fails, the
  uv output the deploy error quotes has every URL's credentials and query
  values masked, along with the expanded values.
- **Launch-time secrets:** at launch, an index setting is delivered as a
  secret env var when any part of it may be secret: URL credentials, a query
  string (a signed URL's token, even one written literally in the file), the
  expanded value of a secret env var, or a
  same-named variable that was itself stored as a secret. On Fargate that requires
  `runtime.fargate.secrets`; without it the replica fails to start rather than
  exposing the credential as a plaintext task override.
- **Fail closed:** a `-r`/`-c` include that resolves outside the bundle, or
  that names a URL, stops the build and the launch, rather than reading host
  files or silently dropping the index options it may carry. Ship included
  files inside the bundle.
- **Custom launch commands:** a manifest command that starts with `uv` gets
  the same treatment. `--no-index` has no environment variable, so it is
  added after `run`, including when uv's global options such as `--offline`
  or `--directory <dir>` come first; a `uv` command other than `uv run` fails
  to start when the requirements set `--no-index`.
- A bundle that ships its own `pyproject.toml` owns its index configuration
  there; a `requirements.txt` beside it is not read for index options.

Each build logs its effective index configuration (URL credentials and query
values redacted), and
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

### Shipped `uv.lock` files

A bundle that ships a `uv.lock` is installed exactly as locked: every build
runs `uv sync --frozen`, which takes the versions, hashes and download URLs
from the lock and never rewrites it. The index settings above then do not
change what gets installed, even when they differ from the index the lock was
made against; they apply to bundles without a lock. A plain `uv sync` would
instead re-resolve whenever the server's index configuration differs from the
lock's (a trailing slash is enough), rewrite the lock and install the new
resolution without reporting it.

Because `--frozen` does not compare the lock with `pyproject.toml`, the deploy
does: when `pyproject.toml` declares a requirement the lock does not record, or
the lock records one `pyproject.toml` no longer declares, the upload is
rejected (HTTP 422) with an error naming them, before the running app is
touched. Run `uv lock` and deploy again. A requirement is
compared by package name, by the extras it requests (`httpx[http2]`), and by
where it is declared: the project's dependencies, a named extra, or a
dependency group, so moving a package from an extra into the dependencies also
counts as a change. Version specifiers, markers and `[tool.uv.sources]` are
not compared, so changing only a specifier or a package's source is not
caught; lock after every change to `pyproject.toml`. The check runs on every
uploaded bundle, whatever the runtime or launch command. It never runs when an
already-accepted deployment comes back up (a restart, rollback, restore,
replica recovery or scale-up), so a deployment accepted with a stale lock
before this check existed keeps working: when its environment has to be
rebuilt, a plain `uv sync` re-resolves it as it did at the time. That holds on
managed container runtimes too (Fargate, Scaleway), where the control plane
tells the runner image to resolve such a lock again (see the
[managed runner contract](fargate-runner-contract.md#python-apps)).

The same check runs before anything is uploaded, on the archive a deploy would
send, so `.shinyhubignore` and fleet `[[bundle_file]]` inputs count exactly as
they will at deploy. `shinyhub run --check` and `shinyhub doctor` fail with the
upload's own message. A plain `shinyhub run` or `shinyhub dev` warns and keeps
serving, because the local sync still resolves the lock; each reload checks
again and warns when an edit makes the lock stale. `shinyhub fleet
plan` and `apply` report it before the first change, against servers that
advertise `stale_uv_lock_refusal`; an older server accepts such an upload, so
the plan does not refuse it there.

The lock records absolute download URLs, so every replica must be able to reach
the index the lock was made against. To install through a different index, lock
against it (`uv lock --default-index <url>`) before deploying.

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

### Python runtime baseline

The default Docker image and reference Fargate runner use standard Python 3.14
(`ghcr.io/astral-sh/uv:python3.14-bookworm-slim`). Local Python test targets also
default to 3.14. Existing explicit image overrides stay authoritative. Native
apps still select their interpreter through `build.python` or `requires-python`;
the server does not replace an app's interpreter constraint with a fleet-wide
version. Set `build.python: "3.14+gil"` to choose the stable baseline for native
apps, explicitly selecting the standard build even when a free-threaded
interpreter is also installed.

### Evaluating Python 3.15

Python 3.15 can be selected explicitly for native apps through `build.python`,
or through the app's interpreter constraint when the server leaves that field
empty. Use a current uv release: downloadable interpreter versions are bundled
with uv, so an old uv may not know about 3.15. An explicit interpreter path also
works. Docker and Fargate require their own tested image; the shipped Python
image default is 3.14.

The compatibility matrix runs the Python SDKs, shared tracing bootstrap,
Python CLI entrypoint, real native launch, agent session and deployed browser
lifecycle on 3.12, 3.14 and 3.15. Metadata allowing a Python version does not
establish compatibility with every app's binary dependencies.

Before moving production apps, resolve their locked dependencies with source
builds disabled, then run their complete tests and representative session load.
Compare readiness, first render, steady session latency and memory. The local
runtime benchmark (`loadtest/python-runtime/README.md`) uses one dependency
lock across both runtimes and measures real sessions through the native proxy.
Release candidates, missing wheels or a material regression keep the production
default unchanged. Lazy imports, JIT and free-threaded builds remain explicit
experiments; none is enabled by ShinyHub's 3.15 compatibility checks.

### Local Python diagnostics

Run on the machine hosting the Python process, using the interpreter PID rather
than a uv launcher PID. Select a Python 3.15+ interpreter compatible with the
target's version and build:

```bash
shinyhub diagnose python 12345 --python /path/to/python3.15 --output table
shinyhub diagnose python 12345 --python /path/to/python3.15 --async-aware --output table
shinyhub diagnose python 12345 --python /path/to/python3.15 --save profile.html --duration 30s
```

The default command dumps every thread once. `--async-aware` includes suspended
asyncio tasks. `--save` collects a bounded profile, writes an HTML flame graph
with permissions `0600`, and refuses an existing destination. Profiling is
optional and does not install instrumentation into the app or restart it.

Tachyon needs permission to read the target process's memory. Linux ptrace policy,
container PID namespaces and capabilities, app user isolation, and macOS
debugging permissions can prevent attachment. The command reports the failure
without changing these settings. For containers or remote workers, run within
the target's PID namespace on its host; a PID from another machine must never
be passed to this local command. Profiles and dumps can expose paths, source
lines and application details, so keep them private.

## Caveat: rotating `SHINYHUB_AUTH_SECRET`

The encryption key is derived from `SHINYHUB_AUTH_SECRET`. Rotating that secret
invalidates every stored secret value: the affected apps fail to read their
secrets until the variables are re-set via the CLI or UI.
