---
description: "Develop ShinyHub itself, run an app locally with safe reloads, or deploy local changes continuously to an explicit remote development host."
---

# Local development

ShinyHub supports complementary loops for developing the control plane, running
an app locally exactly as ShinyHub expects, and continuously deploying local
changes when development depends on a remote host.

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
deploying with `shinyhub deploy . --open`. That command waits for health and
hands the working app directly to your browser. Return later with
`shinyhub apps open <slug>`; use `--no-browser` to print the URL on SSH. See
[Doctor](doctor.md) and [Deployment plan](deployment-plan.md).

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

## Develop on a remote host

Use a remote development loop when the app depends on the target host's data,
identity, network, runtime, or compute:

```bash
shinyhub deploy . --watch --host dev --slug sales-dev --open
```

That default is intentionally attach-only: `sales-dev` must already exist. A
typo therefore cannot create a durable app by accident. Choose creation
explicitly when you need it:

```bash
# Create a normal app that remains after the watch process stops.
shinyhub deploy . --watch --create --host dev --slug sales-dev

# Create a private scratch app and delete it automatically after eight hours.
shinyhub deploy . --watch --ephemeral --ttl 8h --host dev
```

`--create` fails if the slug already exists. `--ephemeral` also fails on a
collision and generates a recognizable `<directory>-dev-<suffix>` slug when
`--slug` is omitted. Ephemeral apps are always private; their TTL must be from
15 minutes through seven days. Stopping the CLI ends the development session,
but it does not delete an ephemeral app early—the printed expiry remains the
stable deadline, so a URL does not disappear merely because a terminal closed.

Watch mode requires an explicit target through `--host` or `SHINYHUB_HOST`; it
never relies silently on the saved current host. It implies `--start` and
`--wait`, opens the app only after the first successful deployment when
`--open` is present, and keeps the source and target visible in its startup
banner.

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
shinyhub deploy . --watch --host dev --slug sales-dev
shinyhub deploy . --watch --create --host dev --slug sales-dev
shinyhub deploy . --watch --ephemeral --ttl 8h --host dev
shinyhub deploy . --watch --host dev --watch-delay 2s
shinyhub deploy . --watch --host dev --allow-repeated-hooks
shinyhub deploy . --watch --host dev --output ndjson
```

`--create` and `--ephemeral` are mutually exclusive. `--watch-delay` accepts
100 ms through one minute. `--git` is not supported;
watch a local checkout instead. A continuous command cannot emit one JSON
document, so use NDJSON for automation. Press Ctrl-C to stop watching; the last
successful remote deployment remains in place.

Plain `shinyhub run` uses only the selected app directory; it does not compose
`[[bundle_file]]` entries from a fleet manifest. If the app is a consumer in a
valid nearest-parent `fleet.toml`, the command warns on stderr so an incomplete
local bundle is not mistaken for fleet parity. This discovery is advisory and
cannot see a manifest passed elsewhere with `-f`.

### Run a fleet app with shared inputs

Use the fleet-aware local entry point when an app consumes shared bundle files:

```bash
shinyhub fleet dev sales-dashboard
shinyhub fleet dev sales-dashboard -f config/fleet.toml --check
```

`fleet dev` selects the app by its manifest slug, mirrors its local source into
a generated workspace, and composes the selected app's `[[bundle_file]]`
destinations there. It never writes vendored copies into either the app or the
shared source tree, and it does not contact a ShinyHub server. Git-backed apps
are not supported by this V1 local workflow.

The command accepts the local-run flags `--port`, `--no-sync`, `--no-reload`,
`--fresh`, `--env`, `--env-file`, `--data-dir`, `--state-dir`, `--open`, and
`--check`. The app slug is authoritative, so there is no `--slug` flag.
`--no-sync` skips dependency preparation but still mirrors the source and
composes shared files. The default `.env` is read from the selected app source,
not from the fleet manifest directory.

Edits to the app source or any declared shared source trigger the same staged,
health-checked reload as ordinary `run`. A missing shared file, a newly
colliding destination, or a candidate that fails startup leaves the last
healthy process serving. `--no-reload` composes once and runs without watching.
Changes to `fleet.toml` itself—including consumer or destination changes—require
restarting `fleet dev` in V1.

Default fleet-development state is keyed by the canonical manifest path plus
app slug. Two slugs may therefore use the same read-only source concurrently,
and an ordinary `run` may coexist with `fleet dev`; each gets a separate
workspace, automatic port, and default data directory. This also means their
default app data is intentionally not shared. Pass the same explicit
`--data-dir` when both workflows should see one local data set. Passing the same
explicit `--state-dir` to concurrent commands instead produces an actionable
workspace-lock error.

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
