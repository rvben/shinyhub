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

Defaults applied when `enabled: true` and the field is unset:

- `otlp_protocol`: `http/protobuf`
- `sample_ratio`: `0.1`
- `slow_request_ms`: `1000`
- `ring_buffer_size`: `200`

## Environment variables injected into each app

When tracing is enabled, every app replica is launched with:

```
OTEL_SERVICE_NAME=<app-slug>
OTEL_RESOURCE_ATTRIBUTES=shinyhub.app=<slug>,shinyhub.replica=<index>
OTEL_EXPORTER_OTLP_ENDPOINT=<your collector>
OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf | grpc
OTEL_EXPORTER_OTLP_HEADERS=<headers if configured>
OTEL_TRACES_SAMPLER=parentbased_traceidratio
OTEL_TRACES_SAMPLER_ARG=<sample_ratio>
```

These are **platform defaults**. Per-app env vars (set via UI or
`PUT /api/apps/<slug>/env/<KEY>`) win on duplicate keys, so any app can
override the collector endpoint, service name, sampler, or headers
independently. The `SHINYHUB_` prefix is the only reserved namespace;
`OTEL_*` is intentionally user-settable.

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
  opentelemetry-instrument shiny run app.py --host ... --port ...
```

uv's `--with` overlay resolves these packages alongside the app's own
dependencies without modifying its venv or lockfile; turn the flag off and
the overlay is gone.

### What you get, and what you don't

- **Transport-layer spans for free.** Shiny for Python runs on Starlette
  (ASGI), so each request gets a server span that nests under ShinyHub's
  propagated trace context, and outbound `requests`/`httpx` calls become
  client spans. This closes the trace at the request boundary: "slow at the
  proxy" becomes "slow inside the app's HTTP hop".
- **Not the reactive graph.** Render functions and calc/effect invalidation
  are not a library boundary; no instrumentor exists for them. Those need
  manual spans (next section).
- **WebSocket noise.** Shiny holds one long-lived WebSocket per session, so
  the auto-instrumented WS span is one long low-signal span. Drop it per app
  with `OTEL_PYTHON_DISABLED_INSTRUMENTATIONS` if it bothers you (table
  below).

### Failure semantics

Instrumentation can never take an app down. If the overlay cannot resolve
(for example the app pins a conflicting `opentelemetry-sdk`) or the wrapped
process fails its health check, ShinyHub retries the boot **uninstrumented**
and logs a warning (`instrumented launch failed; retrying without
auto-instrumentation`) in the server log; uv's resolution error is visible in
the app's own log. Persistent offenders should set `[tracing] auto = false`
in their manifest. Note the failed instrumented attempt costs up to one
health-check timeout before the fallback boots.

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
| `OTEL_PYTHON_DISABLED_INSTRUMENTATIONS=starlette` | Drop the long-lived WebSocket/ASGI spans |
| `OTEL_RESOURCE_ATTRIBUTES=team=ccro,owner=lsf` | Ownership tags on every span (replaces the platform default, so re-add `shinyhub.app=<slug>` if you still want it) |
| `OTEL_SERVICE_NAME=my-name` | Override the default service name (the app slug) |
| `OTEL_EXPORTER_OTLP_ENDPOINT=...` | Send this app's spans to a different collector |

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
server process also exports spans through the OpenTelemetry SDK to the same
OTLP endpoint, so a client/edge trace links through ShinyHub to the app it
proxies:

- **One server span per control-plane request**, named by the matched route
  pattern (not the raw path, so cardinality stays bounded). Spans use HTTP
  semantic-convention attributes (`http.request.method`, `http.route`,
  `http.response.status_code`) and adopt an inbound `traceparent` as the
  parent.
- **Background lifecycle spans** for the watchdog's wake, restart, and
  hibernate operations (`lifecycle.wake`, `lifecycle.restart`,
  `lifecycle.hibernate`), each tagged with `shinyhub.app.slug`, so cold-start
  latency and restart storms are visible in the backend.
- Every exported span carries a resource identifying the instance
  (`service.name`, `service.version`, `service.instance.id`).

This reuses the same `tracing` config block above; there is no separate
server-tracing switch. Server spans and the access log are correlated in both
directions (the span carries the `request_id`; the access-log line carries the
`trace_id`); see [metrics.md](metrics.md) for the access-log fields.

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
