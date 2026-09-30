package proxy_test

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/proxy"
	"github.com/rvben/shinyhub/internal/spanerr"
	"github.com/rvben/shinyhub/internal/tracing"
)

// bothTracingPaths runs fn once on the dependency-free span path and once with
// an SDK tracer wired, so every propagation and ring buffer contract holds on
// both. The recorder is nil on the path without a tracer.
func bothTracingPaths(t *testing.T, fn func(t *testing.T, wire func(*proxy.Proxy) *tracetest.SpanRecorder)) {
	for _, withTracer := range []bool{false, true} {
		name := "fallback"
		if withTracer {
			name = "sdk_tracer"
		}
		t.Run(name, func(t *testing.T) {
			fn(t, func(p *proxy.Proxy) *tracetest.SpanRecorder {
				if !withTracer {
					return nil
				}
				rec := tracetest.NewSpanRecorder()
				tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()), sdktrace.WithSpanProcessor(rec))
				t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
				p.SetSpanTracer(tp.Tracer("test"))
				return rec
			})
		})
	}
}

func attrMap(kvs []attribute.KeyValue) map[string]string {
	out := make(map[string]string, len(kvs))
	for _, kv := range kvs {
		out[string(kv.Key)] = kv.Value.Emit()
	}
	return out
}

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

// TRC-4: the W3C tracestate header carries vendor context (Datadog, Honeycomb,
// etc.). When ShinyHub continues an incoming trace it must propagate tracestate
// downstream unchanged, otherwise the vendor span graph breaks at the proxy hop.
func TestProxy_PropagatesTracestate(t *testing.T) {
	bothTracingPaths(t, func(t *testing.T, wire func(*proxy.Proxy) *tracetest.SpanRecorder) {
		var gotState string
		backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotState = r.Header.Get("tracestate")
		}))
		defer backend.Close()

		p := proxy.New()
		if err := p.Register("app", backend.URL); err != nil {
			t.Fatal(err)
		}
		buf := tracing.NewBuffer(10, time.Second)
		p.SetTracing(config.TracingConfig{Enabled: true, SampleRatio: 1}, buf)
		wire(p)

		req := httptest.NewRequest("GET", "/app/app/", nil)
		req.Header.Set("traceparent", "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01")
		req.Header.Set("tracestate", "dd=s:1;o:rum,congo=t61rcWkgMzE")
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, req)

		if gotState != "dd=s:1;o:rum,congo=t61rcWkgMzE" {
			t.Errorf("tracestate not propagated downstream: got %q", gotState)
		}
	})
}

// TRC-4: tracestate is a W3C list header that a client or edge proxy MAY split
// across multiple header field-values. A receiver must combine them; reading
// only the first (Header.Get) silently drops every vendor entry after the first
// split. ShinyHub must join all inbound Tracestate values before propagating.
func TestProxy_CombinesSplitTracestate(t *testing.T) {
	bothTracingPaths(t, func(t *testing.T, wire func(*proxy.Proxy) *tracetest.SpanRecorder) {
		var gotState string
		backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotState = r.Header.Get("tracestate")
		}))
		defer backend.Close()

		p := proxy.New()
		if err := p.Register("app", backend.URL); err != nil {
			t.Fatal(err)
		}
		buf := tracing.NewBuffer(10, time.Second)
		p.SetTracing(config.TracingConfig{Enabled: true, SampleRatio: 1}, buf)
		wire(p)

		req := httptest.NewRequest("GET", "/app/app/", nil)
		req.Header.Set("traceparent", "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01")
		req.Header.Add("tracestate", "dd=s:1;o:rum")
		req.Header.Add("tracestate", "congo=t61rcWkgMzE")
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, req)

		want := "dd=s:1;o:rum,congo=t61rcWkgMzE"
		if gotState != want {
			t.Errorf("split tracestate not combined downstream: got %q, want %q", gotState, want)
		}
	})
}

