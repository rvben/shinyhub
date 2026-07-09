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

## Caveat: rotating `SHINYHUB_AUTH_SECRET`

The encryption key is derived from `SHINYHUB_AUTH_SECRET`. Rotating that secret
invalidates every stored secret value: the affected apps fail to read their
secrets until the variables are re-set via the CLI or UI.
