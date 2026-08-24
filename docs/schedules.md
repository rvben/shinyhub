---
description: "Run cron-style jobs against an app's bundle as short-lived processes in the same runtime, with the output of every run captured."
---

# Scheduled Jobs

ShinyHub can run cron-style jobs against an app's bundle. Each run is a
short-lived process spawned in the same runtime (native or Docker), with the
same env vars (incl. encrypted secrets), the same `app-data` directory, and
the same resource limits as the serving app — but **independent of whether
the app is running, hibernated, or degraded**.

Scheduled runs do **not** wake a hibernated app. The run produces output to
disk; the app reads it next time it serves traffic.

## Defining a schedule

Via the UI: **Settings ⚙ → Schedules → + Add schedule**.

Via the CLI:

```bash
shinyhub schedule add fetch \
    --name daily-fetch \
    --cron "0 6 * * *" \
    --cmd "uv run --with-requirements requirements.txt python helpers/fetch.py" \
    --timezone "Europe/Amsterdam" \
    --timeout 600 \
    --overlap skip \
    --missed run_once
```

> **Running Python on the native runtime:** the command runs inside the bundle
> dir using the app's runtime. On the native runtime a Python app's interpreter
> and dependencies are managed by `uv`, so run scripts through
> `uv run --with-requirements requirements.txt python <script>` (the same way
> the app itself is launched). A bare `python <script>` can fail with
> `executable file not found` because `python` is not on `PATH`. The Docker
> runtime runs the command inside the app image, where `python` is present.

Fields:

| Field | Meaning |
|---|---|
| `cron` | 5-field standard cron expression. The `timezone` field controls which timezone the expression fires in. Do not embed `TZ=` or `CRON_TZ=` prefixes directly in the expression. |
| `timezone` | Optional IANA timezone for this schedule (e.g. `Europe/Amsterdam`, `America/New_York`). When absent or empty the schedule inherits the server default (`scheduler.timezone` config, default UTC). See "Timezone resolution" below. |
| `cmd` | Command to run inside the bundle dir. Shell-quoted; use `--cmd-json` for exact control. For a Python app on the native runtime, prefix with `uv run` (see the note above). |
| `timeout` | Seconds before SIGTERM; SIGKILL after a 10-second grace. |
| `overlap` | `skip` (default) drops new ticks while one is in flight; `queue` holds at most one extra; `concurrent` allows overlap. |
| `missed` | `skip` (default) ignores ticks missed during downtime; `run_once` dispatches one catch-up at startup, recorded with `trigger: "missed"` (see "Run provenance" below). |
| `run_on_register` | When `true`, fire this schedule once on first registration if the app has never had a successful run of it, warming the cache on a fresh deploy. CLI flag: `--run-on-register`. See "Warm on first deploy" below. |

**Timezone PATCH tri-state:** The `timezone` key in a PATCH request has three distinct meanings:

| Value | Effect |
|---|---|
| key absent | timezone is left unchanged |
| `"timezone": null` | timezone cleared; schedule inherits server default |
| `"timezone": ""` | timezone cleared; schedule inherits server default |
| `"timezone": "America/New_York"` | timezone set to that IANA zone (validated) |

## Timezone resolution

ShinyHub evaluates cron expressions in the schedule's **effective timezone**, determined by this resolution chain (first match wins):

