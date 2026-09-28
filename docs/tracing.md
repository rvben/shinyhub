---
description: "Propagate W3C trace context through the reverse proxy and inject the OTEL variables each app process needs to export its own spans."
---

# OpenTelemetry Tracing

ShinyHub propagates W3C trace context through its reverse proxy and injects
the OTEL\_\* environment variables every app process needs to export its own
spans to your OpenTelemetry collector. Apps export their spans directly to the
collector (ShinyHub never sees the bytes) and the Traces tab in the UI shows
a per-app ring buffer of recent slow or failed proxy spans, deep-linkable into
your backend (Tempo, Jaeger, Honeycomb, etc.).

This keeps ShinyHub a single binary with no embedded OTLP receiver: the
operator picks the backend, the apps export, and ShinyHub just propagates and
surfaces.

## How it works

```
client ──► ShinyHub proxy ──► Shiny app process
              │                     │
              │                     └──► OTLP collector (operator-owned)
              │                            │
              └────────────────────────────┘
              traceparent header        full app spans
              flows end-to-end          delivered directly
```

For every proxied request, ShinyHub:

1. Parses any incoming `traceparent` header. Missing or malformed headers
   start a new trace; valid ones continue it with a fresh span ID for the
   proxy hop.
2. Sets `traceparent` on the upstream request so the app sees ShinyHub's span
   as its parent. Shiny for Python's built-in OpenTelemetry support then
   reports a single connected trace.
3. Records the proxy-level span (method, path, status, duration, replica,
   sampled flag) into a per-app ring buffer if the request was slow, returned
   5xx, or errored.
4. Drops everything else; the buffer never grows beyond `ring_buffer_size`
   spans per app.

The sampling decision uses W3C parent-based traceidratio: child spans honor
the parent's `sampled` flag, and roots fall under `sample_ratio` of all
traces.

## Configuration

Enable tracing in `shinyhub.yaml`:

```yaml
tracing:
  enabled: true
  otlp_endpoint: http://collector.observability.svc:4318
  otlp_protocol: http/protobuf      # or "grpc"
  otlp_headers: "x-api-key=secret"  # optional, for hosted backends
  sample_ratio: 0.1                 # 10% of new traces
  slow_request_ms: 1000             # slow-threshold for buffer admission
  ring_buffer_size: 200             # spans retained per app
  trace_link_template: "https://tempo.example.com/explore?trace={trace_id}"
  auto_instrument_apps: false       # wrap Python apps in opentelemetry-instrument
  auto_instrument_extra_packages:   # extras for the auto-instrument overlay
    - opentelemetry-instrumentation-botocore
  resource_attributes:              # tags added to every span, here and in apps
    deployment.environment.name: production
```

Every field has an env-var override (last-wins over YAML):

| YAML field | Environment variable |
|---|---|
| `enabled` | `SHINYHUB_TRACING_ENABLED` |
| `otlp_endpoint` | `SHINYHUB_TRACING_OTLP_ENDPOINT` |
| `otlp_protocol` | `SHINYHUB_TRACING_OTLP_PROTOCOL` |
| `otlp_headers` | `SHINYHUB_TRACING_OTLP_HEADERS` |
| `sample_ratio` | `SHINYHUB_TRACING_SAMPLE_RATIO` |
| `slow_request_ms` | `SHINYHUB_TRACING_SLOW_REQUEST_MS` |
| `ring_buffer_size` | `SHINYHUB_TRACING_RING_BUFFER_SIZE` |
| `trace_link_template` | `SHINYHUB_TRACING_TRACE_LINK_TEMPLATE` |
| `auto_instrument_apps` | `SHINYHUB_TRACING_AUTO_INSTRUMENT_APPS` |
| `auto_instrument_extra_packages` | `SHINYHUB_TRACING_AUTO_INSTRUMENT_EXTRA_PACKAGES` |
| `resource_attributes` | `SHINYHUB_TRACING_RESOURCE_ATTRIBUTES` |

