---
description: "Develop ShinyHub itself, run an app locally with safe reloads, or deploy local changes continuously to an explicit remote development host."
---

# Local development

`shinyhub dev` is the front door for app development. It runs locally by
default and moves the same continuous workflow to a remote host only when you
name that host explicitly.

## Develop an app locally

From an app bundle containing `app.py`, `app.R`, or a `shinyhub.toml` command:

```bash
shinyhub doctor . --local
shinyhub dev .
```

The doctor preflight validates the directory, deployment slug, manifest,
resolved entrypoint, and required executable without starting a process or
contacting a server. It reports all blockers together. After connecting to a
remote, run `shinyhub doctor .` without `--local` to verify the complete deploy
path, then `shinyhub plan .` to inspect the exact bundle and remote effect before
deploying with `shinyhub deploy . --open`. That command waits for health and
hands the working app directly to your browser. Return later with
`shinyhub apps open <slug>`; use `--no-browser` to print the URL on SSH. See
[Doctor](doctor.md) and [Deployment plan](deployment-plan.md).

The command installs dependencies when needed, presents one startup summary
with the source, generated workspace, data location, reload policy, app type,
and readiness contract, then serves the app at a production-shaped URL such as
<http://127.0.0.1:54321/app/my-app/>. The root URL redirects there. Requests
pass through ShinyHub's real proxy, including prefix stripping, forwarding
headers, WebSocket support, and cookie handling.

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
shinyhub dev . --open                 # open after the app is healthy
shinyhub dev . --fresh                # rebuild generated state; keep app data
shinyhub dev . --no-sync              # skip explicit uv/renv preparation
shinyhub dev . --port 8000            # choose the public proxy port
shinyhub dev . --slug sales           # serve at /app/sales/
shinyhub dev . --data-dir ../dev-data # choose durable app-data storage
shinyhub dev . --state-dir /tmp/sales # choose generated workspace state

# Lower-level one-shot controls remain available.
shinyhub run . --check                # boot smoke test, then exit
shinyhub run . --no-reload            # run one process without watching files
```

Environment values come from `.env` by default and may be overridden with
repeatable `--env KEY=VALUE` flags. The parser is identical to `shinyhub env
apply`, including `export`, quoting, comments, duplicate handling, and key
validation. Values are never printed; startup diagnostics list keys only.
`PORT`, `SHINYHUB_APP_DATA`, and `SHINYHUB_APP_SLUG` are platform-managed and
cannot be overridden.

## Develop in a fleet checkout

Fleet context is automatic. From a local-source app declared by the nearest
`fleet.toml`, the ordinary command uses the manifest slug and composes that
app's `[[bundle_file]]` inputs:

```bash
cd apps/sales-dashboard
shinyhub dev .
```

From the fleet root, the same command starts every local-source app on its own
automatic port and prefixes concurrent output with the app slug:

```bash
shinyhub dev .
shinyhub dev . --app sales-dashboard
shinyhub dev . --app sales-dashboard --app operations
shinyhub dev fleet.toml
```

Git-backed entries are named and skipped by the fleet-root default because a
filesystem watcher needs a local checkout. Add `--all` when skipping anything
would be a mistake; the command then fails before starting unless every
declared app has a watchable local source. `--app` is repeatable and the
manifest slug is authoritative, so it cannot be combined with `--slug`.

Use `--standalone` when a directory lives inside a fleet checkout but should be
developed independently. Use `--file path/to/fleet.toml` when discovery cannot
express the intended manifest.

Every selected app receives an isolated generated workspace and default data
directory. For a multi-app run, explicit `--state-dir` and `--data-dir` values
act as roots with one child directory per slug; an explicit `--port` requires a
single `--app`. Editing either an app source or one of its shared bundle inputs
triggers only that app's staged, health-checked reload. Changes to `fleet.toml`
itself still require restarting the command so changes to consumers and
destinations cannot alter a running session unexpectedly.

`dev` consumes the fleet's app identity, source topology, visibility for an
explicit creation, and shared bundle inputs. It does not reconcile projects,
ownership, `[app.config]`, or pruning. Use `shinyhub fleet plan` and
`shinyhub fleet apply` for declarative configuration changes.

## Develop on a remote host

Use a remote development loop when the app depends on the target host's data,
identity, network, runtime, or compute:

```bash
shinyhub dev . --remote dev --slug sales-dev --open
```

That default is intentionally attach-only: `sales-dev` must already exist. A
typo therefore cannot create a durable app by accident. Choose creation
explicitly when you need it:

```bash
# Create a normal app that remains after the watch process stops.
shinyhub dev . --remote dev --create --slug sales-dev

# Create a private scratch app and delete it automatically after eight hours.
shinyhub dev . --remote dev --ephemeral --ttl 8h
```

`--create` fails if the slug already exists. `--ephemeral` also fails on a
collision and generates a recognizable `<directory>-dev-<suffix>` slug when
`--slug` is omitted. Ephemeral apps are always private; their TTL must be from
15 minutes through seven days. Stopping the CLI ends the development session,
but it does not delete an ephemeral app early—the printed expiry remains the
stable deadline, so a URL does not disappear merely because a terminal closed.

Fleet selection works identically in remote mode:

```bash
# Preflight and attach every local-source fleet app that already exists.
shinyhub dev . --remote dev

# Attach one fleet app, with its shared inputs composed into every deployment.
shinyhub dev . --app sales-dashboard --remote dev

