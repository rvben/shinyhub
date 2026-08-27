---
description: "Build, run, test, and validate the ShinyHub control plane itself."
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
