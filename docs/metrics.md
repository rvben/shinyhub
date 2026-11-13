---
description: "Prometheus metrics for the control plane, a structured access log for every request, and how both line up with trace identifiers."
---

# Metrics and Observability

ShinyHub exposes Prometheus metrics for the server process itself (the control
plane), emits a structured access log for every request, and - when tracing is
enabled - records control-plane spans correlated with that access log. This is
separate from the per-app CPU/RAM sampling shown in the dashboard and from the
per-app proxy trace buffer documented in [tracing.md](tracing.md). Durable,
human-facing app-open reporting is also separate; see
[Usage analytics](usage-analytics.md).

Prometheus is an optional, one-way export. ShinyHub never queries Prometheus to
populate its API, dashboard, autoscaling decisions, or durable usage reports.
Historical peak concurrency in the Usage tab is calculated and retained by
ShinyHub itself.

## Memory measurement

The live app metrics API always reports RSS for a PID-backed replica. Native
Linux replicas also report three nullable attribution counters read from
`/proc/<pid>/smaps_rollup` and summed across the replica's process group:

| Field | Meaning | Operator use |
|---|---|---|
| `rss_bytes` | Resident pages mapped by the process; shared pages appear in every sharer's RSS. | Working-set signal and continuity with existing dashboards. |
| `pss_bytes` | Resident shared pages divided proportionally among their current sharers. | Additive physical-memory attribution and pre-fork evaluation. |
| `uss_bytes` | Private clean, dirty, and private huge pages. | Memory that should be reclaimed when the replica exits. |
| `swap_pss_bytes` | Swapped pages divided proportionally among sharers. | Detect attributed swap use without double-counting. |

The fields are `null` on unsupported hosts and remote/PID-less backends. If a
process exits or its rollup cannot be read while the process group is sampled,
`memory_attribution_partial` is true and the non-null counters are lower bounds.
`shinyhub top --json` sums the attribution fields across running replicas and
preserves the same `*_partial` distinction. The dashboard keeps RSS labelled as
Memory and shows PSS separately; the CLI replica inspector exposes all four.

PSS is for attribution, not enforcement or admission. Per-replica limits remain
cgroup/container limits, and the elastic-worker safety floor remains host
`MemAvailable`. A PSS value can move merely because another sharer starts or
stops, so using it as a hard cap would make the cap non-local and unstable.

## Native workload metrics over OTLP

ShinyHub can export resource usage for each native replica and scheduled command
without instrumenting the application. This export is opt-in and independent of
the Prometheus listener and dashboard history:

```yaml
tracing:
  enabled: true
  otlp_endpoint: http://collector:4318
  otlp_protocol: http/protobuf  # or grpc
  resource_attributes:
    deployment.environment.name: production
metrics:
  process_interval: 30s         # default 0: disabled; allowed range 1s–10m
```

`SHINYHUB_METRICS_PROCESS_INTERVAL` overrides the interval. `0` disables it.
Enabling export requires tracing and its OTLP endpoint; an endpoint alone does
not enable workload metrics. The collector must have an **OTLP metrics pipeline**,
even if it already accepts traces. Endpoint protocol and authentication headers
come from the platform tracing configuration, including `/v1/metrics` appended
to the base URL for HTTP. Per-app endpoint overrides do not redirect these
platform observations.

| Metric | OTLP type | Unit | Meaning |
|---|---|---|---|
| `shinyhub.process.memory.usage` | Gauge | `By` | Summed RSS of the native launch process group, including the launcher and workers that remain in that group. |
| `shinyhub.process.cpu.time` | Monotonic cumulative sum | `s` | Observed user and system CPU seconds accumulated since registration. |
| `shinyhub.replicas` | Gauge | `{replica}` | Native replica processes supervised by this ShinyHub instance, per app. Includes starting, draining and frozen replicas; excludes scheduled commands. |

Resources carry `shinyhub.app`, `shinyhub.app.slug`, deployment identity when
known, operator resource attributes, and either `shinyhub.replica` or
`shinyhub.schedule`, `shinyhub.schedule.name`, and `shinyhub.schedule.run_id`.
Schedule run IDs are strings, matching the injected `OTEL_RESOURCE_ATTRIBUTES`;
the control-plane `schedule.run` span uses an integer run-ID attribute, so some
backends require type normalization when joining it. Environment resource
attributes are decoded with the same percent-encoding as application telemetry.
Explicit app resource overrides are included, while workload identity and host
identity remain authoritative. Unrelated environment variables and command lines
are never exported.

Each observed launch has a unique `service.instance.id` and a `host.name`.
Registration after server recovery starts a new metric stream and CPU accounting
interval. This distinguishes restarts and overlapping deployment generations.
Replica counts use the hostname and server listener address, so a
server restart does not create another count series; sum across hosts/listeners
for a fleet count. Resource attributes remain resources in OTLP;
Prometheus exporters may need resource-to-label conversion or a resource-info
join to expose them as query labels.

