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
    --cmd "python helpers/fetch.py" \
    --timezone "Europe/Amsterdam" \
    --timeout 600 \
    --overlap skip \
    --missed run_once
```

Fields:

| Field | Meaning |
|---|---|
| `cron` | 5-field standard cron expression. The `timezone` field controls which timezone the expression fires in. Do not embed `TZ=` or `CRON_TZ=` prefixes directly in the expression. |
| `timezone` | Optional IANA timezone for this schedule (e.g. `Europe/Amsterdam`, `America/New_York`). When absent or empty the schedule inherits the server default (`scheduler.timezone` config, default UTC). See "Timezone resolution" below. |
| `cmd` | Command to run inside the bundle dir. Shell-quoted; use `--cmd-json` for exact control. |
| `timeout` | Seconds before SIGTERM; SIGKILL after a 10-second grace. |
| `overlap` | `skip` (default) drops new ticks while one is in flight; `queue` holds at most one extra; `concurrent` allows overlap. |
| `missed` | `skip` (default) ignores ticks missed during downtime; `run_once` dispatches one catch-up at startup. |
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
cmd = "python helpers/fetch.py"
timeout_seconds = 600
overlap = "skip"
missed = "run_once"
```

## Triggering manually

```bash
shinyhub schedule run fetch daily-fetch --follow
```

`--follow` tails the run's log until exit.

## Warm on first deploy (`run_on_register`)

A fresh deploy leaves an app's cache empty until the schedule first fires, which
may be hours away (`0 5 * * *`). `run_on_register` closes that gap: when a
schedule is registered for the first time on an app that has never had a
successful run of it, the platform fires it once so the cache is warm by the
time the first user arrives.

```bash
shinyhub schedule add fetch --name daily-fetch \
    --cron "0 6 * * *" --cmd "python helpers/fetch.py" \
    --run-on-register
```

Or in a `shinyhub.toml` manifest:

```toml
[[schedule]]
name = "daily-fetch"
cron = "0 6 * * *"
cmd = "python helpers/fetch.py"
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
  not a failure.

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
- Schedule `daily-fetch` with `cron: "0 6 * * *"`, `cmd: "python helpers/fetch.py"`

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
