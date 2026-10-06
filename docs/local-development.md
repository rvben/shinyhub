---
description: "Use one safe development command for a standalone app, every local app in a fleet, or an explicit remote ShinyHub host."
---

# Develop applications

`shinyhub dev` is the front door for app development:

```bash
shinyhub dev .
```

Your directory chooses the scope. Local execution is always the default; adding
`--remote <host>` is the only way to move the loop to a ShinyHub server.

<div class="shiny-dev-model" markdown>

<div markdown>

**One app · Local**

```bash
shinyhub dev .
```

Run the current app through a production-shaped local route.

</div>

<div markdown>

**Fleet · Local**

```bash
shinyhub dev .
```

At a fleet root, run every app with a watchable local source.

</div>

<div markdown>

**One app · Remote**

```bash
shinyhub dev . --remote dev
```

Attach the current app to an existing target on the named host.

</div>

<div markdown>

**Fleet · Remote**

```bash
shinyhub dev . --remote dev
```

Preflight every selected target before deploying the first change.

</div>

</div>

Use `--app <slug>` to narrow a fleet. Use `--create` or `--ephemeral --ttl 8h`
only when you deliberately want remote mode to create an app.

## The safe local loop

From a bundle containing `app.py`, `app.R`, or a `shinyhub.toml` command:

```bash
shinyhub doctor . --local
shinyhub dev . --open
```

[`doctor`](doctor.md) reports bundle, manifest, entrypoint, and runtime blockers
together without starting a process or contacting a server. `dev` then:

1. mirrors the source into an isolated generated workspace;
2. installs Python or R dependencies when needed;
3. starts the app behind ShinyHub's real local proxy;
4. waits for the declared readiness contract; and
5. watches for the next edit.

The source directory is treated as read-only. Generated `pyproject.toml`,
`uv.lock`, `.venv`, renv files, bytecode, and the `data` link stay outside the
checkout and can never leak into a deployment bundle. App data persists across
restarts in a separate directory.

Each edit starts as a candidate. The proxy switches only after that candidate
becomes healthy, so a syntax error, missing dependency, crash, or failed
readiness check leaves the last healthy version serving. Fix the file and the
next save retries automatically.

Open browser tabs refresh automatically after a successful local reload. Use
`shinyhub dev . --open` to open the first tab as soon as the app is ready; later
saves refresh that tab and any others viewing the app, preserving their URL.
Failed changes do not trigger a refresh. Refresh starts a new app session, so
unsaved form inputs and in-memory session state reset. This also works for each
app in a local fleet. Remote development does not inject browser refresh.

Browser refresh uses a small script in the app's HTML and a local revision
endpoint. Apps that prohibit same-origin connections in their Content Security
Policy, serve compressed HTML despite the proxy's request, or do not have an
injectable HTML shell still require manual refresh.

Requests use the production-shaped `/app/<slug>/` route with prefix stripping,
forwarding headers, WebSocket support, and cookie handling. The root URL
redirects to the app route.

## Terminal development view

On an interactive terminal, local `dev` opens a compact TUI automatically. It
shows the healthy version still serving, the latest save's progress or failure,
the app URL, and the last successful reload. The log selector includes every
app, **All apps**, and **ShinyHub**, even for a single-app session. Narrow
terminals show one view at a time.

- **↑ / ↓** selects an app or session log view.
- **a** jumps to **All apps**; **s** jumps to **ShinyHub**.
- **o** opens the selected app in your browser.
- **r** restarts the selected app through the readiness-checked loop. A healthy
  instance keeps serving until its replacement is ready, including when the
  source has not changed. Failed restarts preserve the healthy instance.
- **x** stops the selected app and any pending startup, including their process
  groups. Automatic reloads are suspended for that app; other apps keep running.
- **u** resumes a stopped app from the latest source. Its local URL and app data
  are preserved. A failed resume stays recoverable by fixing and saving or **r**.