1. The schedule's own `timezone` field (non-empty IANA name).
2. The server-level `scheduler.timezone` config key (or `SHINYHUB_SCHEDULER_TIMEZONE` env var).
3. UTC (always the final fallback — the server never reads the host's `TZ`/`time.Local`).

The effective timezone is shown in the UI and API responses as `effective_timezone`. When it comes from the server default, `timezone_inherited` is `true`.

**DST behaviour** (matches robfig/cron semantics):

- **Spring-forward gap** (e.g. Europe/Amsterdam 2:00 → 3:00): a cron expression that targets a non-existent local time (e.g. `30 2 * * *` on the clock-change Sunday) fires zero times that day — the non-existent local time is skipped. The next fire is the matching time the following day.
- **Fall-back overlap** (e.g. 3:00 → 2:00): a cron expression targeting a repeated local wall-clock time fires **twice** on the fall-back day, once per UTC instant. For example, `30 2 * * *` in Europe/Amsterdam fires at 00:30 UTC (02:30 CEST, before the clock change) and again at 01:30 UTC (02:30 CET, after the clock change).

Configure the server default in `shinyhub.yaml`:

```yaml
scheduler:
  timezone: "Europe/Amsterdam"   # default UTC when absent
```

Or via environment variable:

```bash
SHINYHUB_SCHEDULER_TIMEZONE=Europe/Amsterdam
```

An invalid IANA zone in either location is a fatal configuration error at startup.

In a `shinyhub.toml` manifest:

```toml
[[schedule]]
name = "daily-fetch"
cron = "0 6 * * *"
timezone = "Europe/Amsterdam"
cmd = "uv run --with-requirements requirements.txt python helpers/fetch.py"
timeout_seconds = 600
overlap = "skip"
missed = "run_once"
```

## Triggering manually

```bash
shinyhub schedule run fetch daily-fetch --follow
```

`--follow` tails the run's log until exit.

## Monitoring data freshness

A schedule that stops firing is silent by nature: the app keeps serving, just
from data that stops advancing. Two surfaces report it, both using the same
server-side staleness rule, so a dashboard never has to reimplement cron
arithmetic client-side.

**Across the fleet** (admin or operator):

```bash
shinyhub schedule status              # every app
shinyhub schedule status fetch        # one app
```

```
APP    SCHEDULE      LAST RUN   LAST SUCCESS          AGE  STALE
fetch  daily-fetch   succeeded  2026-08-18T06:00:12Z  30h  yes
```

**Per app** (anyone with access to that app):

```bash
shinyhub schedule ls fetch
```

`schedule ls` carries the same freshness fields per schedule -
`last_run_at`, `last_run_status`, `last_success_at`, `last_success_age_s`
and `stale`.

`stale` is cron-aware rather than a fixed threshold. It takes the last
**success** as the anchor (the schedule's creation time if it has never
succeeded), asks the schedule's own cron expression for the next fire after
that anchor in its effective timezone, and reports stale once that time is more
than 10 minutes in the past. So a schedule that runs and fails every night is
stale: a failure does not advance the data.

Two deliberate exceptions:

- A **disabled** schedule is never stale. It is not supposed to be firing.
- A run still **in progress** is not stale until it exceeds the schedule's
  `timeout`. Past that it is treated as a zombie and staleness applies again.
  The timeout is not otherwise added to the grace period, so a daily schedule
  with a 24h timeout still flags at ~24h, not 48h.

`last_success_at` / `last_success_age_s` are absent when a schedule has never
succeeded (`schedule status` reports them as `null`; `schedule ls` omits the
keys). Neither is ever reported as `0`, so "never succeeded" cannot be read as
"succeeded just now".

## Run provenance (`trigger`)

Every run records **why** it started in its `trigger` field, visible in
`shinyhub schedule runs <app> <name>` and in the API run history:

| `trigger` | Meaning |
|---|---|
| `schedule` | A cron boundary arrived while the server was running. The normal case. |
| `missed` | A catch-up dispatched at startup by `missed = "run_once"`, because one or more boundaries passed while the server was down. Corresponds to no cron boundary. |
| `register` | A first-fire from `run_on_register`, including the startup reconcile that retries a first-fire interrupted by a restart. |
| `manual` | Started by an operator via the CLI, UI, or `POST /api/schedules/{id}/run`. |

`missed` distinguishes a backfill from an on-time fire, so "did the catch-up
policy cover last night's outage?" is answerable from run history alone.

> **Compatibility:** catch-up runs previously recorded `trigger: "schedule"`,
> indistinguishable from an on-time fire. Tooling that filters run history on
> `trigger == "schedule"` to mean "any automatic run" must match `missed` too.
> Runs recorded before the upgrade keep their original `schedule` value.

## Warm on first deploy (`run_on_register`)

A fresh deploy leaves an app's cache empty until the schedule first fires, which
may be hours away (`0 5 * * *`). `run_on_register` closes that gap: when a
schedule is registered for the first time on an app that has never had a
successful run of it, the platform fires it once so the cache is warm by the
time the first user arrives.

```bash
shinyhub schedule add fetch --name daily-fetch \
    --cron "0 6 * * *" --cmd "uv run --with-requirements requirements.txt python helpers/fetch.py" \
    --run-on-register
```

Or in a `shinyhub.toml` manifest:

```toml
[[schedule]]
name = "daily-fetch"
cron = "0 6 * * *"
cmd = "uv run --with-requirements requirements.txt python helpers/fetch.py"
run_on_register = true
```

Semantics:

- **Gate: never succeeded.** A brand-new schedule fires once; a schedule that
  has already succeeded is never re-fired by a re-deploy. A failed first-fire is
  non-fatal and is re-attempted on the next deploy until a run succeeds. A
  `disabled` schedule is never first-fired.
- **Async by default.** The deploy returns immediately and the run warms the
  cache in the background (a `register`-triggered run, visible in the run
  history).
- **Opt-in wait.** Pass `--wait-for-warm` to `shinyhub deploy` or
  `shinyhub fleet apply` to block until the run finishes (within the deploy's
  wait timeout). A genuine warm failure then exits non-zero; a `skipped_overlap`
  (another run is already warming the schedule) is reported as "in progress",
  not a failure. On `fleet apply`, this is a convergence level: an unchanged
  app whose enabled `run_on_register` schedule has never succeeded also exits
  non-zero. This verification reads schedule state but does not re-fire the
  schedule; a prior success keeps the unchanged path free of schedule work.
- **Startup-loaded caches.** Waiting alone does not reload a process that read
  the empty cache before the schedule ran. Pass `--restart-after-warm` to wait
  for every first-fire to succeed and then cycle serving replicas. The flag
  does not start an app that was deliberately stopped.

For fleet-wide freshness beyond first-fire warm-up, pass
`shinyhub fleet apply --verify-schedules`. This checks every enabled schedule,
including schedules without `run_on_register`, against the server-computed
`stale` boolean (one cron interval plus the server's grace policy). It is a
read-only gate: stale schedules fail convergence, but the apply does not trigger
them. Combine it with `--wait-for-warm` when both first-deploy warm-up and
ongoing schedule freshness are required.

## Sharing data between apps

Apps frequently fall into two roles: a **fetcher** that warms data, and one
or more **consumers** that render dashboards. Mount the fetcher's data dir
read-only into each consumer:

```bash
shinyhub share add report --from fetch
```

The consumer now sees `data/shared/fetch/` as a read-only directory inside
its bundle (the same path in both runtimes — Docker enforces RO; native is
RO by convention).

## Worked example: parquet warm + dashboard

`fetch` (the producer):

- `app.py` — minimal Shiny app that just shows the latest fetch time
- `helpers/fetch.py` — runs an Athena query and writes to `data/latest.parquet` atomically
- Schedule `daily-fetch` with `cron: "0 6 * * *"`, `cmd: "uv run --with-requirements requirements.txt python helpers/fetch.py"`

`report` (the consumer):

- Mount: `shinyhub share add report --from fetch`
- In `app.py`: `pd.read_parquet("data/shared/fetch/latest.parquet")`

The consumer reads stale data while the next fetch runs; on success the
fetcher atomically replaces the parquet (`os.rename`), so the consumer
either sees the old file or the new one — never a partial write.

## Limits + caveats

- **Single-instance only.** Running two ShinyHub processes against the same DB will double-fire schedules.
- **No per-schedule env or resource overrides.** Schedules inherit from the app.
- **Timezone.** Each schedule fires in its effective timezone (see "Timezone resolution" above). Schedules without an explicit timezone inherit the server default; the fallback is always UTC, never the host `TZ`. Server-default changes take effect on restart — running schedules are not hot-reloaded on config change.
- **`run_once` catch-up runs at startup only.** It does not re-fire missed runs from arbitrary points in time.
- **Native runtime read-only enforcement.** RO is a convention for native (filesystem permits writes through the symlink). Use Docker if you need OS-level enforcement.

## Audit log

Every schedule action is recorded in the audit log under one of:

```
schedule_create  schedule_update  schedule_delete  schedule_run_manual
schedule_run_succeeded  schedule_run_failed
schedule_run_timed_out  schedule_run_cancelled
shared_data_grant  shared_data_revoke
```

Enable/disable is recorded as `schedule_update`.

Admins can view via **Audit Log** in the UI or `GET /api/audit?action=schedule_run_failed`.
