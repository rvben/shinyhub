package api

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/servertrace"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

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

// TestRouter_RecordsServerSpanWhenTracingEnabled proves that once a tracer is
// wired in, requests through the API router produce a server span named by the
// matched chi route pattern. The public /api/auth/providers endpoint needs no
// auth, so this exercises the middleware end to end through the real router.
func TestRouter_RecordsServerSpanWhenTracingEnabled(t *testing.T) {
	srv := New(&config.Config{Auth: config.AuthConfig{Secret: "test-secret"}}, nil, nil, nil)
	tr, sr := recordingTracer(t)
	srv.SetTracer(tr)

	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, httptest.NewRequest("GET", "/api/auth/providers", nil))
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

// TestRouter_NoSpanWhenTracingDisabled proves the router functions normally when
// no tracer is wired in (the default), so server tracing stays strictly opt-in.
func TestRouter_NoSpanWhenTracingDisabled(t *testing.T) {
	srv := New(&config.Config{Auth: config.AuthConfig{Secret: "test-secret"}}, nil, nil, nil)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, httptest.NewRequest("GET", "/api/auth/providers", nil))
	if rec.Code != 200 {
		t.Fatalf("GET /api/auth/providers returned %d with tracing disabled: %s", rec.Code, rec.Body.String())
	}
}