- **m** toggles the resource inspector; **PgUp / PgDn** scrolls its details.
- **Tab** switches between runtime output and the latest change's logs.
- **PgUp / PgDn** scrolls logs; scrolling back pauses following.
- **f / End** resumes following; **Space** toggles following.
- **/** filters logs; **Esc** clears the filter; **?** shows extra shortcuts.
- **q / Ctrl-C** quits the session, stops all apps, and restores the terminal.

An intentionally stopped app stays stopped when files change; **r** does not
implicitly resume it. Its local URL returns HTTP 503 while it is not running.
The session retains its workspace and data locks until you quit. Restart and
resume do not rerun completed startup seed schedules. If an app crashes, the
TUI keeps the failure visible and its controls available for recovery.

A failed candidate stays visible while the healthy app keeps serving. Fix the
source and save to recover. Runtime and startup output are kept separate, and
log retention is bounded per app and per session view. Selecting a failed app
opens its latest change; selecting a healthy app opens its runtime output.

**All apps** merges runtime, startup, and lifecycle output in receipt order,
labelled with the app and attempt. **ShinyHub** shows workspace preparation,
dependency steps, readiness, reload failures, and session diagnostics without
app stdout/stderr. Both support the same filtering, scrolling, and following;
filters also match app names. Open, restart, stop, and resume apply when an app is
selected; aggregate views cannot control every app accidentally.

The terminal's own colors are used; `NO_COLOR` and `--no-color` disable color
without removing state labels.

The TUI samples local host CPU utilization, available RAM, and free space on the
source directory's filesystem every two seconds. A compact strip appears when
there is room; **m** opens the full resource inspector in any supported terminal
size. App CPU and RSS cover each app's process group, including children. The
serving attempt stays separate from a candidate starting during reload, and
exited processes are removed immediately. Dependency preparation processes are
not attributed to an app; their impact is included in host utilization.

Host CPU uses 100% for the whole machine; app CPU uses 100% for one busy core.
RSS counts shared pages in each process, so adding RSS values does not give
physical RAM consumption. A first CPU sample has no rate yet and displays `—`;
failed measurements also display unavailable rather than zero. Readings older
than six seconds are marked stale. Sampling runs outside the UI loop, preserves
log filtering and scroll position, and is disabled for plain and NDJSON output.

Redirecting any standard stream, running in CI, or using `TERM=dumb` retains
plain output. You can also choose a presentation explicitly:

```bash
shinyhub dev . --tui                  # require an interactive terminal
shinyhub dev . --tui=false            # stream plain logs
shinyhub dev . --output table         # stream plain logs
shinyhub dev . --output ndjson        # lifecycle and log events for agents
shinyhub dev . 2>&1 | tee dev.log      # record a plain development session
```

Local NDJSON includes `type`, `app`, `at`, and `attempt`. Phase events identify
`preparing`, `reloading`, `starting`, `ready`, `failed`, `superseded`, and
`stopped`. A `ready` event includes the public `url` and healthy `generation`;
failed attempts never advance the generation. Log events carry `source` (`app`
or `reload`), `stream` (`stdout` or `stderr`), and `message`. Each candidate has
its own attempt number so consumers can distinguish startup output from the
app currently serving. Process events (`type: "process"`) report `phase: "started"`
or `"exited"`, `pid`, and `attempt`, allowing consumers to track app process
lifetimes. Stop the process with SIGINT or SIGTERM when finished.
Remote development retains its existing streaming interface.

## Local data producers

Local development defaults to leaving schedules idle. Fleet-local settings can
opt into initialization before the first app boot. To execute a
manifest-defined job once, without a server or login:

```bash
shinyhub schedule run --local fetch .
shinyhub schedule run --local fetch -f fleet.toml --app sales
```

The job runs in the same generated workspace and durable data directory as
`dev`, including composed `[[bundle_file]]` inputs. Dependencies are prepared
first; output streams with the schedule name and failures preserve the job's
exit code. At a fleet root the named schedule runs sequentially for each
selected local-source app; `--app`, `--all`, and `--standalone` select the same
scope as `dev`. All selected apps’ manifests, schedule names, and environment
files are checked before the first producer runs. Explicit execution can run a
disabled schedule and reports that choice. Cron timing and server-side replica activation are not applied locally.

To run enabled schedules whose `deploy_trigger` is `first_deploy` or
`bundle_change` before starting the app, opt in on each invocation:

```bash
shinyhub dev . --seed
shinyhub run . --seed --check
```

Producers run in manifest order after dependency preparation and before the
initial app boot. They never run on reloads. Choose a policy explicitly:

- `--seed` or `--seed=always` runs every enabled deploy-trigger producer again.
- `--seed=missing` runs producers without a successful initialization record for
  this app, schedule, parsed command, and data-directory generation.
- `--seed=never` skips startup producers, overriding a fleet default.

Successful manual local schedule runs update the same records. Before a job
starts, ShinyHub durably marks the data as being written. A failed or interrupted
attempt invalidates previous initialization records, so a subsequent `missing`
start retries the producers. Success records live outside app data; a hidden
`.shinyhub-local-generation` marker in app data binds them to the current data
instance. Replacing the data directory or removing that marker requires new
initialization. If a later startup producer replaces data initialized by an
earlier producer, startup stops rather than serving an incomplete generation.
Keep producers scoped to their own output directories; a subsequent `missing`
run repairs the invalidated initialization. `--fresh` preserves data and its
initialization records.

These records track initialization, not freshness or compatibility with changed
code or credentials. Deleting individual output files while retaining the
marker cannot be detected. Use an explicit schedule run or `--seed=always` when
data needs refreshing; there is no parquet-specific or empty-directory check.
Disabled schedules and cron-only schedules are never startup producers.

A failed producer stops startup, but may have partially changed data. Fix and
rerun it before serving that data. Producer steps must finish in the foreground;
a leader exiting while background work keeps output pipes open fails the run
and terminates that background work.

Stop local sessions using the same data directory before running a job or
starting with `--seed`. Local readers and writers coordinate through a data
lock, including across different `--state-dir` values. This lock coordinates
ShinyHub local sessions only; external scripts do not participate. Locks live
in ShinyHub’s user cache outside app data, so clearing the data directory does
not remove its lock. Cleanup targets the launched process group; programs that
deliberately detach into another session are outside that guarantee.

Local jobs accept `--env`, `--env-file`, `--data-dir`, `--state-dir`, `--fresh`,
and `--no-sync`. By default they load the app directory's `.env`. Host cloud
credentials are not inherited automatically. Supply app-specific values
explicitly, for example `--env AWS_PROFILE=development`, or allow exact host
variable names:

```bash
SHINYHUB_APP_ENV_ALLOW=AWS_PROFILE,AWS_CONFIG_FILE shinyhub dev . --seed
```

Both app processes and producers use these explicit environment controls.

## Fleet-local defaults

Add local defaults once to `fleet.toml` (or the supported legacy filename
`shinyhub-fleet.toml`):

```toml
[dev]
seed = "missing"
env_allow = ["AWS_PROFILE", "AWS_CONFIG_FILE"]
env = { IS_LOCAL = "1" }
```

Then develop an app from either the fleet root or its declared source directory:

```bash
shinyhub dev . --app alpha-user-dashboard --open
shinyhub dev ./alpha_user_dashboard --open
```

Both paths select the manifest slug, bundle inputs, durable data directory, and
local defaults. Discovery prefers `fleet.toml` when both filenames exist. The
startup summary shows the selected app, manifest, data path, and seed policy;
producer output explains runs and skips.

Only the exact host variable names in `env_allow` are inherited. They form the
lowest local environment layer, followed by `[dev].env`, the app's `.env` (or
an explicit `--env-file` replacing it), and CLI `--env` overrides. No global
allow-list is changed, and values are not printed in diagnostics.

Defaults apply to fleet-aware local `dev`, `fleet dev`, and
`schedule run --local`. They are validated with the fleet manifest but never
applied to deployments or remote development. Plain `run` and `dev --standalone`
keep standalone behavior and do not inherit fleet defaults. CLI `--seed=never`
or `--seed=always` overrides `[dev].seed`.

Every local app and schedule receives reserved `SHINYHUB_RUN_MODE=local`, so
apps can detect local execution without inventing a flag. This is an execution
mode indicator, not identity or authentication. `PORT`, `SHINYHUB_APP_DATA`,
`SHINYHUB_APP_SLUG`, and `SHINYHUB_RUN_MODE` cannot be overridden through local
environment settings.

## Common options

```bash
shinyhub dev . --open                 # open after the app is healthy
shinyhub dev . --fresh                # rebuild generated state; keep app data
shinyhub dev . --no-sync              # skip explicit uv/renv preparation
shinyhub dev . --port 8000            # choose the public proxy port
shinyhub dev . --slug sales           # serve at /app/sales/
shinyhub dev . --data-dir ../dev-data # choose durable app-data storage
shinyhub dev . --state-dir /tmp/sales # choose generated workspace state
```

Environment values come from `.env` by default and may be overridden with
repeatable `--env KEY=VALUE` flags. Values are never printed; diagnostics list
keys only. `PORT`, `SHINYHUB_APP_DATA`, and `SHINYHUB_APP_SLUG`, and `SHINYHUB_RUN_MODE` are managed by
ShinyHub and cannot be overridden.

Use `[app] readiness_path` when `/` is not a meaningful health endpoint. By
default, any 2xx or 3xx response is healthy; add `readiness_status` to require
one exact status:

```toml
[app]
readiness_path = "/health/ready"
readiness_status = 204
startup_timeout_seconds = 180
```

Only one runner uses a cached workspace at a time. Give concurrent copies of
the same source distinct `--state-dir` values.

## Choose the scope

- [Fleet development](development/fleet.md) explains automatic manifest
  discovery, shared inputs, multi-app output, and selection.
- [Remote development](development/remote.md) explains connection, creation,
  safety, deployment history, logs, and traces.

`shinyhub run` remains the lower-level standalone command for one-shot checks
and specialist controls:

```bash
shinyhub run . --check       # boot smoke test, then exit
shinyhub run . --no-reload   # run once without watching files
```

`--check` also fails, before booting, on a `uv.lock` that no longer records
what `pyproject.toml` declares, which a deploy would refuse (see
[shipped `uv.lock` files](environment.md#shipped-uvlock-files)).

New interactive workflows should use `shinyhub dev` so every app shares one
mental model. To work on the ShinyHub platform rather than an application, use
the separate [contributor development setup](contributing/development.md).