CPU data points include `shinyhub.process.cpu.accounting`. `cgroup` uses an
existing dedicated cgroup's kernel counter, including children that exit between
samples or leave the launch process group. Prior usage of a reused cgroup is
excluded. `sampled` retains observed per-process CPU contributions after children
exit and protects against PID reuse, but misses consumption from children that
start and finish between observations. Accounting mode stays fixed for the
observed launch. Workload metrics do not create cgroups or require root.

RSS counts shared pages in every process that maps them; it is not additive
physical-memory attribution. A failed member read omits that interval's RSS
observation rather than reporting a partial sum as complete. CPU sampling can
still report its observed lower bound. Detached processes outside the original
process group are excluded from RSS.

An initial observation is taken at registration, and available observations for
a completed run are retained for export even if it ends before the first tick.
The initial RSS may represent only the launcher. Periodic samples do **not**
guarantee a run's peak memory or complete short-run CPU usage. Cgroup CPU is read
again before teardown. Completed workloads are then exported once with their
actual observation timestamps, subject to retries, and no longer sampled. An
app's replica count receives a zero observation after its last replica exits.
Backend retention and staleness determine when old series disappear from queries.

Sampling and export run independently, with five-second network timeouts. Live
observations coalesce during an outage; up to 4,096 completed workload observations
are retained in memory, with oldest-first eviction and a warning when full.
Transient failures retry with backoff and collector throttling hints. Permanently
rejected observations are dropped; partial success is not retried. There is no
durable telemetry spool. A bounded flush runs on shutdown.

Run IDs create new historical series: a 15-minute schedule creates 192 series per
day for these two resource metrics, before additional dimensions. Choose backend
retention accordingly. This version covers native workloads on the ShinyHub host;
Docker and remote runtimes are not sampled by this exporter.

## The /metrics endpoint

Metrics are opt-in and served on their own listener, separate from the main
application port so server internals are never exposed on a routable interface
by accident:

```yaml
metrics:
  enabled: true
  addr: "127.0.0.1:9090"   # default when enabled and unset
```

Environment overrides (last-wins over YAML):

| YAML field | Environment variable |
|---|---|
| `enabled` | `SHINYHUB_METRICS_ENABLED` |
| `addr` | `SHINYHUB_METRICS_ADDR` |

The endpoint defaults to loopback. Operators scraping from another host set
`addr` to a private interface behind their own network controls (the
conventional Prometheus pattern). When `enabled: false` no handler and no
listener are created.

Scrape it like any Prometheus target:

```yaml
scrape_configs:
  - job_name: shinyhub
    static_configs:
      - targets: ["shinyhub-host:9090"]
```

## Exposed series

### Process and build

| Metric | Type | Description |
|---|---|---|
| `shinyhub_build_info{version}` | gauge | Always `1`; the build version is a label. |
| `shinyhub_uptime_seconds` | gauge | Seconds since the server started serving. |
| `go_*`, `process_*` | various | Standard Go runtime + process collectors (heap, goroutines, server RSS/CPU/FDs). |

### Control-plane HTTP

Labeled by the matched chi route **pattern** (not the raw path), so
high-cardinality path parameters and unmatched 404 scans cannot explode the
series count.

| Metric | Type | Labels | Description |
|---|---|---|---|
| `shinyhub_http_requests_total` | counter | `method`, `route`, `status` | Control-plane HTTP requests. |
| `shinyhub_http_request_duration_seconds` | histogram | `method`, `route`, `status` | Control-plane request latency. |

### Data-plane admission

| Metric | Type | Labels | Description |
|---|---|---|---|
| `shinyhub_admission_rejects_total` | counter | `slug`, `reason` | Proxy admission rejections. `slug` is `__unknown__` for requests to slugs that are not registered apps. |
| `shinyhub_app_sessions` | gauge | `slug` | Active proxied sessions for an app, summed across live replicas (evaluated at scrape time). |
| `shinyhub_app_sessions_limit` | gauge | `slug` | Admission ceiling for an app: the number of replicas that admit new sessions (live, not draining) times the per-replica session cap. Absent for uncapped apps, so `shinyhub_app_sessions / shinyhub_app_sessions_limit` is the saturation fraction wherever a cap applies. |
| `shinyhub_ws_session_duration_seconds` | histogram | `slug` | Completed WebSocket connection lifetime in seconds, with buckets through 24 hours (and `+Inf`). Long connections are normal; this measures neither request latency nor active work time. |
| `shinyhub_ws_session_ends_total` | counter | `slug`, `closed_by`, `transport_end_side`, `abnormal` | Successfully hijacked WebSocket tunnels that ended. `closed_by` identifies the first observed close-frame sender or a known proxy action; it is `unknown` when only a transport end was observed. `transport_end_side` records the first side whose read ended. `abnormal=true` means an upstream close frame or upstream transport end without a 1000/1001/1012 close code, excluding 1011 with reason exactly `keepalive ping timeout`. Other 1011 closes and upstream endings without a close code remain abnormal. |
| `shinyhub_ws_abnormal_bursts_total` | counter | `slug` | Worker-local groups of at least three abnormal WebSocket endings within ten seconds, with a 30-second warning cooldown. The counter is local to each ShinyHub instance. |

