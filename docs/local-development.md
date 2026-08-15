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
[`internal/fargate/integration_test.go`](../internal/fargate/integration_test.go).
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
