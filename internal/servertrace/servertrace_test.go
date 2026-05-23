package servertrace

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/rvben/shinyhub/internal/config"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// newRecordingTracer wires a Tracer to an in-memory span recorder so tests can
// assert on the spans the middleware produces without any OTLP collector.
func newRecordingTracer(t *testing.T) (*Tracer, *tracetest.SpanRecorder) {
	t.Helper()
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	return NewFromProvider(tp, propagation.TraceContext{}), sr
}

func attrValue(span sdktrace.ReadOnlySpan, key string) (attribute.Value, bool) {
	for _, kv := range span.Attributes() {
		if string(kv.Key) == key {
			return kv.Value, true
		}
	}
	return attribute.Value{}, false
}

// TestMiddleware_RecordsServerSpanWithRoutePattern proves a request through the
// router produces one server span named by the chi route PATTERN (collapsing
// path params) with the route + status attributes set.
func TestMiddleware_RecordsServerSpanWithRoutePattern(t *testing.T) {
	tr, sr := newRecordingTracer(t)
	r := chi.NewRouter()
	r.Use(tr.Middleware)
	r.Get("/api/apps/{slug}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/apps/demo", nil))

	spans := sr.Ended()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	s := spans[0]
	if s.Name() != "GET /api/apps/{slug}" {
		t.Errorf("span name = %q, want \"GET /api/apps/{slug}\"", s.Name())
	}
	if s.SpanKind() != trace.SpanKindServer {
		t.Errorf("span kind = %v, want server", s.SpanKind())
	}
	if v, ok := attrValue(s, "http.route"); !ok || v.AsString() != "/api/apps/{slug}" {
		t.Errorf("http.route = %v (ok=%v), want /api/apps/{slug}", v.AsString(), ok)
	}
	if v, ok := attrValue(s, "http.response.status_code"); !ok || v.AsInt64() != 200 {
		t.Errorf("http.response.status_code = %v (ok=%v), want 200", v.AsInt64(), ok)
	}
}

// TestMiddleware_ContinuesIncomingTrace proves an inbound W3C traceparent is
// adopted as the parent so an upstream edge proxy / client trace links to the
// ShinyHub server span rather than starting a disconnected trace.
func TestMiddleware_ContinuesIncomingTrace(t *testing.T) {
	tr, sr := newRecordingTracer(t)
	r := chi.NewRouter()
	r.Use(tr.Middleware)
	r.Get("/api/auth/providers", func(w http.ResponseWriter, _ *http.Request) {})

	const traceID = "0123456789abcdef0123456789abcdef"
	req := httptest.NewRequest("GET", "/api/auth/providers", nil)
	req.Header.Set("traceparent", "00-"+traceID+"-00f067aa0ba902b7-01")
	r.ServeHTTP(httptest.NewRecorder(), req)

	spans := sr.Ended()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	if got := spans[0].SpanContext().TraceID().String(); got != traceID {
		t.Errorf("server span trace ID = %s, want the inbound %s (trace not continued)", got, traceID)
	}
	if !spans[0].Parent().IsValid() {
		t.Error("server span has no parent; inbound traceparent was not adopted")
	}
}

// TestMiddleware_UnmatchedRouteCollapses proves a 404 records under a constant
// route so attacker-controlled paths cannot explode span/attribute cardinality.
func TestMiddleware_UnmatchedRouteCollapses(t *testing.T) {
	tr, sr := newRecordingTracer(t)
	r := chi.NewRouter()
	r.Use(tr.Middleware)
	r.Get("/known", func(http.ResponseWriter, *http.Request) {})
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/secret/scan/path", nil))

	spans := sr.Ended()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	if v, ok := attrValue(spans[0], "http.route"); !ok || v.AsString() != "unmatched" {
		t.Errorf("http.route = %v, want unmatched", v.AsString())
	}
}

// TestMiddleware_5xxSetsErrorStatus proves a server error marks the span Error
// so failed control-plane requests are findable in the trace backend.
func TestMiddleware_5xxSetsErrorStatus(t *testing.T) {
	tr, sr := newRecordingTracer(t)
	r := chi.NewRouter()
	r.Use(tr.Middleware)
	r.Get("/boom", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/boom", nil))

	spans := sr.Ended()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	if spans[0].Status().Code != codes.Error {
		t.Errorf("span status = %v, want Error for a 500 response", spans[0].Status().Code)
	}
}

// TestResource_CarriesServiceIdentity proves the exported resource identifies
// the ShinyHub server by name and build version in the trace backend.
func TestResource_CarriesServiceIdentity(t *testing.T) {
	res := buildResource("v7.7.7")
	var name, version string
	for _, kv := range res.Attributes() {
		switch string(kv.Key) {
		case "service.name":
			name = kv.Value.AsString()
		case "service.version":
			version = kv.Value.AsString()
		}
	}
	if name != "shinyhub" {
		t.Errorf("service.name = %q, want shinyhub", name)
	}
	if version != "v7.7.7" {
		t.Errorf("service.version = %q, want v7.7.7", version)
	}
}

// TestHTTPTracesEndpoint_AppendsSignalPath proves the configured OTLP endpoint
// is treated as a BASE endpoint (the OTEL_EXPORTER_OTLP_ENDPOINT contract the
// managed apps also use), so the HTTP exporter posts to the /v1/traces signal
// path. Without this, a collector behind a base path receives server spans at
// the wrong URL while app spans still work.
func TestHTTPTracesEndpoint_AppendsSignalPath(t *testing.T) {
	cases := map[string]string{
		"http://127.0.0.1:4318":              "http://127.0.0.1:4318/v1/traces",
		"https://collector.example.com/otel": "https://collector.example.com/otel/v1/traces",
		// A trailing slash on the base must not double up into //v1/traces.
		"https://collector.example.com/otel/": "https://collector.example.com/otel/v1/traces",
	}
	for in, want := range cases {
		if got := httpTracesEndpoint(in); got != want {
			t.Errorf("httpTracesEndpoint(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestSetup_BuildsExporterPerProtocol proves Setup constructs a working Tracer
// for both supported OTLP protocols without dialing the collector (exporters
// connect lazily), and that Shutdown is clean.
func TestSetup_BuildsExporterPerProtocol(t *testing.T) {
	for _, proto := range []string{"http/protobuf", "grpc"} {
		t.Run(proto, func(t *testing.T) {
			cfg := config.TracingConfig{
				Enabled:      true,
				OTLPEndpoint: "http://127.0.0.1:4318",
				OTLPProtocol: proto,
				SampleRatio:  1,
			}
			tr, err := Setup(context.Background(), cfg, "v1")
			if err != nil {
				t.Fatalf("Setup(%s): %v", proto, err)
			}
			if err := tr.Shutdown(context.Background()); err != nil {
				t.Errorf("Shutdown(%s): %v", proto, err)
			}
		})
	}
}
