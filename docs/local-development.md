# Local development

ShinyHub supports two complementary local loops: developing the ShinyHub
control plane itself and running an app exactly as ShinyHub expects it to run.

## Run an app locally

From an app bundle containing `app.py`, `app.R`, or a `shinyhub.toml` command:

```bash
shinyhub doctor . --local
shinyhub run .
```

The doctor preflight validates the directory, deployment slug, manifest,
resolved entrypoint, and required executable without starting a process or
contacting a server. It reports all blockers together. After connecting to a
remote, run `shinyhub doctor .` without `--local` to verify the complete deploy
path, then `shinyhub plan .` to inspect the exact bundle and remote effect before
deploying. See [Doctor](doctor.md) and [Deployment plan](deployment-plan.md).

The command installs dependencies when needed, prints the source, workspace,
data, app type, and readiness contract it selected, then serves the app at a
production-shaped URL such as <http://127.0.0.1:54321/app/my-app/>. The root
URL redirects there. Requests pass through ShinyHub's real proxy, including
prefix stripping, forwarding headers, WebSocket support, and cookie handling.

Your source directory is treated as read-only. ShinyHub mirrors it into an
OS user-cache workspace keyed by its absolute path; generated `pyproject.toml`,
`uv.lock`, `.venv`, renv files, bytecode, and the `data` symlink never appear in
your checkout or deployment bundle. App data persists outside the source tree.

Edits are copied into a candidate workspace process and health-checked before
the proxy switches over. A syntax error, missing dependency, crash, or failed
readiness check leaves the last healthy process serving. Fix the file and the
next save retries automatically.

Useful options:

```bash
shinyhub run . --open                 # open after the app is healthy
shinyhub run . --check                # boot smoke test, then exit
shinyhub run . --fresh                # rebuild generated state; keep app data
shinyhub run . --no-sync              # skip explicit uv/renv preparation
shinyhub run . --no-reload            # run one process without watching files
shinyhub run . --port 8000            # choose the public proxy port
shinyhub run . --slug sales           # serve at /app/sales/
shinyhub run . --data-dir ../dev-data # choose durable app-data storage
shinyhub run . --state-dir /tmp/sales # choose all generated workspace state
```

Environment values come from `.env` by default and may be overridden with
repeatable `--env KEY=VALUE` flags. The parser is identical to `shinyhub env
apply`, including `export`, quoting, comments, duplicate handling, and key
validation. Values are never printed; startup diagnostics list keys only.
`PORT`, `SHINYHUB_APP_DATA`, and `SHINYHUB_APP_SLUG` are platform-managed and
cannot be overridden.

Use `[app] readiness_path` when `/` is not a meaningful health endpoint. By
default, any 2xx or 3xx response is healthy; add `readiness_status` to require
one exact status:

```toml
[app]
readiness_path = "/health/ready"
readiness_status = 204
startup_timeout_seconds = 180
```

The same contract is used locally and after deployment. Readiness requests do
not follow redirects, so an exact `readiness_status` is checked against the
declared endpoint itself rather than the redirect destination.

Only one runner uses a cached workspace at a time. To run two copies of the
same source concurrently, give each a distinct `--state-dir`.
Workspace and data directories must remain outside the app source so local
tooling can never add generated files to a future deployment bundle.

## Develop ShinyHub itself

Install exact project dependencies and the repo-local, pinned live-reload
tool:

```bash
make bootstrap
```

Start the development server:

```bash
make dev
```

Open <http://127.0.0.1:8080> and log in with `admin` / `admin`. Go edits rebuild
the binary; dashboard assets are served directly from `internal/ui/static`.
If a Go edit does not compile, the last healthy server stays online while the
error remains visible in the terminal. `make run` provides the same seeded
login without file watching.

To start from a clean database and app store:

```bash
make dev-reset
```

The old `data/` is archived under `tmp/dev-data-backup-<timestamp>` rather than
deleted. Restore it by stopping the server, moving the fresh `data/` aside, and
moving the backup back to `data/`. `make clean` removes `tmp/`, including those
backups, so recover anything you need first.

Before opening a pull request, run:

```bash
make check
```

That deterministic gate checks formatting, Go vet, skill metadata, Go tests,
and dashboard JavaScript tests. Specialized integration targets remain
separate because they require Docker, uv, R, or cloud infrastructure.

For changes to packaging, remote connection, login, tokens, or first deploy,
run the release-shaped onboarding gate too:

```bash
make test-browser-onboarding-e2e
```

It builds and installs the Python wheel in isolation, starts the installed
server, pairs the installed CLI through a real headless Chrome session, deploys
an app, revokes the credential in the dashboard, and reconnects without a
second sign-in. Set `SHINYHUB_E2E_BROWSER` to a Chrome/Chromium executable when
auto-detection cannot find one. Set `E2E_KEEP=1` to retain logs and screenshots.
