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
| `deploy_trigger` | `never` (default), `first_deploy`, or `bundle_change`. The last mode requires the authoritative last writer to match the current bundle digest and canonical command. CLI flag: `--deploy-trigger`. |
| `on_success` | `none` (default) leaves serving processes unchanged. `roll` durably and gracefully replaces this app's replicas after a successful run so module-scope data is re-imported. |
| `min_roll_interval` | Optional damper such as `1h`. Successful runs still advance job history and the target data generation, but queued activation is coalesced and cannot run before this interval after the last completed successful roll. CLI flag: `--min-roll-interval`. |
| `roll_fallback` | Behavior when the temporary surge cannot be admitted: `defer` (default) keeps serving the old generation and retries, while `restart` stops the old pool first and accepts an availability gap to activate fresh data. CLI flag: `--roll-fallback`. |
| `max_defer_age` | Optional capacity-deferral deadline such as `6h`. At expiry the activation fails visibly instead of retrying forever. `0` (default) keeps the existing unlimited behavior. CLI flag: `--max-defer-age`. |

> **Breaking upgrade:** `run_on_register` has been removed. Replace it with
> `deploy_trigger = "bundle_change"` for a cache producer, or
> `deploy_trigger = "first_deploy"` only for a genuine lifetime bootstrap. The
> old manifest-only flag was not persisted, so schedules already in an upgraded
> database default to `never` until their policy is explicitly reapplied.

> **Producer runtime:** schedules that declare producer semantics
> (`deploy_trigger != "never"` or `on_success = "roll"`) currently require the
> native runtime **and effective `worker_isolation = "multiplex"`**. Native
> multiplex children have durable replica identities and inherit the server's physical publication
> fence. Docker, remote-worker, Fargate, and ECS launches have a
> request-accepted-before-handle-persisted window that cannot be fenced safely
> after control-plane failure; elastic `grouped`/`per_session` workers likewise
> lack durable failover identity. These topologies are rejected at write time
> and revalidated when the server starts and when a producer runs, including
> apps that inherit a changed fleet isolation default.

> Once an elastic worker launch has been attempted, the app carries a durable
> orphan-risk marker. Moving it into producer-capable multiplex mode requires
> stopping the app and explicitly setting `worker_isolation = "multiplex"`;
> the server holds the app operation lock and proves the entire native consumer
> process tree absent before it clears that marker.

> **Unclean 0.12.x upgrade:** upgrade only after scheduled runs have drained.
> If migration finds a run that was still `running`, startup fails closed because
> the old process has no durable PID and did not inherit the new publication
> flock. Stop every old server, reboot the host (or otherwise verify every
> listed legacy job descendant is gone), then run
> `shinyhub resolve-legacy-schedule-writers
> --acknowledge-processes-stopped`. The offline command lists the exact affected
> apps, schedules, and runs, then atomically records conservative producer
> uncertainty before clearing the startup fence. Do not edit the marker table
> directly. The migration also installs a database admission guard so a live
> old server cannot launch another unfenced job during a rolling handoff.

If any designated writer fails or is interrupted after it may have touched app
data, the app is compatibility-quarantined and fresh consumer starts fail
closed. `schedule ls` and fleet schedule status expose
`producer_repair_required`; `apps show` exposes both
`compatibility_quarantined` and `producer_repair_required`. Repair requires a
successful rerun of every flagged producer (including an `on_success = "roll"`
writer whose `deploy_trigger` is `never`) or a deployment that supplies and
successfully runs a replacement producer policy. Merely deploying unrelated
code or rolling back does not erase an uncertain physical write.