Each completed tunnel also emits one structured `ws_session_end` log with its
`connection_id`, replica, deployment, duration, close code when observed, and bytes
written in each tunnel direction. `end_kind` records the bounded outcome classification
listed in [Tracing](tracing.md). `end_signal` distinguishes a close frame,
transport end, proxy action, or unknown ending. Upstream abnormal endings are
WARN; other endings are INFO. A transport EOF does not establish why the worker
stopped responding. This counter measures the same local end events as the logs;
it cannot prove that a specific log record reached an external log store.
When a known lifetime stop or generation cleanup ends a connection, the event
uses `closed_by=lifetime` or `closed_by=drain` and stays at INFO. A
`ws_abnormal_burst` WARN names the app, deployment, worker slot, and number of
abnormal endings in its window, plus their observed time span. It includes a
close code only when all endings in that window agree. Application-provided
close-reason text is omitted from logs and traces because it may contain
sensitive data. It reports
correlation, not a CPU-stall diagnosis.
For Shiny's `/websocket/` connection, the injected interruption overlay sends a
random connection ID on the upgrade and shows that same ID after a connected
session drops. The proxy removes the tag before forwarding the request to the
app. If the script loads after the socket opened or browser crypto is
unavailable, the overlay omits the ID rather than risk showing a different
session's ID; the server still generates and logs one.

The `reason` label is a closed vocabulary. The same value is returned on the
`X-Shinyhub-Reject` response header, so a rejected request can be traced from the
client back to this counter. Reasons differ in what they mean you should *do*,
which is why they are not collapsed into one:

| `reason` | What happened | Remedy |
|---|---|---|
| `unknown-slug` | No app with this slug is registered (404). | Nothing, unless you expected the app to exist. A rising rate is usually scanning. |
| `pool-saturated` | Every replica is live and at its per-replica session cap. | Raise `--max-sessions-per-replica` and/or `--replicas`. This is the only scale-up signal here. |
| `pool-degraded` | Fewer replicas are registered than configured, and the survivors are at cap. | Check replica health first. Adding capacity on top of a crash loop hides it. |
| `app-not-ready` | The app has no replica that has completed a WebSocket handshake yet. | Nothing during a normal cold start. Sustained means the app is failing to come up. |
| `memory-pressure` | The host is below `server.min_available_memory_mb`, so no new elastic worker may start. | Free host memory, lower per-app ceilings, or add hardware. |
| `render-paced` | A new session was deferred because the app's render-admission bucket was empty, then shed after the park window. | Raise the app's [`render_seconds`](scaling.md#render-pacing) accuracy or add cores. More replicas do not help: they do not add CPU. |
| `cpu-saturation` | The host CPU watermark is breached, so a new session was shed to protect connected ones. | Add cores or move apps off this host. |
| `render-deferred` | A page load was shown the "Waiting for capacity" page because the app had no render capacity at that instant. | Same as `render-paced`. See the caveat below before alerting on it. |
| `replica-starting` | A request could not be forwarded because replica readiness has not completed. Non-document clients receive 503 with `Retry-After`. | Allow startup to finish; inspect startup logs if it persists. |

Readiness observations (`app-not-ready`) and expected startup waits
(`replica-starting`) retain their labelled counters but are excluded from the
dashboard's ten-minute admission-issues rollup. A probe poll is not a refused
user session. Failed boots still appear as crashed or degraded apps, and an
upstream failure after readiness remains a `proxy_upstream_error` warning.

`proxy_access` records the actual downstream status. When ShinyHub serves a
starting, deploying, stopped, or crashed page instead of app content, it also
records `fallback: true` and `fallback_reason`. Browser starting pages can
return 200; an unreachable previously ready upstream is identified by
`fallback_reason: "upstream-error"` rather than appearing as an unqualified
success.

Page loads that recover during the bounded inline queue return normally and do
not increment `render-deferred`. The `render_park_*` settings bound page-load and WebSocket waiters
independently.

`render-deferred` counts **page loads deferred**, not sessions refused, and it is
inflated by design: one waiting browser re-polls roughly every 1.75 s until
capacity frees, so a single user can contribute dozens of increments. Use it to
see *that* users are waiting; use `render-paced` to count sessions actually
turned away. Alerting on `render-deferred` as if it were a refusal rate will
page you for one patient user.

Both session gauges are exported **per control-plane instance**, like every metric
here. On a single-node deployment they are exact. In a clustered deployment,
scrape every instance and aggregate in PromQL (`sum by (slug) (...)`) rather than
reading one instance in isolation - the example alert below already does this.

### Usage analytics durability

| Metric | Type | Labels | Description |
|---|---|---|---|
| `shinyhub_usage_persistence_events_total` | counter | `result` | Exceptional durable-usage outcomes: `start_overflow`, `start_retry`, `start_failed`, `start_dropped`, `end_retry`, or `policy_refresh_failed`. Overflow and retries are recovered automatically; failed or dropped starts mean the Usage report may undercount connections. A policy-refresh failure leaves the cached connection fast path conservative while every insert remains clamped against durable policy. |

This counter is process-local. In HA, sum it across every control-plane
instance. An increase in `start_failed` or `start_dropped` is the completeness
alert; the retry and overflow results are useful early warnings.

### Fleet and lifecycle

| Metric | Type | Labels | Description |
|---|---|---|---|
| `shinyhub_apps_running` | gauge | - | Apps currently in the running state (evaluated at scrape time). |
| `shinyhub_replicas_running` | gauge | - | App replicas currently running (evaluated at scrape time). |
| `shinyhub_apps_crashed` | gauge | - | Apps in the crashed state: they exhausted their restart budget and serve nothing. The primary fleet alert target. |
| `shinyhub_fleet_collector_up` | gauge | - | 1 when the scrape-time fleet count query succeeded, 0 when it failed. On 0 the three fleet gauges above are absent for that scrape rather than reading as zero. |
| `shinyhub_deploys_total` | counter | `result` | Deployments by outcome (`success` / `failure`). Alert on a rising failure rate. |
| `shinyhub_generation_handoffs_total` | counter | `outcome` | Generation handoff events from a closed vocabulary: `success`, `deferred`, `explicit_downtime`, `candidate_failure`, `cutover_repair`, `forced_retirement`, or `cleanup_deferred`. |
| `shinyhub_generation_draining` | gauge | - | Old generations currently draining on this control-plane process. |
| `shinyhub_generation_draining_sessions` | gauge | - | Open HTTP streams and WebSockets still attached to draining generations on this control-plane process. |
| `shinyhub_generation_drain_duration_seconds` | histogram | - | Time from cutover until the old route retired or cleanup was deferred. |
| `shinyhub_app_state_transitions_total` | counter | `event` | App lifecycle transitions (`hibernate`, `wake`). |
| `shinyhub_replica_restarts_total` | counter | - | Replica crash-restarts performed by the watchdog. A flapping app shows up as a rising restart rate. |
| `shinyhub_schedule_runs_total` | counter | `slug`, `schedule`, `status` | Finished runs by terminal status: `succeeded`, `failed`, `timed_out`, `interrupted`, `cancelled`, or `superseded`. Counted once per run, by the writer that recorded the outcome. `succeeded`, `failed`, `timed_out` and `interrupted` are published at 0 as soon as a schedule is registered. Process-local: resets on restart and is recorded by the instance that runs the scheduler. |
| `shinyhub_schedule_dispatch_failures_total` | counter | `slug`, `schedule`, `reason` | Cron and catch-up dispatches rejected before a run existed, so no run, audit event or `runs_total` sample records them. `reason` is `app_deploying`, `quarantined`, or `error`; all three are published at 0 per schedule. A shutdown is not counted. |
| `shinyhub_schedule_last_success_seconds` | gauge | `slug`, `schedule` | Unix timestamp of the most recent successful command run; absent until the first success. |
| `shinyhub_schedule_last_failure_seconds` | gauge | `slug`, `schedule` | Unix timestamp at which the newest `failed` or `timed_out` run finished; absent when there is none. |
| `shinyhub_schedule_last_interrupted_seconds` | gauge | `slug`, `schedule` | Unix timestamp at which the newest `interrupted` run was closed; absent when there is none. |
| `shinyhub_schedule_consecutive_failures` | gauge | `slug`, `schedule` | `failed` and `timed_out` runs since the latest `succeeded` run (or ever, for a schedule that has never succeeded), within retained run history. `interrupted` and `cancelled` runs neither count nor reset it. Present for every schedule. |
| `shinyhub_schedule_enabled` | gauge | `slug`, `schedule` | 1 for an enabled schedule, 0 for a disabled or retired one. Present for every schedule. |
| `shinyhub_schedule_stale` | gauge | `slug`, `schedule` | 1 when data is overdue under the shared cron-aware freshness policy, otherwise 0; disabled and retired schedules report 0. Absent when freshness is unknown. |
| `shinyhub_schedule_staleness_seconds` | gauge | `slug`, `schedule` | Seconds past the first unmet success deadline, clamped to zero; absent when freshness is unknown. |
| `shinyhub_schedule_expected_success_timestamp_seconds` | gauge | `slug`, `schedule` | First cron fire after last success or creation, plus the fixed 10-minute freshness margin. Absent when disabled or unknown. |
| `shinyhub_schedule_next_run_timestamp_seconds` | gauge | `slug`, `schedule` | Next nominal cron fire after the scrape time, not a dispatch guarantee. Absent when disabled, unknown, or using an `@every` interval. |
| `shinyhub_schedule_freshness_known` | gauge | `slug`, `schedule` | 1 when freshness is known, 0 for invalid timezone or cron configuration, including expressions that never fire. Disabled schedules report 1. |
| `shinyhub_schedule_collector_up` | gauge | - | 1 when the scrape-time schedule query succeeded, 0 when it failed. On 0 every other database-backed schedule gauge is absent for that scrape. |
| `shinyhub_schedule_activation_status` | gauge | `slug`, `schedule`, `status` | Current durable serving-data activation state; the labeled state has value 1. Alert on `repairing`, `deferred_capacity`, `failed`, `cancelled`, or `blocked_unsupported` according to age and policy. |
| `shinyhub_schedule_activation_age_seconds` | gauge | `slug`, `schedule` | Age of a nonterminal activation. Use with the status metric so ordinary interval damping is not mistaken for a fault. |
| `shinyhub_schedule_activation_capacity_deferrals` | gauge | `slug`, `schedule` | Deferral count of the latest activation, retained across coalesced producer successes. Historical terminal activations retain their counts. |
| `shinyhub_schedule_activation_capacity_deferred_age_seconds` | gauge | `slug`, `schedule` | Age since the first capacity deferral of outstanding work, retained across coalesced producer successes. Absent after completion or cancellation; alert on this age directly so new producer successes cannot reset the alert timer. |
| `shinyhub_schedule_activation_target_generation` | gauge | `slug`, `schedule` | Target serving-data generation of the latest activation. |

### Alerting on schedule failures

Every schedule signal names the schedule: metrics carry `slug` and `schedule`
labels, and the log lines and audit events below carry `app`, `schedule` and
`schedule_id`. An alert can therefore say which schedule failed without a
lookup.

Which run statuses to alert on:

- `failed` and `timed_out` are the default alert: the command ran and did not
  finish successfully.
- `interrupted` means the run never reached a verdict: ShinyHub shut down
  while it was in progress, found it still marked running after a crash, or
  could not complete its admission. It says nothing about the command itself,
  so route it separately, typically at lower severity.
- `cancelled` and `superseded` are deliberate outcomes (an operator cancel, a
  newer deploy replacing the run) and should not page. Overlap skips
  (`skipped_overlap`) are recorded as runs in the UI and API only; they produce
  no metric and no audit event.

The two kinds of schedule metric answer different questions:

- **Database-backed gauges** (`shinyhub_schedule_consecutive_failures`,
  `shinyhub_schedule_last_failure_seconds`,
  `shinyhub_schedule_last_interrupted_seconds`,
  `shinyhub_schedule_last_success_seconds`, `shinyhub_schedule_enabled`) are
  read from the database on each scrape, so they survive restarts and agree on
  every control-plane instance. Alert on run outcomes with these. In a
  clustered deployment, aggregate them with `max by (slug, schedule) (...)` so
  each schedule alerts once rather than once per instance.
- **Counters** (`shinyhub_schedule_runs_total`,
  `shinyhub_schedule_dispatch_failures_total`) are process-local and reset on
  restart; query them with `increase(...)`, which tolerates resets. Every
  alertable series is published at 0 when the schedule is registered, so the
  first event after startup is a visible increase. `runs_total` suits
  dashboards and rates rather than run-outcome alerts: an outcome counted just
  before the process dies is lost if it was never scraped, and runs a crash
  left behind are closed as `interrupted` during startup, before the new
  process is first scraped. A rejected dispatch leaves no run in the
  database, so `dispatch_failures_total` is the only signal for it.

`shinyhub_schedule_last_success_seconds` alone cannot catch a schedule that has
never succeeded, because the series is absent until the first success. Use
`shinyhub_schedule_stale` to detect overdue data even when no run ever started.
It shares the [API freshness policy](schedules.md#monitoring-data-freshness),
including timezone and DST handling: first fire after last success (or creation)
plus 10 minutes. Active runs do not make stale data fresh or extend that deadline
by their timeout. `shinyhub_schedule_staleness_seconds` measures time past that
deadline, so it grows across repeated failures. For run failures specifically,
use `shinyhub_schedule_consecutive_failures` with `shinyhub_schedule_enabled`.
Alert on `shinyhub_schedule_freshness_known == 0` to detect invalid configuration.
Alert on `shinyhub_schedule_collector_up == 0` so a database outage is not read
as "no schedule is failing".

The same outcomes are available as structured log lines for a log-based
alerting pipeline:

- `schedule_run_finished`, once per finished run, at `WARN` for `failed`,
  `timed_out` and `interrupted` and `INFO` otherwise. Fields: `app`,
  `schedule`, `schedule_id`, `run_id`, `status`, `exit_code` (null when no
  process exit was observed), and `trigger`.
- `schedule_dispatch_rejected`, at `WARN`, when a cron or catch-up dispatch is
  rejected before a run starts. Fields: `app`, `schedule`, `schedule_id`,
  `trigger`, `reason`, and `err`.

Run outcomes are also written to the audit log; see
[schedules audit log](schedules.md#audit-log).

```yaml
groups:
  - name: shinyhub-schedules
    rules:
      - alert: ShinyHubScheduleFailed
        # Fires for 15 minutes after each failed or timed_out run.
        expr: time() - max by (slug, schedule) (shinyhub_schedule_last_failure_seconds) < 900
        annotations:
          summary: "Schedule {{ $labels.schedule }} on {{ $labels.slug }} failed or timed out"

      - alert: ShinyHubScheduleFailingRepeatedly
        expr: max by (slug, schedule) (shinyhub_schedule_consecutive_failures) >= 3
          and on (slug, schedule) max by (slug, schedule) (shinyhub_schedule_enabled) == 1
        annotations:
          summary: "Schedule {{ $labels.schedule }} on {{ $labels.slug }} has failed {{ $value }} times in a row"

      - alert: ShinyHubScheduleInterrupted
        # Fires for an hour, including for runs closed at startup after a crash.
        expr: time() - max by (slug, schedule) (shinyhub_schedule_last_interrupted_seconds) < 3600
        labels:
          severity: info
        annotations:
          summary: "A run of {{ $labels.schedule }} on {{ $labels.slug }} was interrupted"

      - alert: ShinyHubScheduleNotDispatched
        expr: sum by (slug, schedule, reason) (increase(shinyhub_schedule_dispatch_failures_total[1h])) > 0
        annotations:
          summary: "Schedule {{ $labels.schedule }} on {{ $labels.slug }} did not start ({{ $labels.reason }})"

      - alert: ShinyHubScheduleStale
        expr: max by (slug, schedule) (shinyhub_schedule_stale) == 1
        for: 15m
        annotations:
          summary: "Data from {{ $labels.schedule }} on {{ $labels.slug }} is overdue"

      - alert: ShinyHubScheduleFreshnessUnknown
        expr: min by (slug, schedule) (shinyhub_schedule_freshness_known) == 0
        for: 5m
        annotations:
          summary: "Freshness for {{ $labels.schedule }} on {{ $labels.slug }} cannot be computed"

      - alert: ShinyHubScheduleMetricsUnavailable
        expr: min(shinyhub_schedule_collector_up) == 0
        for: 5m
        annotations:
          summary: "Schedule health gauges cannot be read from the database"

      - alert: ShinyHubScheduleActivationStuck
        expr: max by (slug, schedule) (shinyhub_schedule_activation_capacity_deferred_age_seconds) > 3600
        annotations:
          summary: "Serving data for {{ $labels.schedule }} on {{ $labels.slug }} has waited over 1h for capacity"
```

### Application-log durability and delivery

These process-local signals cover the shared-log path used by clustered
Postgres deployments. They deliberately carry no app, replica, or run labels:
those identities are unbounded and belong in the log viewer, not in Prometheus
series. Scrape every control-plane instance and aggregate with `sum(...)` in HA.

| Metric | Type | Labels | Description |
|---|---|---|---|
| `shinyhub_app_log_flush_attempts_total` | counter | `result` | Shared-log database flush attempts (`ok` / `error`). |
| `shinyhub_app_log_flush_duration_seconds` | histogram | - | Database-call duration for every shared-log flush attempt. |
| `shinyhub_app_log_persistence_lag_seconds` | histogram | - | Time from entering the retry buffer to a successful database flush. Failed attempts do not produce a lag sample. |
| `shinyhub_app_log_pending_bytes` | gauge | - | Bytes currently queued for persistence by this process, excluding the one chunk in flight. A brief non-zero value is normal because writes are batched. |
| `shinyhub_app_log_buffer_dropped_bytes_total` | counter | - | Bytes evicted from the bounded retry buffer, including bytes abandoned when the final shutdown flush fails. Any increase means shared history may be incomplete. |
| `shinyhub_app_log_runs_pruned_total` | counter | - | Immutable run records removed from database retention by the owner. |
| `shinyhub_app_log_files_pruned_total` | counter | - | Orphaned immutable files removed from this node's private disk. |
| `shinyhub_app_log_followers` | gauge | - | Active per-run shared database followers on this process. Compare with `shinyhub_app_log_viewers` to confirm concurrent viewers are sharing followers. |
| `shinyhub_app_log_viewers` | gauge | - | Active retained-log viewer subscriptions on this process. |
| `shinyhub_app_log_follow_errors_total` | counter | - | Failed database reads by shared retained-log followers. A rise means live delivery is degraded; durable history remains available for catch-up once reads recover. |

The flush and buffer metrics remain present at zero on single-node SQLite
deployments, where the viewer reads its local files directly. Retention cleanup
counters can still rise there. Like all Prometheus counters, cleanup and loss
totals reset when a ShinyHub process restarts.

After a follower read fails, its polling interval backs off exponentially with
jitter to a five-second ceiling. A successful read restores the normal 200 ms
cadence, while a locally committed log chunk wakes the follower immediately.
This bounds database pressure during an outage without delaying healthy local
delivery.

### Example alerts

```yaml
groups:
  - name: shinyhub
    rules:
      - alert: ShinyHubDeployFailures
        expr: increase(shinyhub_deploys_total{result="failure"}[15m]) > 0
        annotations:
          summary: "A ShinyHub deploy failed in the last 15m"

      - alert: ShinyHubGenerationForcedRetirement
        expr: sum(increase(shinyhub_generation_handoffs_total{outcome="forced_retirement"}[15m])) > 0
        annotations:
          summary: "An old app generation exceeded its drain deadline"

      - alert: ShinyHubGenerationCleanupDeferred
        expr: sum(increase(shinyhub_generation_handoffs_total{outcome="cleanup_deferred"}[15m])) > 0
        annotations:
          summary: "Old generation process cleanup was deferred to startup recovery"

      - alert: ShinyHubGenerationDrainStuck
        expr: sum(shinyhub_generation_draining) > 0
        for: 10m
        annotations:
          summary: "An app generation has been draining for more than 10m"

      - alert: ShinyHubExplicitDowntimeDeploy
        expr: sum(increase(shinyhub_generation_handoffs_total{outcome="explicit_downtime"}[15m])) > 0
        annotations:
          summary: "A deploy used the explicit stop-first fallback"

      - alert: ShinyHubAppsCrashed
        expr: max(shinyhub_apps_crashed) > 0
        annotations:
          summary: "An app exhausted its restart budget and is serving nothing"

      - alert: ShinyHubFleetMetricsUnavailable
        # Without this, a database outage silences ShinyHubAppsCrashed.
        expr: min(shinyhub_fleet_collector_up) == 0
        for: 5m
        annotations:
          summary: "Fleet health gauges cannot be read from the database"

      - alert: ShinyHubReplicaFlapping
        expr: increase(shinyhub_replica_restarts_total[10m]) > 5
        annotations:
          summary: "A ShinyHub replica is crash-restarting repeatedly"

      - alert: ShinyHubSessionsNearCap
        # sum by (slug) aggregates across control-plane instances; on a single
        # node it is simply the one series.
        expr: sum by (slug) (shinyhub_app_sessions) / sum by (slug) (shinyhub_app_sessions_limit) > 0.9
        for: 5m
        annotations:
          summary: "{{ $labels.slug }} is above 90% of its admission ceiling"

      - alert: ShinyHubUsageHistoryIncomplete
        expr: sum(increase(shinyhub_usage_persistence_events_total{result=~"start_failed|start_dropped"}[10m])) > 0
        annotations:
          summary: "Durable app-usage history may be incomplete"

      - alert: ShinyHubAppLogPersistenceErrors
        expr: sum(increase(shinyhub_app_log_flush_attempts_total{result="error"}[10m])) > 0
        annotations:
          summary: "Shared app-log persistence has failed"

      - alert: ShinyHubAppLogBacklogStuck
        expr: sum(shinyhub_app_log_pending_bytes) > 0
        for: 5m
        annotations:
          summary: "Shared app-log bytes have remained queued for 5m"

      - alert: ShinyHubAppLogDataDropped
        expr: sum(increase(shinyhub_app_log_buffer_dropped_bytes_total[5m])) > 0
        annotations:
          summary: "Shared app-log history may be incomplete"

      - alert: ShinyHubAppLogFollowErrors
        expr: sum(increase(shinyhub_app_log_follow_errors_total[10m])) > 0
        annotations:
          summary: "Shared app-log live delivery has encountered database errors"
```

## Access log

Every request emits one structured `api_access` record through `log/slog`
(replacing chi's unstructured stock logger), so the control plane has a single
structured log stream a log aggregator can ingest. Fields:

- `request_id` - per-request correlation ID (see below)
- `method`, `path`, `route` (matched pattern), `status`
- `bytes`, `duration_ms`
- `client_ip` - the trusted-proxy-aware client IP (honest even when ShinyHub
  sits behind an edge proxy; see `server.trusted_proxies`)
- `trace_id`, `span_id`, `trace_sampled` - present when a trace identity exists;
  sampling indicates eligibility for export, not confirmed delivery

### Request-ID correlation

Each request is assigned a correlation ID echoed on the response as the
`X-Request-Id` header and threaded through the request context so downstream
handlers tag their own logs with the same ID. A well-formed inbound
`X-Request-Id` (e.g. minted by a trusted edge proxy) is honored so a request
stays correlated across tiers; a malformed or oversized value is rejected and
replaced, closing a log- and header-injection vector.

### Log <-> trace correlation

When server tracing is enabled (see [tracing.md](tracing.md)), the access log
and the trace are linked in both directions:

- `api_access` and `proxy_access` records carry `trace_id`, `span_id`, and
  `trace_sampled`, so you can find the relevant span from a log line, and
- the server span carries the `request_id` attribute, so you can pivot from a
  trace back to the log line.

For accepted WebSocket upgrades, `proxy_access` is emitted when the handshake
finishes and reports only handshake duration. `ws_session_end` separately
reports connection lifetime and byte totals. When a close span is created,
its `trace_id`, `span_id`, and `trace_sampled` are recorded on that log; the
original handshake is identified by `handshake_trace_id`, `handshake_span_id`,
and `handshake_trace_sampled`, including when the close tracer was removed.
Proxy access records also include `deployment_id` when known.

Contextual server logs inherit active trace fields; request loggers retain
those fields for related background work. Unsampled identities remain useful
for joining logs, but do not imply a trace exists in the backend. Sampled
identities may also be absent there because of export failure, collector-side
sampling, or retention.

### Trace delivery health

When both tracing and metrics are enabled, the private `/metrics` registry
includes the following server-side instruments. App processes export directly
and are not covered by these counters.

| Metric | Type | Description |
|---|---|---|
| `shinyhub_trace_export_batches_total` | counter | Completed export calls after exporter retries, by `result` (`success` or `failure`). |
| `shinyhub_trace_export_spans_total` | counter | Spans in those calls, by `result` (`success` or `failure`). Failure does not establish that every span was rejected; a collector may partially accept a batch. |
| `shinyhub_trace_export_last_success_timestamp_seconds` | gauge | Last successful export time; zero until the first success. Idle workloads need not advance this timestamp. |
| `otel_sdk_processor_span_queue_size` | gauge | Spans waiting in the SDK batch processor's queue, excluding a batch currently being exported. |
| `otel_sdk_processor_span_queue_capacity` | gauge | Queue capacity, respecting standard `OTEL_BSP_*` settings. |
| `otel_sdk_processor_span_processed_total{error_type="queue_full"}` | counter | Sampled spans dropped because the SDK queue was full. |

Native SDK instruments retain component and instrumentation-scope labels.
Their current SDK feature gate, `OTEL_GO_X_OBSERVABILITY`, is enabled only
while constructing the processor at startup, when neither it nor its legacy
alias `OTEL_GO_X_SELF_OBSERVABILITY` has a nonempty value. The original
environment is then restored, so this default does not leak to child processes. An explicit `false`
disables the native SDK instruments; export-result counters remain available.
Only native `otel.sdk.processor.span.*` instruments are exposed; per-span,
exporter and unrelated global-meter instruments are filtered out. ShinyHub does
not send health metrics through the trace exporter or replace the SDK's
bounded, nonblocking queue. SDK metric names are experimental and may change with SDK upgrades.

Alert on increases in failed export batches or queue-full drops. Queue
occupancy approaching capacity is an earlier warning. A successful export means the exporter returned no error, not that a trace
was retained downstream. Reported partial acceptance, even with an HTTP 200
response, is counted as a failure; some spans in that batch may have been
accepted. Monitor collector-side rejection metrics as well. Monitor the collector's own queues and downstream delivery too.

A failed export emits `trace_export_failed`, rate-limited to once per minute
while failure continues; the first successful call afterward emits
`trace_export_recovered`. These logs work without Prometheus enabled. Error
classifications are fixed (`timeout`, `canceled`, or `export`); raw exporter
messages, endpoints and payloads are omitted. Other SDK errors produce bounded
`trace_sdk_error` warnings.

## Server (control-plane) tracing

Enabling `tracing` also instruments the control-plane API: ShinyHub emits one
server span per request and spans for background lifecycle operations
(`lifecycle.wake`, `lifecycle.restart`, `lifecycle.hibernate`, tagged with
`shinyhub.app.slug`), exported to the same OTLP endpoint the managed apps use.
Spans use OpenTelemetry HTTP semantic-convention attribute names
(`http.request.method`, `http.route`, `http.response.status_code`) and carry a
resource identifying the instance (`service.name`, `service.version`,
`service.instance.id`). An inbound `traceparent` is adopted as the parent, so a
client/edge trace links through ShinyHub to the app it proxies.

This reuses the existing `tracing` config block; there is no separate
server-tracing switch. See [tracing.md](tracing.md) for the configuration
fields and the per-app proxy trace buffer.

### Database connection pool

| Metric | Type | Meaning |
|---|---|---|
| `shinyhub_db_open_connections` | gauge | Open connections, including idle connections. |
| `shinyhub_db_in_use_connections` | gauge | Connections currently borrowed by database operations. |
| `shinyhub_db_max_open_connections` | gauge | Configured pool limit; zero means unlimited. |
| `shinyhub_db_wait_count_total` | counter | Connection acquisitions that had to wait for the pool. |
| `shinyhub_db_wait_duration_seconds_total` | counter | Cumulative time waiting to acquire a connection. |

These metrics observe the server's actual pool and carry no DSN or query-text
labels. Wait time excludes query execution and SQLite lock contention. Compare
counter increases over the same interval as request latency and CPU usage;
connection waits alone do not identify a slow query.

Retired reload generations are reported by `shinyhub_retired_generations` and
`shinyhub_retired_sessions`. `shinyhub_retired_state_unknown` is 1 when a held
registry lock has an unreadable or invalid snapshot; session counts then cover
only verified snapshots. These gauges include legacy output-only owners until
exit, with zero sessions. See [reload visibility](zero-downtime-upgrades.md#retired-generation-visibility-and-lifetime).