// TRC-4: when no valid upstream traceparent is present a fresh trace is started,
// so any inbound tracestate references a different (or no) trace and MUST NOT be
// forwarded — that would attach stale vendor context to a brand-new trace.
func TestProxy_DropsTracestateOnNewTrace(t *testing.T) {
	bothTracingPaths(t, func(t *testing.T, wire func(*proxy.Proxy) *tracetest.SpanRecorder) {
		var gotState string
		var sawHeader bool
		backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotState = r.Header.Get("tracestate")
			_, sawHeader = r.Header["Tracestate"]
		}))
		defer backend.Close()

		p := proxy.New()
		if err := p.Register("app", backend.URL); err != nil {
			t.Fatal(err)
		}
		buf := tracing.NewBuffer(10, time.Second)
		p.SetTracing(config.TracingConfig{Enabled: true, SampleRatio: 1}, buf)
		wire(p)

		req := httptest.NewRequest("GET", "/app/app/", nil)
		// No traceparent: a new trace is started. The stray tracestate must be dropped.
		req.Header.Set("tracestate", "dd=s:1;o:rum")
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, req)

		if sawHeader || gotState != "" {
			t.Errorf("stale tracestate must be dropped on a new trace, got %q (present=%v)", gotState, sawHeader)
		}
	})
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

// TRC-1: with tracing enabled the transport wraps upstream response bodies to
// capture mid-stream read errors. It must NOT wrap a 101 Switching Protocols
// body: Go's httputil.ReverseProxy requires that body to implement
// io.ReadWriteCloser before it can tunnel the WebSocket. A wrapper that exposes
// only io.ReadCloser makes the upgrade fail (502 via ErrorHandler), breaking
// Shiny/Streamlit apps whenever tracing is on. This drives a raw WS-style
// upgrade through the proxy and asserts the handshake and byte tunnel succeed.
func TestProxy_TracedWebSocketUpgradeSucceeds(t *testing.T) {
	bothTracingPaths(t, func(t *testing.T, wire func(*proxy.Proxy) *tracetest.SpanRecorder) {
		backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
				http.Error(w, "expected websocket upgrade", http.StatusBadRequest)
				return
			}
			hj, ok := w.(http.Hijacker)
			if !ok {
				http.Error(w, "no hijack", http.StatusInternalServerError)
				return
			}
			conn, brw, err := hj.Hijack()
			if err != nil {
				return
			}
			defer conn.Close()
			time.Sleep(10 * time.Millisecond) // make handshake admission to the slow buffer deterministic
			_, _ = brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
			_ = brw.Flush()
			// Echo one client line back so the test confirms the tunnel is live.
			line, err := brw.ReadString('\n')
			if err != nil {
				return
			}
			_, _ = brw.WriteString("echo:" + line)
			_ = brw.Flush()
		}))
		defer backend.Close()

		p := proxy.New()
		if err := p.Register("app", backend.URL); err != nil {
			t.Fatal(err)
		}
		buf := tracing.NewBuffer(10, time.Millisecond)
		p.SetTracing(config.TracingConfig{Enabled: true, SampleRatio: 1}, buf)
		spans := wire(p)

		front := httptest.NewServer(p)
		defer front.Close()

		conn, err := net.Dial("tcp", strings.TrimPrefix(front.URL, "http://"))
		if err != nil {
			t.Fatalf("dial frontend: %v", err)
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

		fmt.Fprintf(conn, "GET /app/app/ws HTTP/1.1\r\nHost: example\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		r := bufio.NewReader(conn)
		statusLine, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("read status line: %v", err)
		}
		if !strings.Contains(statusLine, "101") {
			t.Fatalf("traced WebSocket upgrade failed: status line = %q (want 101)", strings.TrimSpace(statusLine))
		}
		// Drain the rest of the response headers.
		for {
			l, err := r.ReadString('\n')
			if err != nil {
				t.Fatalf("read headers: %v", err)
			}
			if strings.TrimSpace(l) == "" {
				break
			}
		}
		// The HTTP span must export while the backend is still waiting for
		// the first message; the session span must remain open.
		if spans != nil {
			deadline := time.Now().Add(5 * time.Second)
			for len(spans.Ended()) == 0 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			ended := spans.Ended()
			if len(ended) != 1 || ended[0].Name() != "GET /app/{slug}" {
				t.Fatalf("HTTP handshake must end before the session: %v", ended)
			}
			if got := attrMap(ended[0].Attributes())["http.response.status_code"]; got != "101" {
				t.Fatalf("handshake status = %s", got)
			}
		}
		buffered := buf.Snapshot("app")
		for deadline := time.Now().Add(5 * time.Second); len(buffered) == 0 && time.Now().Before(deadline); {
			time.Sleep(time.Millisecond)
			buffered = buf.Snapshot("app")
		}
		if len(buffered) != 1 || buffered[0].Status != 101 {
			t.Fatalf("handshake must reach the buffer before the session closes: %v", buffered)
		}
		// Confirm the byte tunnel actually carries traffic both ways.
		fmt.Fprintf(conn, "hello\n")
		echo, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("read echo: %v", err)
		}
		if strings.TrimSpace(echo) != "echo:hello" {
			t.Fatalf("tunnel echo = %q, want %q", strings.TrimSpace(echo), "echo:hello")
		}
		if spans == nil {
			return
		}
		// The backend closes the tunnel after the echo, ending the session.
		deadline := time.Now().Add(5 * time.Second)
		for len(spans.Ended()) < 2 && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		ended := spans.Ended()
		if len(ended) != 2 {
			t.Fatalf("want handshake and session spans, got %d", len(ended))
		}
		httpSpan, sessionSpan := ended[0], ended[1]
		if sessionSpan.Name() != "WS /app/{slug}" || sessionSpan.SpanKind() != trace.SpanKindInternal {
			t.Fatalf("unexpected session span: %s (%s)", sessionSpan.Name(), sessionSpan.SpanKind())
		}
		if sessionSpan.Parent().SpanID() != httpSpan.SpanContext().SpanID() || sessionSpan.SpanContext().TraceID() != httpSpan.SpanContext().TraceID() {
			t.Fatal("session must be a child of the handshake in the same trace")
		}
		attrs := attrMap(sessionSpan.Attributes())
		if _, ok := attrs["http.route"]; ok {
			t.Fatal("session must not contribute to HTTP route latency")
		}
		if attrs["shinyhub.ws.bytes_to_client"] == "0" || attrs["shinyhub.ws.bytes_to_upstream"] == "0" {
			t.Fatalf("missing tunnel byte counts: %v", attrs)
		}
		if got := buf.Snapshot("app"); len(got) != 1 || got[0].DurationMS != buffered[0].DurationMS {
			t.Fatalf("session close must not overwrite or duplicate the HTTP buffer entry: %v", got)
		}
	})
}

