---
description: "Build, run, test, and validate the ShinyHub control plane, command-line interface, dashboard, and documentation locally."
---

# Develop ShinyHub itself

This guide is for contributors changing the ShinyHub server, CLI, or dashboard.
To develop an application that runs on ShinyHub, use
[`shinyhub dev`](../local-development.md) instead.

Install exact project dependencies and the repo-local, pinned live-reload tool:

```bash
make bootstrap
```

Start the development server:

```bash
make dev
```

Open <http://127.0.0.1:8080> and log in with `admin` / `admin`. Go edits rebuild
the binary; dashboard assets are served directly from `internal/ui/static`. If
a Go edit does not compile, the last healthy server stays online while the
error remains visible in the terminal. `make run` provides the same seeded
login without file watching.

## Reset local state

```bash
make dev-reset
```

The old `data/` is archived under `tmp/dev-data-backup-<timestamp>` rather than
deleted. Restore it by stopping the server, moving the fresh `data/` aside, and
moving the backup back to `data/`. `make clean` removes `tmp/`, including those
backups, so recover anything you need first.

## Run the standard gate

Before opening a pull request:

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
second sign-in. Set `SHINYHUB_E2E_BROWSER` to a Chrome or Chromium executable
when auto-detection cannot find one. Set `E2E_KEEP=1` to retain logs and
screenshots.

## Run product acceptance

```bash
make test-acceptance
```

This builds the current server and starts a disposable loopback hub with its own
database, random credentials, storage, and Python Shiny app. It requires Go,
Node 20+, uv, and an installed Python 3.14 interpreter. The Make target installs
the repository's locked browser dependencies and Chromium. To select a different
installed Python, set `PYTHON_VERSION`; the suite does not download interpreters.

With dependencies already installed, `npm run test:acceptance` runs the same
suite. `npm run test:acceptance -- --api-only` runs the backend portion only.
`SHINYHUB_E2E_BINARY` can select an already-built binary; no flag or environment
variable can redirect the suite to an existing hub or database.

The suite exercises deployment and invalid-upload recovery, settings, data and
shared mounts, successful and failed scheduled jobs, token revocation,
announcement replay/conflicts, membership and role restrictions, and audit
history, followed by rollback, stop/restart, and deletion of a deployed app.
Browser journeys cover sign-in, a real reactive app session, settings
save/discard/navigation confirmation, new-app deployment, token management,
profile changes, announcement conflict recovery, and copying audit deep links.
Logs, traces, and resource history must contain live app activity; the disposable
tracing collector listens only on loopback.
All dashboard/app pages and a schedule detail are checked in both themes at
390, 1113, and 1440px. Additional checks straddle the overview's layout
breakpoint, so an empty grid column fails even without horizontal overflow.
Phone checks also open the new-app, token, and profile dialogs and the
announcement editor, and exercise navigation and permission redirects.
Browser axe checks cover WCAG A/AA rules on desktop/light and phone/dark; these
checks complement manual accessibility review, rather than certify compliance.

Results, server logs, and masked screenshots go under the ignored
`loadtest/results/acceptance-*/` directory. Failure exits nonzero. The suite
stops its server and removes its temporary database, credentials, and app data,
including when an assertion fails. CI runs it in the `test-acceptance` job and
retains diagnostics for seven days. No remote worker, Docker daemon, existing
account, or deployed service is required; live remote-worker behavior remains
covered by `make test-remote-e2e`.

Extend `scripts/acceptance/api.mjs` for public API journeys and
`scripts/acceptance/browser.mjs` for user actions or dashboard routes. Verify
persisted results through the API and observable browser output; do not seed the
database directly or replace API responses. Keep screenshot diagnostics masked
and credentials out of reports. Use explicit readiness conditions rather than
fixed sleeps. The small test hub uses a 0.1-core update reserve and retries only
explicit transient CPU-capacity refusals up to four attempts with a 30-second
retry window; other deployment failures fail immediately. Capacity policy itself
has a dedicated lifecycle gate.

## Run backup, restore, and upgrade acceptance

```bash
git fetch --no-tags origin refs/tags/v0.22.0:refs/tags/v0.22.0
npm run test:recovery
```

CI runs this alongside product acceptance. It builds the pinned 0.22.0 source
release and the current checkout, then deploys a synthetic private Python Shiny
app to a disposable SQLite hub. The test backs up the live source, stops it,
empties its storage, and runs the current build's offline restore. It retains
the fixture's configuration and encryption key, as a real restore requires,
and restores at the original deployment paths.

The test verifies sign-in with the original account, app settings, uploaded
data, and serving the original deployment without uploading it again. A saved
schedule verifies the exact decrypted secret without printing it. No browser,
production backup, or existing service is needed. The recovery test always
builds both binaries; it does not use `SHINYHUB_E2E_BINARY`. The archive and
fixture are removed at cleanup; diagnostic logs use the same ignored results
directory as product acceptance. When moving the recovery baseline, update
the release tag and pinned commit together.

## Run the real-cluster Fargate smoke test

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
refreshes after the one-second sharing window, and Prometheus reports two `ok`
reads plus seven `shared` reads. It creates no AWS resources.