`auto_instrument_extra_packages` requires `auto_instrument_apps`; its env form
is whitespace-separated PEP 508 requirements (name, optional `[extras]`,
optional version specifiers; no spaces inside one requirement, no URLs):
`SHINYHUB_TRACING_AUTO_INSTRUMENT_EXTRA_PACKAGES="opentelemetry-instrumentation-botocore opentelemetry-instrumentation-redis>=0.40"`.

`resource_attributes` keys must start with a letter, followed by any mix of
letters, digits, `.`, `_`, or `-` (up to 128 characters); the env form
is the same `k=v,k2=v2` wire format as `OTEL_RESOURCE_ATTRIBUTES`, with values
percent-encoded (unreserved-character set per RFC 3986) so separators and
non-ASCII survive decoding: `SHINYHUB_TRACING_RESOURCE_ATTRIBUTES="deployment.environment.name=production,team=data-eng"`.
Setting the env var replaces the whole YAML map rather than merging into it.
`service.name`, `service.version`, `service.instance.id`, and any key
prefixed `shinyhub.` are reserved (ShinyHub sets them itself) and rejected at
startup; see [Multiple instances, one backend](#multiple-instances-one-backend).

Defaults applied when `enabled: true` and the field is unset:

- `otlp_protocol`: `http/protobuf`
- `sample_ratio`: `0.1`
- `slow_request_ms`: `1000`
- `ring_buffer_size`: `200`

## Environment variables injected into each app

When tracing is enabled, every app replica is launched with:

```
OTEL_SERVICE_NAME=<app-slug>
OTEL_RESOURCE_ATTRIBUTES=shinyhub.app=<slug>,shinyhub.replica=<index>[,<your resource_attributes, sorted by key>]
OTEL_EXPORTER_OTLP_ENDPOINT=<your collector>
OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf | grpc
OTEL_TRACES_SAMPLER=parentbased_traceidratio
OTEL_TRACES_SAMPLER_ARG=<sample_ratio>
OTEL_PYTHON_STARLETTE_EXCLUDED_URLS=/websocket/?$
OTEL_EXPORTER_OTLP_HEADERS=<headers if configured>
```

`tracing.resource_attributes` pairs are appended after the built-in
`shinyhub.app`/`shinyhub.replica` pair, sorted by key, and percent-encoded the
same way as the config env override above. `OTEL_PYTHON_STARLETTE_EXCLUDED_URLS`
keeps Shiny's session WebSocket out of the auto-instrumented Starlette spans
(see [What you get, and what you don't](#what-you-get-and-what-you-dont)
below); it is set for every app whenever tracing is enabled, whether or not
auto-instrumentation is on, since it is harmless for an app that reads no
`OTEL_*` vars.

These are **platform defaults**. Per-app env vars (set via UI or
`PUT /api/apps/<slug>/env/<KEY>`) win on duplicate keys, so any app can
override the collector endpoint, service name, sampler, or headers
independently. The `SHINYHUB_` prefix is the only reserved namespace;
`OTEL_*` is intentionally user-settable.

## Multiple instances, one backend

Point every ShinyHub instance that shares a fleet (a zero-downtime upgrade
pair, staging vs. production, or two unrelated fleets sharing a collector) at
the same OTLP endpoint and tell them apart in the backend with
`tracing.resource_attributes`:

```yaml
tracing:
  resource_attributes:
    deployment.environment.name: production
```

Give each instance a value that differs (environment, region, cluster) and
filter or group on it in Tempo/Grafana. The pairs land in three places: every
app replica's `OTEL_RESOURCE_ATTRIBUTES` (above), every scheduled job run's
(see [Scheduled jobs](#scheduled-jobs) below), and the resource of every span
the server process itself exports.

`service.instance.id` on the server's own spans is `server.instance_id`
(`shinyhub.yaml` only - there is no `SHINYHUB_SERVER_INSTANCE_ID` env
override), which defaults to `<hostname>-<pid>`. That default changes on
every restart, so if you want to correlate a server's spans across restarts,
or need a stable identity for a specific host rather than one process's
lifetime, set `server.instance_id` explicitly.

If apps or jobs run behind an outbound HTTP proxy (`HTTP_PROXY`/`HTTPS_PROXY`
set in their environment), add the collector's host to `NO_PROXY` on those
hosts so the OTLP exporter reaches it directly instead of through the proxy.

## Auto-instrumentation (zero-config app spans)

With one fleet-level flag, every Python app gets transport-layer spans with
no change to its `pyproject.toml`, `requirements.txt`, or run command:

```yaml
tracing:
  enabled: true
  otlp_endpoint: http://collector.observability.svc:4318
  auto_instrument_apps: true   # default: false
```

Env override: `SHINYHUB_TRACING_AUTO_INSTRUMENT_APPS`. Individual apps opt in
or out against the fleet default in their bundle's `shinyhub.toml`:

```toml
[tracing]
auto = false   # this app opts out (or `true` to opt in when the fleet default is off)
```

The override travels with the bundle: it is re-read at every boot (deploy,
crash restart, hibernation wake) and applies per deployed version, including
rollbacks.

ShinyHub already injects the `OTEL_*` env vars and propagates `traceparent`;
auto-instrumentation adds the remaining two pieces. The app is launched as

```
uv run [--with-requirements requirements.txt] \
  --with opentelemetry-distro \
  --with opentelemetry-exporter-otlp \
  --with opentelemetry-instrumentation-starlette \
  --with opentelemetry-instrumentation-requests \
  --with opentelemetry-instrumentation-httpx \
  [--with <tracing.auto_instrument_extra_packages>] \
  opentelemetry-instrument python -m shiny run app.py --host ... --port ...
```

The entrypoint runs as `python -m shiny` so it executes under the overlay's
interpreter; the app's own `shiny` console script would run under its own
environment's interpreter, which cannot see the overlay packages.

uv's `--with` overlay resolves these packages alongside the app's own
dependencies without modifying its venv or lockfile; turn the flag off and
the overlay is gone. `tracing.auto_instrument_extra_packages` (a fleet-wide
setting) is layered in after the built-in set for instrumentors the built-in
list doesn't cover, such as `opentelemetry-instrumentation-botocore`.

This applies identically to a pool replica's initial boot and to an elastic
worker spawned on demand: both resolve the launch command through the same
seam, honor the same fleet default and manifest override, and get the same
uninstrumented retry (below) if the instrumented launch fails.

### What you get, and what you don't

- **Transport-layer spans for free.** Shiny for Python runs on Starlette
  (ASGI), so each request gets a server span that nests under ShinyHub's
  propagated trace context, and outbound `requests`/`httpx` calls become
  client spans. This closes the trace at the request boundary: "slow at the
  proxy" becomes "slow inside the app's HTTP hop".
- **The reactive graph, from Shiny itself.** Shiny for Python 1.6+ ships its
  own OpenTelemetry instrumentation: once the SDK is wired (by
  auto-instrumentation, or manually per below), Shiny emits `session_start`,
  `reactive_update`, `output <id>`, and `reactive.calc <name>` spans under the
  instrumentation scope `co.posit.python-package.shiny`. With the default
  WebSocket exclusion (below) the session has no request span to nest under,
  so each `session_start` and `reactive_update` is the root of its own trace;
  overriding the exclusion per app nests them under the session's WebSocket
  span, at the cost of one span that lasts the whole session. Verbosity is
  controlled per app with
  `SHINY_OTEL_COLLECT`; Posit suggests `session` in production and
  `reactive_update` in staging. This covers renders and calc/effect
  invalidation without writing a single span by hand - reach for manual spans
  (next section) for your own library code inside a calc or output, which
  Shiny's instrumentation cannot see into.
- **WebSocket span excluded by default.** Shiny holds one long-lived
  WebSocket per session, so an uncontrolled auto-instrumented WS span would be
  one long, low-signal span for the whole visit. ShinyHub sets
  `OTEL_PYTHON_STARLETTE_EXCLUDED_URLS=/websocket/?$` in every app's
  environment whenever tracing is enabled, so the Starlette instrumentor
  skips it. Override the pattern per app the same way as any other `OTEL_*`
  default (table below) - set it to an empty string to trace the WebSocket
  after all (which also nests Shiny's reactive spans under it, above), or to a
  different regex.
- **Logs and metrics too, unless you turn them off.** The
  `opentelemetry-distro` that auto-instrumentation launches defaults
  `OTEL_LOGS_EXPORTER` and `OTEL_METRICS_EXPORTER` to `otlp` as well as
  traces, and sends them to the same `OTEL_EXPORTER_OTLP_ENDPOINT`. A
  collector with only a traces pipeline rejects those exports (over
  `http/protobuf`, a 404 on `/v1/logs` and `/v1/metrics`), and the exporter
  reports each rejection in the app's log. The spans are unaffected. To stop
  it for an app, set `OTEL_LOGS_EXPORTER=none` and/or
  `OTEL_METRICS_EXPORTER=none` in that app's env.

### Failure semantics

Instrumentation can never take an app down. If the overlay cannot resolve,
or the wrapped process crashes at startup (for example the app pins an old
`opentelemetry-api` that breaks `opentelemetry-instrument`'s imports), or it
fails its health check, ShinyHub retries the boot **uninstrumented** and logs
a warning (`instrumented launch failed; retrying without
auto-instrumentation`) in the server log; the uv resolution error or Python
traceback is visible in the app's own log. Persistent offenders should set
`[tracing] auto = false` in their manifest. Note the failed instrumented
attempt costs up to one health-check timeout before the fallback boots.

Scope and caveats:

- **Python only.** R apps (`app.R`/`Rscript`) are never wrapped; there is no
  `opentelemetry-instrument` equivalent for R.
- **Inferred commands only.** Deploys that supply a custom command are never
  wrapped; wrap your own command if you need both.
- **Docker runtime:** the overlay resolves inside the container at start, so
  the first start (and starts after image replacement) download the OTEL
  packages; subsequent starts hit uv's cache only if you persist it. Budget a
  few extra seconds of cold start, including hibernation wakes.

## Tracing your app

Two layers of per-app control sit on top of auto-instrumentation. Both
assume `auto_instrument_apps` (or the app's `[tracing] auto = true`).

**Layer 1 - config knobs, no code.** Per-app env vars win over the injected
platform defaults, so tuning is a few settings (UI → app → Configuration, or
`PUT /api/apps/<slug>/env/<KEY>`):

| Env var | Effect |
|---|---|
| `OTEL_TRACES_SAMPLER_ARG=1.0` | Sample this app harder than the fleet `sample_ratio` |
| `OTEL_PYTHON_STARLETTE_EXCLUDED_URLS=<regex>` | Change which paths the Starlette instrumentor skips; the platform default excludes only the session WebSocket |
| `OTEL_PYTHON_DISABLED_INSTRUMENTATIONS=starlette` | Drop **all** Starlette/ASGI spans, including the per-request server spans, not just the WebSocket; most apps want the exclusion above instead |
| `OTEL_RESOURCE_ATTRIBUTES=team=analytics,owner=data-eng` | Ownership tags on every span. Replaces the entire platform value, including `shinyhub.app`/`shinyhub.replica` and any fleet `resource_attributes` (e.g. `deployment.environment.name`) - re-add anything you still want alongside your own pairs |
| `OTEL_SERVICE_NAME=my-name` | Override the default service name (the app slug) |
| `OTEL_EXPORTER_OTLP_ENDPOINT=...` | Send this app's spans to a different collector |
| `OTEL_LOGS_EXPORTER=none`, `OTEL_METRICS_EXPORTER=none` | Stop the distro exporting logs and metrics, for a collector that only accepts traces |

**Layer 2 - custom spans in two lines.** `opentelemetry-instrument` has
already wired the global TracerProvider, the OTLP exporter, and incoming
`traceparent` extraction, so an app adds its own spans with just:

```python
from opentelemetry import trace

tracer = trace.get_tracer(__name__)

def load_cluster_data():
    with tracer.start_as_current_span("load_cluster_data"):
        return load_data()   # nests under the ASGI request span, exports for free
```

This is where the reactive-graph gap closes: wrap your heavy data loads,
renders, and calcs by hand and they appear inside the request trace.

**The one footgun:** rely on the auto-configured *global* provider - call
`trace.get_tracer(...)` and emit. Do **not** call
`trace.set_tracer_provider(...)` yourself; that double-initialises the SDK
and breaks export.

### Manual instrumentation (without auto-instrumentation)

If the fleet flag is off and the app cannot opt in, the pre-existing route
still works: add `opentelemetry-distro`, `opentelemetry-exporter-otlp`, and
the instrumentors to the bundle's own dependencies and deploy with a custom
command that wraps `shiny run` in `opentelemetry-instrument`. The injected
`OTEL_*` env vars apply either way. Posit's guide:
<https://shiny.posit.co/py/docs/opentelemetry.html>

## The Traces tab

`UI → App detail → Traces` polls `GET /api/apps/<slug>/traces` every 5
seconds and shows the most recent slow or failed proxy spans, newest first.
The buffer is in-memory and per-process, so it resets on ShinyHub restart and
holds at most `ring_buffer_size` spans per app.

Each row shows:

- **When** the request started
- **Method / Path** (the path after stripping the `/app/<slug>` prefix)
- **Status** (HTTP status from the backend)
- **Duration** (ms)
- **Replica** index that handled the request
- **Trace**: the short trace ID, with a link to your backend if
  `trace_link_template` is configured (`{trace_id}` is replaced with the full
  32-hex trace ID).

## API

`GET /api/apps/<slug>/traces` uses the same auth model as `/metrics` (any user who
can view the app):

```json
{
  "enabled": true,
  "trace_link_template": "https://tempo.example.com/explore?trace={trace_id}",
  "spans": [
    {
      "trace_id": "0af7651916cd43dd8448eb211c80319c",
      "span_id": "b7ad6b7169203331",
      "parent_id": "00f067aa0ba902b7",
      "app_slug": "my-app",
      "replica": 0,
      "method": "GET",
      "path": "/session/abc/dataobj",
      "status": 502,
      "duration_ms": 1843,
      "started_at": "2026-05-13T10:34:01Z",
      "sampled": true,
      "error": "context canceled"
    }
  ]
}
```

When tracing is disabled the endpoint still returns `200` with `enabled:
false` and an empty `spans: []` so the UI can render an "off" state without
extra error handling.

## Server-side spans (control plane)

The propagation and ring buffer above cover the **proxy hot path** and the
**app processes**. Separately, when `tracing.enabled` is set, ShinyHub's own
server process exports spans through the OpenTelemetry SDK to the same OTLP
endpoint, so a client/edge trace links through ShinyHub to the app it
proxies, and background work (deploys, wakes, restarts) is visible even when
no client is watching.

- **One server span per control-plane API request**, named by the matched
  route pattern (not the raw path, so cardinality stays bounded), e.g.
  `POST /api/apps/{slug}/deploy`. Spans use HTTP semantic-convention
  attributes (`http.request.method`, `http.route`, `http.response.status_code`)
  and adopt an inbound `traceparent` as the parent.
- **One span per `/app/<slug>/...` proxy request**, named `GET /app/{slug}`
  (method varies, route is always the pattern). Attributes: `http.request.method`,
  `http.route`, `url.path` (the path after stripping the `/app/<slug>` prefix),
  `http.response.status_code`, `shinyhub.app.slug`, and, when known,
  `shinyhub.replica`, `shinyhub.deployment.id`, and `shinyhub.proxy.reject_reason`.
  It adopts an inbound `traceparent` as its parent and uses the same sampling
  decision (`ParentBased(TraceIDRatioBased(sample_ratio))`) as everything
  else, so the trace ID and sampled flag it hands to the app in the outbound
  `traceparent` are the exported span's own: the app's spans always find their
  parent in the backend. An open WebSocket session produces **one** such span
  covering the whole session, not one per message.
- **Background lifecycle spans** for the watchdog's wake, restart, and
  hibernate operations (`lifecycle.wake`, `lifecycle.restart`,
  `lifecycle.hibernate`), each tagged with `shinyhub.app.slug`, so cold-start
  latency and restart storms are visible in the backend. `lifecycle.wake`
  additionally carries `shinyhub.wake.trigger`, one of:
  - `request` - a proxied request found the app hibernated. The span is a
    **child** of that request's `/app` proxy span above, and inherits its
    sampled flag: an inbound `traceparent` with the sampled bit unset
    suppresses the wake span too, along with everything nested under it.
  - `reconcile` - the watchdog's periodic reconciler found an app stuck
    `waking` (e.g. the instance that started the wake died mid-flight) and
    resumed driving it. This is a trace **root**; there is no request to
    parent on.

  There is no third trigger: warm restore at startup (re-freezing hibernated
  apps back to their pre-restart state) does not go through this wake
  machinery at all, so its replica boots below are roots, not
  `lifecycle.wake` children.
- **Deploy phase spans**, opened as internal spans nested by call structure:
  - `deploy.receive`, `deploy.extract`, `deploy.validate`, `deploy.handoff` -
    API-layer phases of a deploy/rollback/restart request, each a direct
    child of the control-plane request span (siblings of each other and of
    `deploy.run` below, not nested inside one another).
  - `deploy.run` - the root of one pool deploy (all replicas of one app
    version), tagged `shinyhub.deploy.replicas` (count) and
    `shinyhub.deploy.prepare_only`. A deploy/rollback/restart submitted
    through the API is its child (via `traceDeploy` with the request's
    context, so it inherits that request's trace and sampling). Two triggers
    run with no live request and are trace roots instead: a replica-count,
    placement, resource-limit, or worker-isolation change from
    `PATCH /api/apps/<slug>` launches its redeploy in a detached goroutine
    that carries no request context; and scheduled activation's disruptive
    capacity-fallback restart (stopping and rebooting the whole pool when a
    surge-based roll cannot find capacity) runs from the background
    activation coordinator loop, which likewise carries no span. A
    schedule's own `deploy_trigger = "bundle_change"` reconvergence never
    reaches this code path at all: it reruns the producer command as an
    ordinary job run (a `schedule.run` span, see
    [Scheduled jobs](#scheduled-jobs) below), not a deploy. Scaling and
    warm-pool operations boot replicas one at a time and never open a
    `deploy.run` at all - see the standalone case below.
    - `deploy.build` - building the bundle's environment (dependency
      install), once per deploy regardless of replica count.
    - `deploy.post_deploy_hooks` - the bundle's post-deploy hook run.
    - `deploy.replica` (one per replica, tagged `shinyhub.replica`) -
      booting one replica.
      - `deploy.replica.start` - launching the process.
      - `deploy.replica.health` - polling until healthy, tagged
        `shinyhub.deploy.health_timeout_ms`.
  - Outside a pool deploy, `deploy.replica` (and its `.start`/`.health`
    children) also appears **standalone**, with no `deploy.run` wrapper:
    - Under `lifecycle.restart` - the watchdog's crash-restart path boots
      exactly the one replica that died.
    - Under `lifecycle.wake` - waking a suspended app first tries an
      abbreviated resume (`deploy.replica` tagged
      `shinyhub.deploy.resume=true`); if that fails, it falls back to a full
      cold boot. A failed-resume-then-cold-boot wake therefore produces
      **two** `deploy.replica` spans nested under the same `lifecycle.wake`:
      the resume attempt (recorded as an error) followed by the cold boot.
    - At startup, **warm restore** re-adopts every replica that was warm
      when the server last stopped, one `deploy.replica` per replica. This
      does not go through the wake machinery at all (see above), so these
      spans are trace **roots**, not children of a `lifecycle.wake`.
    - **Scale up/down** and **warm-pool** operations (growing or shrinking a
      pool, thawing a suspended replica to keep it in the warm floor) boot or
      thaw replicas individually the same way, each producing its own
      standalone `deploy.replica` span rather than a shared `deploy.run`.
    - **Scheduled activation's normal roll** boots the same way: it surges in
      one replica of the new generation, then cuts canonical replicas over to
      it one at a time, each boot its own standalone `deploy.replica`. Only
      the disruptive capacity-fallback restart mentioned above opens a
      `deploy.run`.
  - Regardless of which span parents a deploy or replica boot, the
    background work itself never observes the triggering request's
    cancellation or deadline - only its place in the trace. A client
    disconnecting mid-deploy does not cut the deploy short; it only stops
    that one client from seeing the outcome.
- Every exported span carries a resource identifying the instance
  (`service.name`, `service.version`, `service.instance.id`, plus any
  `tracing.resource_attributes`; see [Multiple instances, one
  backend](#multiple-instances-one-backend)).

This reuses the same `tracing` config block above; there is no separate
server-tracing switch. Server spans and the access log are correlated in both
directions (the span carries the `request_id`; the access-log line carries the
`trace_id`); see [metrics.md](metrics.md) for the access-log fields.

## Scheduled jobs

When `tracing.enabled` is set, every scheduled job run gets a root
`schedule.run` span and the same `OTEL_*` defaults an app replica gets (see
[Environment variables injected into each app](#environment-variables-injected-into-each-app)
above), so the job's own spans, once it starts one, land in the same trace.

The span is opened the moment the run's row is inserted, so it covers the
whole admitted lifetime, including time spent waiting on the schedule's own
lock or an execution fence, through to the terminal status
(`succeeded`/`failed`/etc.). A run that never starts because another run is
still in flight (`skipped_overlap`) gets no span at all - there is nothing to
trace. Attributes: `shinyhub.app.slug`, `shinyhub.schedule.name`,
`shinyhub.schedule.id`, `shinyhub.schedule.run_id`, `shinyhub.schedule.trigger`
(see [Run provenance](schedules.md#run-provenance-trigger)),
`shinyhub.deployment.id`, and, once the run ends,
`shinyhub.schedule.status`, `shinyhub.schedule.persisted`, and
`process.exit.code`. Sampling follows the fleet `sample_ratio` like any other
root span (`ParentBased(TraceIDRatioBased(sample_ratio))`).

The job process itself gets no automatic instrumentation - there is no
overlay equivalent of `auto_instrument_apps` for jobs. Instead, the run's
trace context reaches the process as environment variables, `TRACEPARENT` and
`TRACESTATE` (set last, so they always win over any per-app env of the same
name), which the job extracts to parent its own spans under `schedule.run`.
The recommended job command layers the same OTEL packages the app overlay
uses via `uv run --with`:

```bash
uv run --with-requirements requirements.txt \
  --with opentelemetry-distro \
  --with opentelemetry-exporter-otlp \
  opentelemetry-instrument python refresh.py
```

and the script extracts the propagated context to parent its own spans:

```python
import os
from opentelemetry import trace
from opentelemetry.propagate import extract

carrier = {"traceparent": os.environ.get("TRACEPARENT", "")}
if os.environ.get("TRACESTATE"):
    carrier["tracestate"] = os.environ["TRACESTATE"]
ctx = extract(carrier)

tracer = trace.get_tracer(__name__)
with tracer.start_as_current_span("refresh", context=ctx):
    refresh_data()
```

A job run with tracing disabled has no `TRACEPARENT` set; the snippet above
still works unmodified, since `extract` on an empty carrier returns the
current (background) context and `start_as_current_span` simply starts its
own root trace.

## What ShinyHub does not do

- **No embedded OTLP receiver.** ShinyHub exports its own spans and propagates
  trace context, but it does not receive, collect, or visualise traces for
  other services. Run a collector (Tempo, Jaeger, Grafana Alloy, Honeycomb,
  etc.) and point ShinyHub and the apps at it.
- **No app-span correlation in the ring buffer.** The Traces-tab ring buffer
  is proxy-level metadata only; for full request data, follow the trace ID
  into your backend. (Server spans and the access log are correlated
  separately, as noted above.)
- **No sidecar.** The OTEL\_\* env approach uses the OpenTelemetry SDK that
  Shiny already loads, with no separate agent and no exporter binary on the host.
