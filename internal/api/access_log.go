package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// RequestIDHeader carries the per-request correlation ID. It is honored on
// inbound requests (so an ID minted by a trusted edge proxy survives through
// ShinyHub) and echoed on every response so clients and log aggregators can
// stitch a single request together across tiers.
const RequestIDHeader = "X-Request-Id"

// maxRequestIDLen bounds an honored inbound correlation ID. Anything longer is
// treated as untrusted and replaced, so a caller cannot bloat log lines or
// response headers with an arbitrarily large value.
const maxRequestIDLen = 128

type ctxKeyRequestID struct{}

// RequestIDFromContext returns the correlation ID assigned to the request, or
// "" when the access-log middleware did not run (e.g. a handler exercised
// directly in a test). Handlers use it to tag their own structured logs with
// the same ID the access log records.
func RequestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(ctxKeyRequestID{}).(string)
	return id
}

// newRequestID returns a fresh 96-bit hex correlation ID.
func newRequestID() string {
	var b [12]byte
	// crypto/rand.Read never returns a short read or error on the platforms we
	// target; ignoring the error keeps the hot path allocation-light.
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// safeRequestID returns id when it is a non-empty, reasonably sized token built
// only from characters safe to embed in a log line and an HTTP header value
// (letters, digits, '-', '_', '.'). Otherwise it returns "" so the caller mints
// a fresh ID. This rejects header/log-injection payloads (newlines, spaces,
// control bytes) without trusting the caller's formatting.
func safeRequestID(id string) string {
	if id == "" || len(id) > maxRequestIDLen {
		return ""
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-' || c == '_' || c == '.':
		default:
			return ""
		}
	}
	return id
}

// accessLog assigns a correlation ID to every request, echoes it on the
// response, threads it through the request context for downstream handlers, and
// emits one structured api_access record (request_id, method, path, matched
// route pattern, status, bytes, duration, client IP) after the handler returns.
// It replaces chi's stock middleware.Logger so the API has a single structured
// log stream instead of an unstructured stderr line running in parallel.
func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID := safeRequestID(r.Header.Get(RequestIDHeader))
		if reqID == "" {
			reqID = newRequestID()
		}
		w.Header().Set(RequestIDHeader, reqID)
		r = r.WithContext(context.WithValue(r.Context(), ctxKeyRequestID{}, reqID))

		// Correlate logs and traces in both directions when a server span is
		// active (the tracer middleware runs outside the router): stamp the
		// request ID onto the span so a trace links back to the log line, and
		// capture the trace ID to emit alongside the access record below.
		var traceID string
		if span := trace.SpanFromContext(r.Context()); span.SpanContext().IsValid() {
			span.SetAttributes(attribute.String("request_id", reqID))
			traceID = span.SpanContext().TraceID().String()
		}

		ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)
		start := time.Now()
		next.ServeHTTP(ww, r)

		status := ww.Status()
		if status == 0 {
			status = http.StatusOK
		}
		attrs := []any{
			"request_id", reqID,
			"method", r.Method,
			"path", r.URL.Path,
			"status", status,
			"bytes", ww.BytesWritten(),
			"duration_ms", time.Since(start).Milliseconds(),
			"client_ip", s.ClientIP(r),
		}
		if route := chi.RouteContext(r.Context()).RoutePattern(); route != "" {
			attrs = append(attrs, "route", route)
		}
		if traceID != "" {
			attrs = append(attrs, "trace_id", traceID)
		}
		slog.Info("api_access", attrs...)
	})
}
