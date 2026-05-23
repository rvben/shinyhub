package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/servertrace"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// attrValue returns the value of the named attribute on a recorded span.
func attrValue(span sdktrace.ReadOnlySpan, key string) (attribute.Value, bool) {
	for _, kv := range span.Attributes() {
		if string(kv.Key) == key {
			return kv.Value, true
		}
	}
	return attribute.Value{}, false
}

// recordingTracer wires a servertrace.Tracer to an in-memory span recorder so
// router tests can assert on the spans the wired-in middleware produces without
// any OTLP collector.
func recordingTracer(t *testing.T) (*servertrace.Tracer, *tracetest.SpanRecorder) {
	t.Helper()
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	return servertrace.NewFromProvider(tp, propagation.TraceContext{}), sr
}

// TestObserve_RecordsServerSpanWhenTracingEnabled proves that once a tracer is
// wired in, requests through the observed API handler produce a server span
// named by the matched chi route pattern. The public /api/auth/providers
// endpoint needs no auth, so this exercises the middleware end to end through
// the real router.
func TestObserve_RecordsServerSpanWhenTracingEnabled(t *testing.T) {
	srv := New(&config.Config{Auth: config.AuthConfig{Secret: "test-secret"}}, nil, nil, nil)
	tr, sr := recordingTracer(t)
	srv.SetTracer(tr)

	rec := httptest.NewRecorder()
	srv.Observe(srv.Router()).ServeHTTP(rec, httptest.NewRequest("GET", "/api/auth/providers", nil))
	if rec.Code != 200 {
		t.Fatalf("GET /api/auth/providers returned %d: %s", rec.Code, rec.Body.String())
	}

	spans := sr.Ended()
	if len(spans) != 1 {
		t.Fatalf("expected 1 server span, got %d", len(spans))
	}
	if got := spans[0].Name(); got != "GET /api/auth/providers" {
		t.Fatalf("span name = %q, want \"GET /api/auth/providers\"", got)
	}
}

// TestObserve_NoSpanWhenTracingDisabled proves the observed handler functions
// normally when no tracer is wired in (the default), so server tracing stays
// strictly opt-in.
func TestObserve_NoSpanWhenTracingDisabled(t *testing.T) {
	srv := New(&config.Config{Auth: config.AuthConfig{Secret: "test-secret"}}, nil, nil, nil)
	rec := httptest.NewRecorder()
	srv.Observe(srv.Router()).ServeHTTP(rec, httptest.NewRequest("GET", "/api/auth/providers", nil))
	if rec.Code != 200 {
		t.Fatalf("GET /api/auth/providers returned %d with tracing disabled: %s", rec.Code, rec.Body.String())
	}
}

// TestObserve_SpanReflectsTimeoutStatus proves that a request exceeding the
// timeout handler's deadline produces a span carrying the 503 the client
// actually received, because tracing wraps the timeout handler rather than
// running inside it.
func TestObserve_SpanReflectsTimeoutStatus(t *testing.T) {
	srv := New(&config.Config{Auth: config.AuthConfig{Secret: "test-secret"}}, nil, nil, nil)
	tr, sr := recordingTracer(t)
	srv.SetTracer(tr)

	inner := chi.NewRouter()
	inner.Get("/api/slow", func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})
	timed := http.TimeoutHandler(inner, 5*time.Millisecond, `{"error":"timeout"}`)

	rec := httptest.NewRecorder()
	srv.Observe(timed).ServeHTTP(rec, httptest.NewRequest("GET", "/api/slow", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("client saw status %d, want 503", rec.Code)
	}

	spans := sr.Ended()
	if len(spans) != 1 {
		t.Fatalf("expected 1 server span, got %d", len(spans))
	}
	if v, ok := attrValue(spans[0], "http.response.status_code"); !ok || v.AsInt64() != 503 {
		t.Fatalf("span status_code = %v (ok=%v), want 503", v.AsInt64(), ok)
	}
	if spans[0].Status().Code != codes.Error {
		t.Fatalf("span status = %v, want Error for a 503", spans[0].Status().Code)
	}
}
