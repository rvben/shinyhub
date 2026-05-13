# OpenTelemetry Tracing

ShinyHub propagates W3C trace context through its reverse proxy and injects
the OTEL\_\* environment variables every app process needs to export its own
spans to your OpenTelemetry collector. Apps export their spans directly to the
collector — ShinyHub never sees the bytes — and the Traces tab in the UI shows
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
4. Drops everything else — the buffer never grows beyond `ring_buffer_size`
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
independently. The `SHINYHUB_` prefix is the only reserved namespace —
`OTEL_*` is intentionally user-settable.

## Instrumenting your Shiny app

Follow Posit's guide:
<https://shiny.posit.co/py/docs/opentelemetry.html>

```python
# requirements.txt
shiny
opentelemetry-distro
opentelemetry-exporter-otlp
opentelemetry-instrumentation-asgi
```

```bash
opentelemetry-bootstrap -a install
opentelemetry-instrument shiny run app.py
```

ShinyHub's process manager runs whatever command your bundle declares
(`shinyhub.yaml` → `command`), so swap `shiny run` for
`opentelemetry-instrument shiny run` and the OTEL\_\* env vars take care of
the rest.

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
- **Trace** — the short trace ID, with a link to your backend if
  `trace_link_template` is configured (`{trace_id}` is replaced with the full
  32-hex trace ID).

## API

`GET /api/apps/<slug>/traces` — same auth model as `/metrics` (any user who
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

## What ShinyHub does not do

- **No embedded OTLP receiver.** ShinyHub forwards trace context; it does not
  collect, store, or visualise traces. Run a collector (Tempo, Jaeger,
  Grafana Alloy, Honeycomb, etc.) and point apps at it.
- **No log/metric correlation in the buffer.** The ring buffer is
  proxy-level metadata only; for full request data, follow the trace ID into
  your backend.
- **No sidecar.** The OTEL\_\* env approach uses the OpenTelemetry SDK that
  Shiny already loads — no separate agent, no exporter binary on the host.
