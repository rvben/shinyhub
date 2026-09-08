---
description: "How deploy reports each phase: a spinner in an interactive terminal, durable line-oriented updates in CI, and heartbeats through long builds."
---

# Deployment progress

`shinyhub deploy` reports the work the server is actually doing. Interactive
terminals show a compact spinner for the active phase and settle it into a
completed line. CI and redirected stderr receive durable, line-oriented phase
updates instead of terminal control characters.

Typical phases are:

1. Build and inspect the local bundle.
2. Upload and validate it on the server.
3. Build Python or R dependencies, with elapsed-time heartbeats for long builds.
4. Run manifest post-deploy hooks.
5. Start replicas and check readiness.
6. Record the deployment and apply manifest configuration.
7. Clean up superseded bundle files.

If a deployment fails, the error names the failed phase and keeps the server's
stable `failure_kind`. When a previous version exists, progress also says
whether recovery restored it, left the app stopped, or could not recover it.
Secrets, environment values, package-index credentials, and hook output are not
copied into the progress stream. App logs and `deploy-hooks.log` remain the
detailed diagnostic sources. `deploy-hooks.log` records each hook's command,
completion or failure, duration, and exit status; the deploy result separately
reports declared, run, and runtime-skipped hook counts.

For the interactive end-to-end path, run `shinyhub deploy . --open`. It implies
`--start` and `--wait`, then verifies a public app through its actual routed URL
and opens it. Browser-launch failure is a convenience failure, not a deployment
failure: the URL remains visible and JSON reports `opened: false`. A route-check
failure is different—it exits non-zero while explicitly preserving the fact
that the deployment became healthy.

## Fleet progress

`shinyhub fleet apply` uses one live display in an interactive terminal. Apps
keep their manifest order, with a spinner for active work, a check for completed
work, and a failure mark for errors. Each row shows its current phase and elapsed
time; schedule waits include the schedule and run ID, and health waits show the
observed status. Countdown values are labeled `timeout in`, not estimated finish
times. Warnings and logs remain visible above the display, and the final report
includes recovery commands when needed.

Fleet deploys also negotiate the server's deployment event stream, showing
actual dependency builds, hook execution, replica startup, and recovery phases.
A failure names its phase and retains the server's recovery result (for example,
that the previous deployment remained available). A missing final stream result
is reported as an unknown outcome with an inspection command; it does not cause
an automatic upload retry. Older servers keep their ordinary JSON response.

Health waits explain current server observations: a blocking schedule and run
ID, delayed data activation, an unavailable worker, a replica failure reason, or
a deployment that is still running while the current version serves traffic.
Changes in the reason appear immediately in CI, even when the app status stays
`degraded`. Timeouts retain the latest observed reason. Historical process exits
and completed activations are not treated as current blockers; when the server
has no detail, the CLI keeps the status-only message.

`CI=true` (also `CI=1` or `GITLAB_CI=true`) disables animation and default color,
even if the runner allocates a terminal. Redirected output and `TERM=dumb` also
use durable event lines. Status changes appear promptly; unchanged wait reminders
back off to once a minute. Refresh admission and completion remain explicit.
`FORCE_COLOR=1` can enable color in CI, but never cursor movement. `NO_COLOR` and
`--no-color` still take precedence; in an interactive terminal they preserve the
live layout without color. False CI values such as `false` and `0` do not disable
interactive output.

The live display needs at least 50 columns and room for every app. Smaller
terminals and larger fleets use the event log. If the terminal is resized during
a run, output switches to event lines to preserve existing logs. Long labels are
clipped to prevent wrapping. `LANG=C` selects ASCII progress markers.

With `fleet apply --json`, stdout remains a single JSON report and progress goes
to stderr, using the same terminal/CI rules.

## Automation

Outside CI, redirected stdout defaults to one JSON result document. In CI the
default is the human-readable report, so request JSON explicitly when saving or
parsing the result:

```sh
shinyhub deploy . --output json > deploy-result.json
```

Use NDJSON when automation should observe every phase in real time:

```sh
shinyhub deploy . --output ndjson | jq -c .
```

Every line is an independent JSON object. The stable top-level event types are:

- `phase`: a lifecycle update with `phase`, `status`, and `message`.
- `result`: the terminal success event; `result` contains the normal deploy API
  response.
- `error`: the terminal failure event with `phase`, `status_code`,
  `failure_kind`, and `message`.

Phase statuses are `started`, `progress`, `completed`, `warning`, and `failed`.
Additional optional fields include `elapsed_seconds`, `current`, `total`,
`replica`, `file_count`, `bytes`, and `digest`.

The CLI negotiates this stream through the deploy endpoint. Servers that do not
support it return their original JSON response; the CLI detects that response
and emits a compatible terminal NDJSON event without requiring a version flag
or separate command.