For legacy-upgrade uncertainty, the original schedule may have
`deploy_trigger = "never"` and `on_success = "none"`. Run it explicitly with
`shinyhub schedule run APP SCHEDULE --follow`: while
`producer_repair_required` is set, that exact manual execution is promoted to a
fenced publisher regardless of its declaration, and only a successful exit
clears the uncertainty. This also works while the schedule is disabled.

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
on_success = "roll"
min_roll_interval = "1h"
roll_fallback = "restart"
max_defer_age = "6h"
```

## Activating refreshed data in serving replicas

Writing a new file does not reload a process that read it at import time. Set
`on_success = "roll"` when a successful schedule run must also make that data
visible to new sessions without app-side reload code:

```toml
[[schedule]]
name = "refresh-pend-data"
cron = "*/15 * * * *"
cmd = "uv run python helpers/fetch_data.py --only pend_data"
on_success = "roll"
min_roll_interval = "1h"
roll_fallback = "restart"
max_defer_age = "6h"
```

The schedule run and serving-data activation are deliberately separate
outcomes. A successful command commits its run and activation request in one
database transaction. The activation then starts one fresh surge replica,
waits for its health check, drains and replaces canonical slots one at a time,
invalidates suspended snapshots, and removes the surge. Existing sessions are
allowed to drain; once activation succeeds, every routable or resumable replica
belongs to the run's target generation.

Important behavior:

- Only `succeeded` runs activate. Failed, timed-out, cancelled, interrupted, or
  overlap-skipped runs leave serving processes unchanged.
- The configured replica count and `runtime.max_replicas` are steady-state
  limits. A roll admits exactly one temporary, memory-checked surge
  (`max_surge = 1`) above that count. If safe headroom cannot be proved,
  `roll_fallback = "defer"` records `deferred_capacity` and retries without
  dropping the warm floor; `roll_fallback = "restart"` instead stops the old
  pool before starting the target generation.
- During every cold boot ShinyHub samples full process-group RSS from runtime
  start through the successful readiness boundary and persists that startup
  high-water mark on the replica. Surge admission uses a configured memory
  limit when one exists; otherwise it uses the larger of the persisted startup
  peak and current live RSS, then retains a 25% + 64 MiB safety margin. Failed
  startups do not replace the stored peak, and status-only lifecycle changes do
  not erase it. The replica API/metrics field is `startup_peak_rss_bytes`, and
  capacity errors and feasibility advisories identify the estimate source.
- Capacity deferrals back off at 1, 2, 4, 8, then 15-minute intervals. The
  persisted `capacity_deferrals` count is separate from rollout `attempts`.
  With `max_defer_age` set, the deadline starts at the first capacity defer,
  the next check is clamped to it, and the activation then fails with an
  explicit expiry reason.
- Activations are globally serialized in the first release, so schedules that
  complete together do not start simultaneous cold imports on one host.
- A newer queued success supersedes older queued work for the same app while
  preserving the earliest eligible time. Work already repairing after a
  destructive boundary cannot be superseded.
- The app's replica topology and lifecycle settings are fenced while an
  activation is running or repairing. Scale, restart, deploy, rollback, stop,
  and delete requests return a conflict rather than erasing recovery identity.
- Disabling `on_success` affects future runs only. The UI continues to show a
  durable activation already pending, running, or repairing, and the schedule
  cannot be deleted until that work becomes terminal.
- `shinyhub schedule cancel <app> <schedule>` clears the newest queued or
  capacity/interval-deferred activation without disabling the schedule.
  Cancellation is refused once an activation is running or repairing because
  it may already own live runtime state. A successful cancellation is retained
  as the terminal `cancelled` activation state; it is not reported as a failure.
- Schedule create/update responses, deploy results, the dashboard, and
  `shinyhub doctor --remote --slug <app>` surface a roll-feasibility advisory
  when the current surge estimate plus host floor exceeds available memory.
  This is a warning rather than a rejection because host capacity can change
  before the next run; the text names whether the configured outcome is a
  capacity defer or an in-place restart availability gap.
- The first release supports self-rolls for multiplex apps on the native
  runtime. Grouped/per-session isolation, container, remote, or managed
  runtimes, cross-app consumers, and in-process `signal` hooks are rejected or
  left for a separately specified expansion.

Use the UI's independent **Job** and **Serving-data activation** statuses,
`shinyhub apps show <app>` for an attributable per-app summary, or
`shinyhub schedule runs <app> <schedule>` for full history. Fleet APIs also
expose activation status, phase, age, target generation, attention, and serving
freshness. Logs and audit events carry the activation id, source run, and target
generation for incident attribution.

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
APP    SCHEDULE      LAST RUN   LAST SUCCESS          AGE  STALE  REFRESHING
fetch  daily-fetch   running    2026-08-18T06:00:12Z  30h  yes    yes
```

**Per app** (anyone with access to that app):

```bash
shinyhub schedule ls fetch
```

