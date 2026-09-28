package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/servertrace"
	"github.com/rvben/shinyhub/internal/spanerr"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"
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
//
// The inner handler wraps the server's real router with a blocking middleware
// so /api/auth/providers (a registered route) is resolved via s.router.Match
// before the inner chain runs. This is the production-accurate shape.
func TestObserve_SpanReflectsTimeoutStatus(t *testing.T) {
	srv := New(&config.Config{Auth: config.AuthConfig{Secret: "test-secret"}}, nil, nil, nil)
	tr, sr := recordingTracer(t)
	srv.SetTracer(tr)

	slow := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			<-r.Context().Done()
		})
	}
	router := srv.Router()
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slow(router).ServeHTTP(w, r)
	})
	timed := http.TimeoutHandler(inner, 5*time.Millisecond, `{"error":"timeout"}`)

	rec := httptest.NewRecorder()
	srv.Observe(timed).ServeHTTP(rec, httptest.NewRequest("GET", "/api/auth/providers", nil))
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

// A deploy request records its handler phases as children of the request's
// server span, in order, and hands deploy.Run a tracer plus a parent context
// carrying that same span so deploy.run nests under the request.
func TestDeploy_PhaseSpansNestUnderRequestSpan(t *testing.T) {
	srv, store, token := newManifestE2EServer(t)
	tr, sr := recordingTracer(t)
	srv.SetTracer(tr)
	seedStoppedTestApp(t, store, "demo", "running", 1)

	var gotTracer oteltrace.Tracer
	var gotParent oteltrace.SpanContext
	srv.SetDeployRunForTest(func(p deploy.Params) (*deploy.PoolResult, error) {
		gotTracer = p.Tracer
		if p.TraceCtx != nil {
			gotParent = oteltrace.SpanContextFromContext(p.TraceCtx)
		}
		return &deploy.PoolResult{Replicas: []deploy.Result{{Index: 0, PID: 1, Port: 20001}}}, nil
	})

	body, ctype := buildMultiFileBundleUpload(t, map[string]string{"app.py": "from shiny import App\n"})
	req := httptest.NewRequest("POST", "/api/apps/demo/deploy", body)
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-ShinyHub-Allow-Downtime", "1")
	rec := httptest.NewRecorder()
	srv.Observe(srv.Router()).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("deploy returned %d: %s", rec.Code, rec.Body.String())
	}

	var server sdktrace.ReadOnlySpan
	phases := map[string]sdktrace.ReadOnlySpan{}
	for _, s := range sr.Ended() {
		switch s.Name() {
		case "POST /api/apps/{slug}/deploy":
			server = s
		case "deploy.receive", "deploy.extract", "deploy.validate":
			if _, dup := phases[s.Name()]; dup {
				t.Fatalf("more than one %s span", s.Name())
			}
			phases[s.Name()] = s
		}
	}
	if server == nil {
		t.Fatal("no server span for the deploy request")
	}
	if gotTracer == nil {
		t.Fatal("deploy.Params.Tracer is nil with tracing enabled")
	}
	if gotParent.SpanID() != server.SpanContext().SpanID() || gotParent.TraceID() != server.SpanContext().TraceID() {
		t.Fatalf("deploy.Params.TraceCtx carries span %v, want the request span %v", gotParent.SpanID(), server.SpanContext().SpanID())
	}
	order := []string{"deploy.receive", "deploy.extract", "deploy.validate"}
	for i, name := range order {
		s, ok := phases[name]
		if !ok {
			t.Fatalf("no %s span; got phases %v", name, phases)
		}
		if s.Parent().SpanID() != server.SpanContext().SpanID() {
			t.Fatalf("%s is not a child of the request span", name)
		}
		if s.Status().Code == codes.Error {
			t.Fatalf("%s ended with error status %q on a successful deploy", name, s.Status().Description)
		}
		if i > 0 {
			prev := phases[order[i-1]]
			if prev.EndTime().After(s.StartTime()) {
				t.Fatalf("%s ended at %v, after %s started at %v", order[i-1], prev.EndTime(), name, s.StartTime())
			}
		}
	}
}

// A failed handler phase exports a reduced copy of its error: the first line
// only, URL userinfo masked, at most spanerr.MaxBytes. The exported text must
// never carry a credential or an unbounded body.
func TestPhase_SpanErrorIsRedactedAndBounded(t *testing.T) {
	const secret = "tok-s3cret"
	srv := New(&config.Config{Auth: config.AuthConfig{Secret: "test-secret"}}, nil, nil, nil)
	tr, sr := recordingTracer(t)
	srv.SetTracer(tr)

	err := errors.New("extract failed: https://user:" + secret + "@idx.example/simple " +
		strings.Repeat("x", 4000) + "\nsecond line " + secret)
	srv.phase(context.Background(), "deploy.extract")(err)

	spans := sr.Ended()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	assertSpanErrorReduced(t, spans[0], secret)
}

// assertSpanErrorReduced fails when span is not in Error status, or when its
// status description or any event attribute carries secret, a newline, or more
// than spanerr.MaxBytes.
func assertSpanErrorReduced(t *testing.T, span sdktrace.ReadOnlySpan, secret string) {
	t.Helper()
	if span.Status().Code != codes.Error {
		t.Fatalf("status = %v, want Error", span.Status().Code)
	}
	texts := []string{span.Status().Description}
	for _, ev := range span.Events() {
		for _, kv := range ev.Attributes {
			texts = append(texts, kv.Value.Emit())
		}
	}
	for _, s := range texts {
		if strings.Contains(s, secret) {
			t.Errorf("secret exported (%d bytes): %.120q", len(s), s)
		}
		if strings.ContainsAny(s, "\r\n") {
			t.Errorf("newline exported (%d bytes): %.120q", len(s), s)
		}
		if len(s) > spanerr.MaxBytes {
			t.Errorf("exported %d bytes, want <= %d", len(s), spanerr.MaxBytes)
		}
	}
}