// TestProxy_RecordsErrorToBuffer verifies that a 5xx response is admitted to
// the ring buffer with method, path, status, and replica information.
func TestProxy_RecordsErrorToBuffer(t *testing.T) {
	bothTracingPaths(t, func(t *testing.T, wire func(*proxy.Proxy) *tracetest.SpanRecorder) {
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
		wire(p)

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
	})
}

func tracedProxy(t *testing.T, sampler sdktrace.Sampler) (*proxy.Proxy, *tracetest.SpanRecorder, *httptest.Server, *atomic.Value) {
	t.Helper()
	got := &atomic.Value{}
	got.Store("")
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Store(r.Header.Get("traceparent"))
	}))
	t.Cleanup(backend.Close)
	p := proxy.New()
	if err := p.Register("demo", backend.URL); err != nil {
		t.Fatal(err)
	}
	p.SetTracing(config.TracingConfig{Enabled: true, SampleRatio: 1}, tracing.NewBuffer(10, time.Hour))
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(sampler), sdktrace.WithSpanProcessor(rec))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	p.SetSpanTracer(tp.Tracer("test"))
	return p, rec, backend, got
}

func TestProxy_ExportsSpanWhoseIDsTheAppReceives(t *testing.T) {
	p, rec, _, got := tracedProxy(t, sdktrace.AlwaysSample())
	req := httptest.NewRequest("GET", "/app/demo/page?x=1", nil)
	upstream := "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"
	req.Header.Set("traceparent", upstream)
	p.ServeHTTP(httptest.NewRecorder(), req)

	spans := rec.Ended()
	if len(spans) != 1 {
		t.Fatalf("want 1 span, got %d", len(spans))
	}
	s := spans[0]
	tp, ok := tracing.ParseTraceparent(got.Load().(string))
	if !ok || trace.TraceID(tp.TraceID) != s.SpanContext().TraceID() || trace.SpanID(tp.SpanID) != s.SpanContext().SpanID() {
		t.Fatalf("app got %q, exported %s/%s", got.Load(), s.SpanContext().TraceID(), s.SpanContext().SpanID())
	}
	if s.Parent().SpanID().String() != "b7ad6b7169203331" || !s.Parent().IsRemote() {
		t.Fatalf("parent: %v", s.Parent())
	}
	if s.Name() != "GET /app/{slug}" || s.SpanKind() != trace.SpanKindServer {
		t.Fatalf("name/kind: %s %v", s.Name(), s.SpanKind())
	}
	a := attrMap(s.Attributes())
	if a["shinyhub.app.slug"] != "demo" || a["url.path"] != "/page" || a["http.response.status_code"] != "200" || a["shinyhub.replica"] != "0" {
		t.Fatalf("attrs: %v", a)
	}
}