`schedule ls` carries the same freshness fields per schedule -
`last_run_id`, `last_run_at`, `last_run_status`, `last_success_at`,
`last_success_age_s`, `stale`, `refreshing`, and the latest durable activation.
The fleet status endpoint additionally reports `activation_status`,
`activation_phase`, `activation_age_s`, `activation_due_at`,
`activation_target_generation`, `activation_error`, `activation_attention`,
and `serving_freshness`. The latest run's id, status, and timestamp come from
one database snapshot, so a diagnostic can fetch the log for exactly the run
whose state it reports.

`stale` is cron-aware rather than a fixed threshold. It takes the last
**success** as the anchor (the schedule's creation time if it has never
succeeded), asks the schedule's own cron expression for the next fire after
that anchor in its effective timezone, and reports stale once that time is more
than 10 minutes in the past. So a schedule that runs and fails every night is
stale: a failure does not advance the data.

Two deliberate properties:

- A **disabled** schedule is never stale. It is not supposed to be firing.
- `stale` describes the age of proven data, while `refreshing` describes a live
  run that is still within its timeout. They are independent: overdue data can
  be `stale: true, refreshing: true` until the active run succeeds. A run past
  its timeout is no longer refreshing.

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
| `deploy` | A run admitted by `deploy_trigger`, including restart recovery for an interrupted run that still applies to the current bundle. |
| `manual` | Started by an operator via the CLI, UI, or `POST /api/schedules/{id}/run`. |

`missed` distinguishes a backfill from an on-time fire, so "did the catch-up
policy cover last night's outage?" is answerable from run history alone.

> **Compatibility:** catch-up runs previously recorded `trigger: "schedule"`,
> indistinguishable from an on-time fire. Tooling that filters run history on
> `trigger == "schedule"` to mean "any automatic run" must match `missed` too.
> Runs recorded before the upgrade keep their original `schedule` value.

## Deploy-triggered data convergence

A schedule command is a pointer into the deployed bundle. `deploy_trigger =
"bundle_change"` relates its output to that bundle, so changing only the
producer script still requires a new successful run.

```bash
shinyhub schedule add fetch --name daily-fetch \
    --cron "0 6 * * *" --cmd "uv run --with-requirements requirements.txt python helpers/fetch.py" \
    --deploy-trigger bundle_change
```

Or in a `shinyhub.toml` manifest:

```toml
[[schedule]]
name = "daily-fetch"
cron = "0 6 * * *"
cmd = "uv run --with-requirements requirements.txt python helpers/fetch.py"
deploy_trigger = "bundle_change"
```

Semantics:

- **Policy.** `never` disables deploy dispatch. `first_deploy` is a durable
  one-time bootstrap. `bundle_change` is satisfied only when the authoritative
  last writer matches both the current content digest and the canonical
  producer command. Historical successes are not a cache: after another
  producer writes, a rollback must produce again.
- **Pre-start convergence.** A deploy or rollback prepares the candidate
  environment, confirms every previous consumer has physically stopped, runs every unsatisfied producer
  synchronously against the exact candidate bundle, and only then starts the
  candidate consumers. With `bundle_change`, new code is therefore never
  started against data whose authoritative producer belongs to another bundle;
  `first_deploy` intentionally accepts its lifetime bootstrap publication. If a producer begins and
  the deploy later fails, ShinyHub leaves the app stopped instead of reviving an
  older consumer against data that may already have changed format.
- **Durable desired state.** Each promoted deployment records one convergence
  obligation for every persisted enabled policy, including API-created
  schedules and schedules retained when absent from a later manifest. Pending
  admission and interrupted server work repair automatically. A producer
  command that runs and fails is left failed for inspection; retry an enabled
  producer with `shinyhub schedule retry-convergence APP SCHEDULE`. A disabled
  producer must be enabled first or repaired deliberately with a manual run.
- **Exact execution provenance.** Admission snapshots `deployment_id`,
  `app_version`, and `content_digest`. Queued work executes that immutable
  bundle instead of whichever deployment happens to be newest later.
- **Fail-closed dispatch.** A required deploy run that cannot be admitted makes
  the deploy request fail visibly and remains durably pending for repair. A
  disabled schedule is never dispatched. Active obligation bundles are pinned
  against deployment cleanup until their work is terminal.
