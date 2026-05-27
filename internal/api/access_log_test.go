package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/config"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// captureLogs swaps the process default slog logger for one writing JSON to a
// buffer, returning the buffer and a restore func. Lets a test assert on the
// structured records a handler emits.
func captureLogs(t *testing.T) (*bytes.Buffer, func()) {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return &buf, func() { slog.SetDefault(prev) }
}

// findRecord scans newline-delimited JSON slog output for the first record
// whose "msg" equals msg, returning it decoded. Fails the test when absent.
func findRecord(t *testing.T, logs, msg string) map[string]any {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(logs), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line is not JSON: %q: %v", line, err)
		}
		if rec["msg"] == msg {
			return rec
		}
	}
	t.Fatalf("no %q record found in logs:\n%s", msg, logs)
	return nil
}

// TestAccessLog_SetsRequestIDHeaderAndLogs proves every API request gets a
// correlation ID echoed on the response and that one structured api_access
// record is emitted carrying that same ID plus the request shape.
func TestAccessLog_SetsRequestIDHeaderAndLogs(t *testing.T) {
	srv := New(&config.Config{Auth: config.AuthConfig{Secret: "test-secret"}}, nil, nil, nil)
	buf, restore := captureLogs(t)
	defer restore()

	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, httptest.NewRequest("GET", "/api/auth/providers", nil))

	if rec.Code != 200 {
		t.Fatalf("GET /api/auth/providers returned %d: %s", rec.Code, rec.Body.String())
	}

	id := rec.Header().Get(RequestIDHeader)
	if id == "" {
		t.Fatalf("response missing %s header", RequestIDHeader)
	}

	r := findRecord(t, buf.String(), "api_access")
	if r["request_id"] != id {
		t.Fatalf("access log request_id %v != header %q", r["request_id"], id)
	}
	if r["method"] != "GET" {
		t.Fatalf("access log method = %v, want GET", r["method"])
	}
	if r["path"] != "/api/auth/providers" {
		t.Fatalf("access log path = %v, want /api/auth/providers", r["path"])
	}
	if r["route"] != "/api/auth/providers" {
		t.Fatalf("access log route = %v, want matched pattern", r["route"])
	}
	if r["status"].(float64) != 200 {
		t.Fatalf("access log status = %v, want 200", r["status"])
	}
	if _, ok := r["duration_ms"]; !ok {
		t.Fatalf("access log missing duration_ms: %v", r)
	}
}

// TestAccessLog_CorrelatesWithActiveSpan proves that when a server span is
// already active (the tracer middleware runs outside the router), the access log
// carries the span's trace_id AND the span carries the request_id attribute - so
// an operator can pivot between the log line and the trace in either direction.
func TestAccessLog_CorrelatesWithActiveSpan(t *testing.T) {
	srv := New(&config.Config{Auth: config.AuthConfig{Secret: "test-secret"}}, nil, nil, nil)

	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	buf, restore := captureLogs(t)
	defer restore()

	// Wrap accessLog in a handler that starts a span first, mirroring how the
	// tracer middleware sits outside the router in production.
	var wantTraceID string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, span := tp.Tracer("test").Start(r.Context(), "incoming")
		wantTraceID = span.SpanContext().TraceID().String()
		srv.accessLog(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {})).
			ServeHTTP(w, r.WithContext(ctx))
		span.End()
	})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/api/auth/providers", nil))

	logRec := findRecord(t, buf.String(), "api_access")
	if logRec["trace_id"] != wantTraceID {
		t.Fatalf("access log trace_id = %v, want %q", logRec["trace_id"], wantTraceID)
	}
	reqID, _ := logRec["request_id"].(string)
	if reqID == "" {
		t.Fatal("access log missing request_id")
	}

	spans := sr.Ended()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	var gotReqID string
	for _, kv := range spans[0].Attributes() {
		if string(kv.Key) == "request_id" {
			gotReqID = kv.Value.AsString()
		}
	}
	if gotReqID != reqID {
		t.Fatalf("span request_id attribute = %q, want %q", gotReqID, reqID)
	}
}

// TestAccessLog_HonorsSafeInboundRequestID proves a well-formed upstream
// X-Request-Id (set by a trusted edge proxy) is propagated rather than replaced,
// so a trace started at the edge stays correlated through ShinyHub.
func TestAccessLog_HonorsSafeInboundRequestID(t *testing.T) {
	srv := New(&config.Config{Auth: config.AuthConfig{Secret: "test-secret"}}, nil, nil, nil)
	buf, restore := captureLogs(t)
	defer restore()

	req := httptest.NewRequest("GET", "/api/auth/providers", nil)
	req.Header.Set(RequestIDHeader, "edge-abc123")
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if got := rec.Header().Get(RequestIDHeader); got != "edge-abc123" {
		t.Fatalf("X-Request-Id = %q, want propagated edge-abc123", got)
	}
	r := findRecord(t, buf.String(), "api_access")
	if r["request_id"] != "edge-abc123" {
		t.Fatalf("access log request_id = %v, want edge-abc123", r["request_id"])
	}
}

// TestAccessLog_RejectsUnsafeInboundRequestID proves a malformed/oversized
// inbound X-Request-Id is discarded and replaced with a freshly generated ID,
// closing a log- and header-injection vector.
func TestAccessLog_RejectsUnsafeInboundRequestID(t *testing.T) {
	srv := New(&config.Config{Auth: config.AuthConfig{Secret: "test-secret"}}, nil, nil, nil)
	buf, restore := captureLogs(t)
	defer restore()

	req := httptest.NewRequest("GET", "/api/auth/providers", nil)
	req.Header.Set(RequestIDHeader, "bad id\nwith newline")
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	got := rec.Header().Get(RequestIDHeader)
	if got == "" || strings.ContainsAny(got, " \n\r") {
		t.Fatalf("unsafe inbound id leaked into response: %q", got)
	}
	r := findRecord(t, buf.String(), "api_access")
	if r["request_id"] == "bad id\nwith newline" {
		t.Fatalf("access log echoed unsafe inbound id")
	}
}