// Review Focus 2: the flag the app sees equals whether the span was exported.
func TestProxy_TraceparentFlagMatchesExportDecision(t *testing.T) {
	for _, tc := range []struct {
		sampler sdktrace.Sampler
		flag    string
		n       int
	}{{sdktrace.NeverSample(), "00", 0}, {sdktrace.AlwaysSample(), "01", 1}} {
		p, rec, _, got := tracedProxy(t, sdktrace.ParentBased(tc.sampler))
		p.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/app/demo/", nil))
		if !strings.HasSuffix(got.Load().(string), "-"+tc.flag) || len(rec.Ended()) != tc.n {
			t.Fatalf("flag %q spans %d, want %s/%d", got.Load(), len(rec.Ended()), tc.flag, tc.n)
		}
	}
}

func TestProxy_SpanMarksUpstreamErrors(t *testing.T) {
	// A dead backend does NOT produce a 502: the ErrorHandler serves the
	// loading page with 200 so the client retries while the wake runs. The
	// span must still carry the transport error, recorded via proxyErr.
	p, rec, backend, _ := tracedProxy(t, sdktrace.AlwaysSample())
	backend.Close()
	resp := httptest.NewRecorder()
	p.ServeHTTP(resp, httptest.NewRequest("GET", "/app/demo/page", nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("client status = %d, want 200 (loading page)", resp.Code)
	}
	spans := rec.Ended()
	if len(spans) != 1 {
		t.Fatalf("want 1 span, got %d", len(spans))
	}
	st := spans[0].Status()
	if st.Code != codes.Error || !strings.Contains(st.Description, "connect") {
		t.Fatalf("span status = %v %q, want Error mentioning the dial failure", st.Code, st.Description)
	}
}

// failingTransport fails every round trip with err, standing in for a replica
// transport whose errors carry arbitrary text.
type failingTransport struct{ err error }

func (f failingTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, f.err }

// The exported span carries a reduced copy of the upstream error: a replica
// transport error can quote a URL with credentials and run long, and neither
// may leave the process on a span.
func TestProxy_SpanUpstreamErrorIsRedactedAndBounded(t *testing.T) {
	const secret = "tok-s3cret"
	p := proxy.New()
	p.SetPoolSize("demo", 1)
	transportErr := fmt.Errorf("dial via https://svc:%s@gw.example/replica: %s\ndetail: %s",
		secret, strings.Repeat("x", 4000), secret)
	if err := p.RegisterReplica("demo", 0, "http://127.0.0.1:1", failingTransport{err: transportErr}, 0); err != nil {
		t.Fatal(err)
	}
	p.SetTracing(config.TracingConfig{Enabled: true, SampleRatio: 1}, tracing.NewBuffer(10, time.Hour))
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()), sdktrace.WithSpanProcessor(rec))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	p.SetSpanTracer(tp.Tracer("test"))

	p.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/app/demo/page", nil))

	spans := rec.Ended()
	if len(spans) != 1 {
		t.Fatalf("want 1 span, got %d", len(spans))
	}
	st := spans[0].Status()
	if st.Code != codes.Error || !strings.Contains(st.Description, "dial via") {
		t.Fatalf("status code %v, want Error carrying the transport error: %.120q", st.Code, st.Description)
	}
	if strings.Contains(st.Description, secret) {
		t.Errorf("secret exported (%d bytes): %.120q", len(st.Description), st.Description)
	}
	if strings.ContainsAny(st.Description, "\r\n") {
		t.Errorf("newline exported (%d bytes): %.120q", len(st.Description), st.Description)
	}
	if len(st.Description) > spanerr.MaxBytes {
		t.Errorf("exported %d bytes, want <= %d", len(st.Description), spanerr.MaxBytes)
	}
}

func TestProxy_SpanMarks5xxAsError(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer backend.Close()
	p := proxy.New()
	if err := p.Register("demo", backend.URL); err != nil {
		t.Fatal(err)
	}
	p.SetTracing(config.TracingConfig{Enabled: true, SampleRatio: 1}, tracing.NewBuffer(10, time.Hour))
	rec := tracetest.NewSpanRecorder()
	p.SetSpanTracer(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)).Tracer("test"))
	p.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/app/demo/", nil))
	spans := rec.Ended()
	if len(spans) != 1 || spans[0].Status().Code != codes.Error {
		t.Fatalf("want 1 Error span, got %d", len(spans))
	}
	if a := attrMap(spans[0].Attributes()); a["http.response.status_code"] != "502" {
		t.Fatalf("status attr = %q", a["http.response.status_code"])
	}
}
