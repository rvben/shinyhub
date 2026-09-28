package proxy_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/proxy"
	"github.com/rvben/shinyhub/internal/tracing"
)

// wakeTriggerSite arranges p so that a GET /app/demo/ reaches one specific wake
// trigger site. It returns a cleanup that unblocks anything it pinned.
type wakeTriggerSite struct {
	name  string
	setup func(t *testing.T, p *proxy.Proxy) (cleanup func())
}

func wakeTriggerSites() []wakeTriggerSite {
	return []wakeTriggerSite{
		{
			// A miss on an empty pool: holdForWake fires the trigger.
			name: "hold_miss",
			setup: func(t *testing.T, p *proxy.Proxy) func() {
				p.SetWakeHoldTimeout(0)
				p.SetPoolSize("demo", 1)
				return func() {}
			},
		},
		{
			// A pre-response upstream error: serveMissPage fires the trigger.
			name: "upstream_error",
			setup: func(t *testing.T, p *proxy.Proxy) func() {
				closed := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
				url := closed.URL
				closed.Close()
				if err := p.Register("demo", url); err != nil {
					t.Fatal(err)
				}
				return func() {}
			},
		},
		{
			// Every live replica is draining: the pool-degraded shed fires it.
			name: "all_draining",
			setup: func(t *testing.T, p *proxy.Proxy) func() {
				b := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
				p.SetPoolSize("demo", 1)
				if err := p.RegisterReplica("demo", 0, b.URL, nil, 0); err != nil {
					t.Fatal(err)
				}
				if !p.DrainReplica("demo", 0) {
					t.Fatal("DrainReplica returned false for a live slot")
				}
				return b.Close
			},
		},
		{
			// Fewer live replicas than configured, all at cap: the degraded
			// saturation shed fires it.
			name: "degraded_at_cap",
			setup: func(t *testing.T, p *proxy.Proxy) func() {
				release := make(chan struct{})
				pin := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
				p.SetPoolSize("demo", 2)
				p.SetPoolCap("demo", 1)
				if err := p.RegisterReplica("demo", 0, pin.URL, nil, 0); err != nil {
					t.Fatal(err)
				}
				var wg sync.WaitGroup
				wg.Add(1)
				go func() {
					defer wg.Done()
					p.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/app/demo/", nil))
				}()
				waitForCount(p, "demo", func(c []int64) bool { return len(c) > 0 && c[0] >= 1 })
				return func() {
					close(release)
					wg.Wait()
					pin.Close()
				}
			},
		},
	}
}

// requestValueKey marks a value fireWake places on the request context, to show
// what of the request reaches the wake trigger.
type requestValueKey struct{}

// fireWake sends one GET /app/demo/ through p with a cancellable request
// context carrying a requestValueKey value, cancels that context once the
// response is written (the client is gone), and returns the context the wake
// trigger received.
func fireWake(t *testing.T, p *proxy.Proxy) context.Context {
	t.Helper()
	got := make(chan context.Context, 8)
	p.SetWakeTrigger(func(ctx context.Context, slug string) {
		if slug == "demo" {
			got <- ctx
		}
	})
	reqCtx, cancel := context.WithCancel(context.WithValue(context.Background(), requestValueKey{}, "request-scoped"))
	req := httptest.NewRequest(http.MethodGet, "/app/demo/", nil).WithContext(reqCtx)
	p.ServeHTTP(httptest.NewRecorder(), req)
	cancel()
	select {
	case ctx := <-got:
		return ctx
	case <-time.After(2 * time.Second):
		t.Fatal("wake trigger did not fire")
		return nil
	}
}

// TestProxy_WakeTriggerCarriesProxySpan proves every request-driven wake
// trigger site hands the watcher a context positioned under the request's
// exported /app span, and that the context outlives the request: the wake keeps
// running after the client has gone.
func TestProxy_WakeTriggerCarriesProxySpan(t *testing.T) {
	for _, site := range wakeTriggerSites() {
		t.Run(site.name, func(t *testing.T) {
			p := proxy.New()
			p.SetTracing(config.TracingConfig{Enabled: true, SampleRatio: 1}, tracing.NewBuffer(10, time.Hour))
			rec := tracetest.NewSpanRecorder()
			tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()), sdktrace.WithSpanProcessor(rec))
			t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
			p.SetSpanTracer(tp.Tracer("test"))
			cleanup := site.setup(t, p)
			defer cleanup()
			// Spans from any pinning request in setup are not the one under test.
			before := len(rec.Ended())

			ctx := fireWake(t, p)

			ended := rec.Ended()[before:]
			if len(ended) != 1 {
				t.Fatalf("want exactly one proxy span for the request, got %d", len(ended))
			}
			proxySpan := ended[0].SpanContext()
			got := trace.SpanContextFromContext(ctx)
			if !got.IsValid() {
				t.Fatal("wake trigger context carries no span context")
			}
			if got.TraceID() != proxySpan.TraceID() || got.SpanID() != proxySpan.SpanID() {
				t.Fatalf("wake trigger span context %v, want the proxy span %v", got, proxySpan)
			}
			if err := ctx.Err(); err != nil {
				t.Fatalf("wake trigger context inherited the request's cancellation: %v", err)
			}
			if v := ctx.Value(requestValueKey{}); v != nil {
				t.Fatalf("wake trigger context retains a request-scoped value %v; it must carry only the span context", v)
			}
		})
	}
}

// TestProxy_WakeTriggerWithoutTracerHasNoSpan proves that with no SDK tracer
// wired the trigger still fires with a live, span-free context, so the watcher
// starts a root span (or none) exactly as before.
func TestProxy_WakeTriggerWithoutTracerHasNoSpan(t *testing.T) {
	for _, site := range wakeTriggerSites() {
		t.Run(site.name, func(t *testing.T) {
			p := proxy.New()
			cleanup := site.setup(t, p)
			defer cleanup()

			ctx := fireWake(t, p)

			if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
				t.Fatalf("wake trigger context carries a span context %v with no tracer wired", sc)
			}
			if err := ctx.Err(); err != nil {
				t.Fatalf("wake trigger context inherited the request's cancellation: %v", err)
			}
			if v := ctx.Value(requestValueKey{}); v != nil {
				t.Fatalf("wake trigger context retains a request-scoped value %v", v)
			}
		})
	}
}