- **Opt-in wait.** `--wait-for-warm` asks the server to reconcile persisted
  policy after every fleet action, including unchanged apps, then joins the
  exact obligation run and rechecks authoritative state. Required convergence
  serializes behind ordinary work instead of accepting `overlap = "skip"` as
  proof. An older run that finishes last becomes the authoritative writer and
  automatically reopens the current obligation.
- **Startup-loaded caches.** Deploy and rollback convergence happens before
  replica boot, so startup-loaded caches see the candidate producer's output on
  their first read. `--restart-after-warm` remains useful when an unchanged-app
  reconciliation or a direct schedule-policy edit repairs convergence after
  replicas were already running; it is not needed for a producer marked
  `prestart` in the deploy response.
- **Rollback declarations.** Every promoted deployment retains an immutable
  snapshot of its complete effective schedule declaration set. Rollback
  restores that snapshot atomically, runs its producer commands against the
  matching historical bundle, and only then starts consumers. A deployment
  created before snapshot support cannot be a rollback target until that
  version is redeployed, so the server never guesses across code generations.
- **Activation fence.** If a newer bundle becomes current after a successful
  producer run but before its `on_success = "roll"` activation, that activation
  finishes as `superseded` rather than rolling newer code onto older output.
- **Visible data provenance.** `apps show` places each activated replica's
  `data_generation` beside the exact producer deployment, app version, and
  content digest; the JSON surface also carries the producer-command
  fingerprint. Schedule status exposes the same authoritative last writer, so
  code/data compatibility is directly observable rather than inferred from
  timestamps.

The server treats a successful process exit as an atomic publication signal;
it cannot make arbitrary filesystem writes transactional. A producer used for
deployment convergence must therefore be idempotent, build into a temporary or
generation-specific location, and atomically rename or switch the completed
output into place immediately before exiting successfully. This also makes the
recorded completion order faithfully represent writer order.

For a read-only fleet audit, pass `shinyhub fleet apply --verify-schedules`.
It checks every enabled schedule against the server-computed `stale` boolean
(one cron interval plus the server's grace policy), rejects any enabled
deploy-trigger whose authoritative producer does not match current code, and
rejects unresolved producer-write uncertainty even when that writer is now
disabled. It never dispatches work; use `--wait-for-warm` when the apply should reconcile and
wait. If a stale schedule is actively
running, the report says `stale · refreshing`; activity does not substitute for
a successful data refresh. If the stored cron or timezone cannot be evaluated,
freshness is `unknown` and strict gates fail closed; the fleet-health banner is
also `unknown`, never healthy, until the observation is complete.

Failed fleet gates have stable JSON `failure_kind` values such as
`warm_wait_timeout`, `warm_bundle_not_ready`, and `schedule_stale`. When the
latest atomic state identifies a failed run, `fleet apply` includes the last 25
non-empty schedule-log lines and prints the exact follow-up command:

```bash
shinyhub schedule logs <app> <schedule> --run <run-id>
```

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
- **Native runtime read-only enforcement.** RO is a convention for native (filesystem permits writes through the symlink). Producer semantics require native execution; use atomic replacement and appropriate filesystem permissions for the data contract.
- **Activation scope.** `on_success = "roll"` is limited to self-rolls for multiplex apps on the native runtime. Unsupported topology is rejected when the schedule or app placement is written; `roll_fallback` applies only to a supported roll that fails capacity admission.

## Audit log

Every schedule action is recorded in the audit log under one of:

```
schedule_create  schedule_update  schedule_delete  schedule_run_manual
schedule_run_succeeded  schedule_run_failed
schedule_run_timed_out  schedule_run_cancelled  schedule_run_interrupted
schedule_activation_roll  schedule_activation_restart
schedule_activation_cancel  schedule_activation_outcome
shared_data_grant  shared_data_revoke
```

Create and update audit details include `on_success` and
`min_roll_interval_seconds`, `roll_fallback`, and `max_defer_age_seconds`;
enable/disable is recorded as `schedule_update`.

`schedule_activation_outcome` records the activation ID, source run, schedule
snapshot, target generation, terminal status, last operational phase, and error.
Admins can expand those details in **Audit Log**, or filter the API with
`GET /api/audit?action=schedule_activation_outcome`.