# Creation stays deliberately single-app.
shinyhub dev . --app sales-dashboard --remote dev --create
shinyhub dev . --app sales-dashboard --remote dev --ephemeral --ttl 8h
```

A multi-app remote session verifies that every selected target exists before
the first deployment. If any target is missing, nothing is mutated and the CLI
points to `shinyhub fleet apply`. `--create` and `--ephemeral` therefore require
one selected app; mass creation belongs to the fleet convergence workflow.
Each app gets its own durable development-session ID and Deployments group.
Multi-app NDJSON records include an `app` field, while terminal output is
prefixed with the manifest slug.

Remote development requires `--remote <host>`, where the value is a saved host
name or server URL. The saved current host and `SHINYHUB_HOST` never turn a
local `dev` invocation into a remote mutation. It starts and waits for the app,
opens it only after the first successful deployment when `--open` is present,
and keeps the source and target visible in its startup banner.

After the initial deployment, ShinyHub observes the local source tree. It waits
for a 750 ms quiet period after a save burst, builds the same canonical bundle
as ordinary `deploy`, and sends only the latest state. Changes that arrive while
a deployment is running are coalesced into one follow-up attempt. If generated,
ignored, or metadata-only changes leave the content digest unchanged, no remote
mutation occurs.

A build, hook, startup, or readiness failure is printed without ending the
watch process; fix the source and save again. The server attempts to restore the
previous successful version after a failed candidate. Remote deployments
currently cycle the app pool during the handoff, so viewers may see a brief
interruption on each deployed change. Use a dedicated development slug rather
than a production app when that interruption is unacceptable.

Every attempted change is an ordinary durable deployment row: successful
versions remain rollback-capable and failed attempts keep their failure reason.
A generated development-session ID ties those rows to their replica logs and
proxy traces. The Deployments tab shows one compact **Remote development** item
per watch process—with actor, target type, save/failure counts, current release,
and start/end state—and expands to the complete attempt history. Its **View
logs** and **View traces** links filter observability to the deployments in that
session. Because post-deploy hooks may have non-idempotent side effects, watch
mode refuses a manifest containing hooks unless you explicitly add
`--allow-repeated-hooks` after reviewing them.

Useful options and constraints:

```bash
shinyhub dev . --remote dev --slug sales-dev
shinyhub dev . --remote dev --create --slug sales-dev
shinyhub dev . --remote dev --ephemeral --ttl 8h
shinyhub dev . --remote dev --watch-delay 2s
shinyhub dev . --remote dev --allow-repeated-hooks
shinyhub dev . --remote dev --output ndjson
```

`--create` and `--ephemeral` are mutually exclusive. `--watch-delay` accepts
100 ms through one minute. A continuous command cannot emit one JSON document,
so use NDJSON for automation. Press Ctrl-C to stop watching; the last successful
remote deployment remains in place.

`shinyhub run` and `shinyhub deploy --watch` remain supported as lower-level
commands for scripts and specialist options. The latter requires an explicit
`--host` or `SHINYHUB_HOST` and does not support `--git`; watch a local checkout
instead. New interactive workflows should prefer `shinyhub dev` so local and
remote development share one mental model.

The older fleet-local entry point also remains available for scripts that need
the lower-level `--check` or `--no-reload` controls:

```bash
shinyhub fleet dev sales-dashboard
shinyhub fleet dev sales-dashboard -f config/fleet.toml --check
```

`shinyhub run` remains standalone and intentionally does not discover or
compose fleet inputs.

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

### Real-cluster Fargate smoke test

Changes to the Fargate runtime or external log handoff should also compile and,
when AWS test infrastructure is available, run the opt-in real-cluster gate:

```bash
make test-fargate-it
```

The target skips unless `SHINYHUB_FARGATE_IT_CLUSTER` is set. A configured run
launches one billed Fargate task, verifies routing and inventory, persists the
exact ECS task Logs handoff in a migrated local database, stops the task, and
proves both AWS and the immutable run still identify that stopped execution.
The test installs an emergency cleanup before making assertions, gives the main
lifecycle eight minutes, and gives cleanup another two minutes to confirm the
task reaches `STOPPED`.

AWS documents that stopped tasks remain available to `DescribeTasks` for at
least one hour. The smoke test checks that immediate post-stop window; the
database assertion separately proves ShinyHub keeps the exact handoff with its
immutable run after the runtime lifecycle ends. See the
[ECS `DescribeTasks` API](https://docs.aws.amazon.com/AmazonECS/latest/APIReference/API_DescribeTasks.html).

Set the cluster, task definition, container, subnets, and optional network,
region, command, and port variables documented in
[`internal/fargate/integration_test.go`](https://github.com/rvben/shinyhub/blob/main/internal/fargate/integration_test.go).
AWS credentials use the standard SDK chain (`AWS_PROFILE`, environment
credentials, SSO, or an instance role). The principal needs permission to run,
describe, list, tag, and stop tasks and to pass the task roles used by the task
definition.

After that lifecycle test, its stopped task's CloudWatch stream can verify the
authenticated multi-viewer API path without launching another task:

```bash
AWS_PROFILE=shinyhub-it \
SHINYHUB_PROVIDER_LOG_IT_REGION=eu-west-1 \
SHINYHUB_PROVIDER_LOG_IT_GROUP=/shinyhub-it/provider-canary \
SHINYHUB_PROVIDER_LOG_IT_STREAM=app/app/<task-id> \
SHINYHUB_PROVIDER_LOG_IT_EXPECT=<expected-message> \
make test-provider-logs-it
```

This opt-in canary makes exactly two `GetLogEvents` calls. It proves eight
adjacent authenticated viewers share one provider request, a later request
refreshes after the one-second sharing window, and Prometheus reports two
`ok` reads plus seven `shared` reads. It creates no AWS resources.
