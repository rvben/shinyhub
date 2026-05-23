package proxy_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/proxy"
	"github.com/rvben/shinyhub/internal/tracing"
)

// TestProxy_InjectsTraceparent verifies that an upstream Shiny process
// receives ShinyHub's span as its parent (continuing the incoming trace ID).
func TestProxy_InjectsTraceparent(t *testing.T) {
	var gotTraceparent string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTraceparent = r.Header.Get("traceparent")
	}))
	defer backend.Close()

	p := proxy.New()
	if err := p.Register("app", backend.URL); err != nil {
		t.Fatal(err)
	}
	buf := tracing.NewBuffer(10, time.Second)
	p.SetTracing(config.TracingConfig{Enabled: true, SampleRatio: 1}, buf)

	incoming := "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"
	req := httptest.NewRequest("GET", "/app/app/", nil)
	req.Header.Set("traceparent", incoming)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if gotTraceparent == "" {
		t.Fatalf("backend received no traceparent")
	}
	if gotTraceparent == incoming {
		t.Errorf("expected fresh span id, but backend got identical traceparent")
	}
	// Trace ID portion (chars 3..35) must be continued.
	if len(gotTraceparent) < 55 || gotTraceparent[3:35] != "0af7651916cd43dd8448eb211c80319c" {
		t.Errorf("trace id not continued: got %q", gotTraceparent)
	}
}

// TestProxy_DisabledTracingLeavesHeaderAlone verifies the hot path is a no-op
// when tracing is off.
func TestProxy_DisabledTracingLeavesHeaderAlone(t *testing.T) {
	var got string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("traceparent")
	}))
	defer backend.Close()

	p := proxy.New()
	if err := p.Register("app", backend.URL); err != nil {
		t.Fatal(err)
	}
	// SetTracing not called: traceCfg.Enabled is false.

	incoming := "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"
	req := httptest.NewRequest("GET", "/app/app/", nil)
	req.Header.Set("traceparent", incoming)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if got != incoming {
		t.Errorf("disabled tracing should pass through unchanged: got %q, want %q", got, incoming)
	}
}

// TRC-1: an upstream connection failure (the ReverseProxy ErrorHandler path)
// must populate span.Error so the documented field is actually emitted and the
// error-admission branch is reachable even when no 5xx status was produced.
func TestProxy_RecordsUpstreamErrorMessage(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := backend.URL
	backend.Close() // connections are now refused, forcing the ErrorHandler

	p := proxy.New()
	if err := p.Register("app", url); err != nil {
		t.Fatal(err)
	}
	buf := tracing.NewBuffer(10, time.Hour) // huge slow threshold: only error/5xx admit
	p.SetTracing(config.TracingConfig{Enabled: true, SampleRatio: 1}, buf)

	req := httptest.NewRequest("GET", "/app/app/page", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	spans := buf.Snapshot("app")
	if len(spans) != 1 {
		t.Fatalf("expected one buffered span, got %d", len(spans))
	}
	if spans[0].Error == "" {
		t.Fatalf("upstream connection failure must populate span.Error (status=%d)", spans[0].Status)
	}
}

// TRC-1: an upstream that drops the connection mid-stream (after a 200 header
// was already sent) does NOT trigger the ReverseProxy ErrorHandler; the error
// surfaces only on the body copy. The span must still record Error, which is
// the only thing that admits it to the buffer when the status is 200 and the
// request was not slow. This exercises the otherwise-dead Error-only branch.
func TestProxy_RecordsMidStreamErrorMessage(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000") // promise more than we send
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("partial"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// Hijack and slam the connection so the proxy's body read sees an
		// unexpected EOF before Content-Length bytes arrive.
		if hj, ok := w.(http.Hijacker); ok {
			conn, _, err := hj.Hijack()
			if err == nil {
				_ = conn.Close()
			}
		}
	}))
	defer backend.Close()

	p := proxy.New()
	if err := p.Register("app", backend.URL); err != nil {
		t.Fatal(err)
	}
	buf := tracing.NewBuffer(10, time.Hour) // huge slow threshold: only error admits
	p.SetTracing(config.TracingConfig{Enabled: true, SampleRatio: 1}, buf)

	req := httptest.NewRequest("GET", "/app/app/page", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	spans := buf.Snapshot("app")
	if len(spans) != 1 {
		t.Fatalf("a mid-stream upstream drop must be buffered via Error, got %d spans", len(spans))
	}
	if spans[0].Error == "" {
		t.Fatalf("mid-stream drop must populate span.Error (status=%d)", spans[0].Status)
	}
}

// TestProxy_RecordsErrorToBuffer verifies that a 5xx response is admitted to
// the ring buffer with method, path, status, and replica information.
func TestProxy_RecordsErrorToBuffer(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer backend.Close()

	p := proxy.New()
	if err := p.Register("app", backend.URL); err != nil {
		t.Fatal(err)
	}
	buf := tracing.NewBuffer(10, time.Hour) // slow threshold huge so only error path admits
	p.SetTracing(config.TracingConfig{Enabled: true, SampleRatio: 1}, buf)

	req := httptest.NewRequest("GET", "/app/app/page", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	spans := buf.Snapshot("app")
	if len(spans) != 1 {
		t.Fatalf("expected one buffered span, got %d", len(spans))
	}
	s := spans[0]
	if s.Status != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", s.Status)
	}
	if s.Method != "GET" {
		t.Errorf("method = %q", s.Method)
	}
	if s.Path != "/page" {
		t.Errorf("path = %q, want /page (stripped /app/<slug> prefix)", s.Path)
	}
}
