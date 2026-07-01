# Metrics and Observability

ShinyHub exposes Prometheus metrics for the server process itself (the control
plane), emits a structured access log for every request, and - when tracing is
enabled - records control-plane spans correlated with that access log. This is
separate from the per-app CPU/RAM sampling shown in the dashboard and from the
per-app proxy trace buffer documented in [tracing.md](tracing.md).

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
