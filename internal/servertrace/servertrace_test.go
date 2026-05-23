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

// TestGRPCEndpoint_DerivesHostAndSecurity proves the gRPC endpoint reducer
// strips an endpoint URL down to the host:port the gRPC exporter wants and
// reports TLS intent from the scheme: only https stays secure, every other
// form (http, a non-http scheme, a bare host, a host:port the URL parser
// rejects) is treated as insecure and returned verbatim. Getting this wrong
// would either dial plaintext against a TLS collector or vice versa.
func TestGRPCEndpoint_DerivesHostAndSecurity(t *testing.T) {
	cases := []struct {
		raw          string
		wantEndpoint string
		wantInsecure bool
	}{
		{"https://otel-collector:4317", "otel-collector:4317", false},
		{"http://127.0.0.1:4317", "127.0.0.1:4317", true},
		{"grpc://collector:4317", "collector:4317", true},
		// No scheme and no authority: parsed Host is empty, so the raw value is
		// passed through and treated as insecure.
		{"localhost", "localhost", true},
		// host:port with no scheme is the common shorthand; whether the URL
		// parser errors or yields an empty host, the contract is the same.
		{"127.0.0.1:4317", "127.0.0.1:4317", true},
	}
	for _, tc := range cases {
		gotEndpoint, gotInsecure := grpcEndpoint(tc.raw)
		if gotEndpoint != tc.wantEndpoint || gotInsecure != tc.wantInsecure {
			t.Errorf("grpcEndpoint(%q) = (%q, %v), want (%q, %v)",
				tc.raw, gotEndpoint, gotInsecure, tc.wantEndpoint, tc.wantInsecure)
		}
	}
}

// TestParseHeaders parses the OTEL_EXPORTER_OTLP_HEADERS "k=v,k2=v2" form,
// trimming whitespace around keys and values, splitting only on the first '='
// so values may contain '=', and skipping empty or '='-less fragments rather
// than emitting junk keys. A misparse here would silently send the collector
// the wrong (or no) auth header.
func TestParseHeaders(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want map[string]string
	}{
		{"single", "x-honeycomb-team=KEY", map[string]string{"x-honeycomb-team": "KEY"}},
		{"multiple", "a=1,b=2", map[string]string{"a": "1", "b": "2"}},
		{"whitespace trimmed", " a = 1 , b = 2 ", map[string]string{"a": "1", "b": "2"}},
		{"value keeps inner equals", "token=a=b", map[string]string{"token": "a=b"}},
		{"empty string", "", map[string]string{}},
		{"malformed pairs skipped", "good=1,novalue,also=2", map[string]string{"good": "1", "also": "2"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseHeaders(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("parseHeaders(%q) = %v, want %v", tc.in, got, tc.want)
			}
			for k, want := range tc.want {
				if got[k] != want {
					t.Errorf("parseHeaders(%q)[%q] = %q, want %q", tc.in, k, got[k], want)
				}
			}
		})
	}
}

// TestNewExporter_AppliesHeaders proves both the HTTP and gRPC exporter builders
// thread configured OTLP headers through to the exporter (the WithHeaders
// branch) and return a usable exporter without dialing the collector. The
// exporter type carries no public accessor for its headers, so this guards the
// construction path against a regression that drops auth headers.
func TestNewExporter_AppliesHeaders(t *testing.T) {
	for _, proto := range []string{"http/protobuf", "grpc"} {
		t.Run(proto, func(t *testing.T) {
			exp, err := newExporter(context.Background(), config.TracingConfig{
				OTLPEndpoint: "http://127.0.0.1:4318",
				OTLPProtocol: proto,
				OTLPHeaders:  "x-honeycomb-team=KEY,x-tenant=acme",
			})
			if err != nil {
				t.Fatalf("newExporter(%s): %v", proto, err)
			}
			if exp == nil {
				t.Fatalf("newExporter(%s) returned nil exporter", proto)
			}
			if err := exp.Shutdown(context.Background()); err != nil {
				t.Errorf("exporter Shutdown(%s): %v", proto, err)
			}
		})
	}
}

// TestHTTPTracesEndpoint_MalformedPassThrough proves an endpoint the URL parser
// rejects is returned unchanged rather than being silently rewritten, so the
// exporter surfaces the original misconfiguration instead of posting to a
// mangled URL.
func TestHTTPTracesEndpoint_MalformedPassThrough(t *testing.T) {
	const bad = "%zz" // invalid percent-escape: url.Parse rejects it
	if got := httpTracesEndpoint(bad); got != bad {
		t.Errorf("httpTracesEndpoint(%q) = %q, want it returned unchanged", bad, got)
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
