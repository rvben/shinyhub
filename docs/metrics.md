---
description: "Prometheus metrics for the control plane, a structured access log for every request, and how both line up with trace identifiers."
---

# Metrics and Observability

ShinyHub exposes Prometheus metrics for the server process itself (the control
plane), emits a structured access log for every request, and - when tracing is
enabled - records control-plane spans correlated with that access log. This is
separate from the per-app CPU/RAM sampling shown in the dashboard and from the
per-app proxy trace buffer documented in [tracing.md](tracing.md).

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

### Fleet and lifecycle

| Metric | Type | Labels | Description |
|---|---|---|---|
| `shinyhub_apps_running` | gauge | - | Apps currently in the running state (evaluated at scrape time). |
| `shinyhub_replicas_running` | gauge | - | App replicas currently running (evaluated at scrape time). |
| `shinyhub_deploys_total` | counter | `result` | Deployments by outcome (`success` / `failure`). Alert on a rising failure rate. |
| `shinyhub_app_state_transitions_total` | counter | `event` | App lifecycle transitions (`hibernate`, `wake`). |
| `shinyhub_replica_restarts_total` | counter | - | Replica crash-restarts performed by the watchdog. A flapping app shows up as a rising restart rate. |
| `shinyhub_schedule_last_success_seconds` | gauge | `slug`, `schedule` | Unix timestamp of the most recent successful command run; absent until the first success. |
| `shinyhub_schedule_activation_status` | gauge | `slug`, `schedule`, `status` | Current durable serving-data activation state; the labeled state has value 1. Alert on `repairing`, `deferred_capacity`, `failed`, or `blocked_unsupported` according to age and policy. |
| `shinyhub_schedule_activation_age_seconds` | gauge | `slug`, `schedule` | Age of a nonterminal activation. Use with the status metric so ordinary interval damping is not mistaken for a fault. |
| `shinyhub_schedule_activation_target_generation` | gauge | `slug`, `schedule` | Target serving-data generation of the latest activation. |

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
- `trace_id` - present when tracing is enabled and a span is active

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

- the `api_access` record carries the active span's `trace_id`, so you can
  pivot from a log line to the trace, and
- the server span carries the `request_id` attribute, so you can pivot from a
  trace back to the log line.

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
