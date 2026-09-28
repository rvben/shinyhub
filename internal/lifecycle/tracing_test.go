package lifecycle

import (
	"context"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/process"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func spanNames(sr *tracetest.SpanRecorder) []string {
	var names []string
	for _, s := range sr.Ended() {
		names = append(names, s.Name())
	}
	return names
}

func hasSpanWithSlug(sr *tracetest.SpanRecorder, name, slug string) bool {
	for _, s := range sr.Ended() {
		if s.Name() != name {
			continue
		}
		for _, kv := range s.Attributes() {
			if string(kv.Key) == "shinyhub.app.slug" && kv.Value.AsString() == slug {
				return true
			}
		}
	}
	return false
}

// TestTracing_WakeEmitsSpan proves waking a hibernated app on a proxy miss emits
// a "lifecycle.wake" span tagged with the slug, so cold-start latency is visible
// in the trace backend.
func TestTracing_WakeEmitsSpan(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	prx := newFakeProxy()
	st := newFakeStore(
		map[string]*db.App{"app": {ID: 1, Slug: "app", Status: "hibernated", Replicas: 1}},
		[]*db.Deployment{{BundleDir: "/bundles/v1"}},
	)
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, &fakeManager{}, prx, st,
		func(slug, bundleDir string, idx int) (*deploy.Result, error) {
			return &deploy.Result{Index: idx, PID: 33, Port: 20033}, nil
		})
	w.SetTracer(tp.Tracer("test"))

	w.WakeTrigger(context.Background(), "app")
	waitNotWaking(t, st, "app")
	w.wakeWG.Wait()

	if !hasSpanWithSlug(sr, "lifecycle.wake", "app") {
		t.Fatalf("expected a lifecycle.wake span for slug app, got spans %v", spanNames(sr))
	}
}

// newTracedWakeWatcher returns a watcher with a recording tracer and one app
// "demo" in the given status whose deploy always succeeds.
func newTracedWakeWatcher(t *testing.T, status string) (*Watcher, *fakeStore, *tracetest.SpanRecorder) {
	t.Helper()
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()), sdktrace.WithSpanProcessor(sr))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	st := newFakeStore(
		map[string]*db.App{"demo": {ID: 1, Slug: "demo", Status: status, Replicas: 1}},
		[]*db.Deployment{{BundleDir: "/bundles/v1"}},
	)
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, &fakeManager{}, newFakeProxy(), st,
		func(slug, bundleDir string, idx int) (*deploy.Result, error) {
			return &deploy.Result{Index: idx, PID: 33, Port: 20033}, nil
		})
	w.SetTracer(tp.Tracer("test"))
	return w, st, sr
}

// endedWakeSpan returns the single ended lifecycle.wake span.
func endedWakeSpan(t *testing.T, sr *tracetest.SpanRecorder) sdktrace.ReadOnlySpan {
	t.Helper()
	var found []sdktrace.ReadOnlySpan
	for _, s := range sr.Ended() {
		if s.Name() == "lifecycle.wake" {
			found = append(found, s)
		}
	}
	if len(found) != 1 {
		t.Fatalf("want exactly one lifecycle.wake span, got %d (spans %v)", len(found), spanNames(sr))
	}
	return found[0]
}

func wakeTriggerAttr(s sdktrace.ReadOnlySpan) string {
	for _, kv := range s.Attributes() {
		if string(kv.Key) == "shinyhub.wake.trigger" {
			return kv.Value.AsString()
		}
	}
	return ""
}

// TestTracing_RequestWakeIsChildOfTriggeringSpan proves a request-driven wake
// is nested under the span of the request that triggered it, and that the
// request's cancellation (the client leaving before the cold start finishes)
// does not reach the wake.
func TestTracing_RequestWakeIsChildOfTriggeringSpan(t *testing.T) {
	w, st, sr := newTracedWakeWatcher(t, "hibernated")

	reqTP := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()))
	t.Cleanup(func() { _ = reqTP.Shutdown(context.Background()) })
	reqCtx, reqSpan := reqTP.Tracer("t").Start(context.Background(), "GET /app/{slug}")
	cancelled, cancel := context.WithCancel(reqCtx)
	cancel() // the request is gone before the wake finishes

	w.WakeTrigger(cancelled, "demo")
	waitNotWaking(t, st, "demo")
	w.wakeWG.Wait()
	reqSpan.End()

	wake := endedWakeSpan(t, sr)
	want := reqSpan.SpanContext()
	if wake.Parent().SpanID() != want.SpanID() || wake.Parent().TraceID() != want.TraceID() {
		t.Fatalf("wake parent %v, want request span %v", wake.Parent(), want)
	}
	if got := wakeTriggerAttr(wake); got != "request" {
		t.Fatalf("shinyhub.wake.trigger = %q, want request", got)
	}
	if wake.Status().Code == codes.Error {
		t.Fatalf("wake must not inherit the request's cancellation: %v", wake.Status())
	}
	st.mu.Lock()
	status := st.apps["demo"].Status
	st.mu.Unlock()
	if status != "running" {
		t.Fatalf("app status after wake = %q, want running", status)
	}
}

// TestSpanParent_KeepsSpanDropsCancellation pins the context a request-driven
// wake runs under: the request's span context survives, while its cancellation
// and deadline do not, so work the wake does with that context later is never
// cut short by the client leaving.
func TestSpanParent_KeepsSpanDropsCancellation(t *testing.T) {
	tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	reqCtx, reqSpan := tp.Tracer("t").Start(context.Background(), "GET /app/{slug}")
	defer reqSpan.End()
	deadlined, cancelDeadline := context.WithTimeout(reqCtx, time.Hour)
	defer cancelDeadline()
	cancelled, cancel := context.WithCancel(deadlined)
	cancel()

	got := spanParent(cancelled)

	if sc := trace.SpanContextFromContext(got); !sc.Equal(reqSpan.SpanContext()) {
		t.Fatalf("span context %v, want %v", sc, reqSpan.SpanContext())
	}
	if err := got.Err(); err != nil {
		t.Fatalf("spanParent kept the request's cancellation: %v", err)
	}
	if got.Done() != nil {
		t.Fatal("spanParent context can still be cancelled")
	}
	if _, ok := got.Deadline(); ok {
		t.Fatal("spanParent kept the request's deadline")
	}
}

// TestTracing_ReconcileWakeIsRoot proves a wake driven by the owner's reconcile
// tick (no request involved) starts its own trace.
func TestTracing_ReconcileWakeIsRoot(t *testing.T) {
	w, st, sr := newTracedWakeWatcher(t, "waking")

	w.runOnce()
	waitNotWaking(t, st, "demo")
	w.wakeWG.Wait()

	wake := endedWakeSpan(t, sr)
	if wake.Parent().IsValid() {
		t.Fatalf("reconcile wake has parent %v, want a root span", wake.Parent())
	}
	if got := wakeTriggerAttr(wake); got != "reconcile" {
		t.Fatalf("shinyhub.wake.trigger = %q, want reconcile", got)
	}
}

// TestTracing_RestartEmitsSpan proves a successful crash-restart emits a
// "lifecycle.restart" span tagged with the slug.
func TestTracing_RestartEmitsSpan(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	mgr := &fakeManager{entries: []*process.ProcessInfo{
		{Slug: "myapp", Index: 0, Status: process.StatusCrashed},
	}}
	st := newFakeStore(
		map[string]*db.App{"myapp": {ID: 1, Slug: "myapp", Status: "running", Replicas: 1}},
		[]*db.Deployment{{BundleDir: "/bundles/v1"}},
	)
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, mgr, newFakeProxy(), st,
		func(slug, bundleDir string, idx int) (*deploy.Result, error) {
			return &deploy.Result{Index: idx, PID: 33, Port: 20033}, nil
		})
	w.SetTracer(tp.Tracer("test"))

	w.handleCrashed("myapp", 0)

	if !hasSpanWithSlug(sr, "lifecycle.restart", "myapp") {
		t.Fatalf("expected a lifecycle.restart span for slug myapp, got spans %v", spanNames(sr))
	}
}

// TestTracing_NilTracerIsSafe proves the watcher tolerates an unset tracer
// (tracing disabled) without panicking on the background paths.
func TestTracing_NilTracerIsSafe(t *testing.T) {
	mgr := &fakeManager{entries: []*process.ProcessInfo{
		{Slug: "myapp", Index: 0, Status: process.StatusCrashed},
	}}
	st := newFakeStore(
		map[string]*db.App{"myapp": {ID: 1, Slug: "myapp", Status: "running", Replicas: 1}},
		[]*db.Deployment{{BundleDir: "/bundles/v1"}},
	)
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, mgr, newFakeProxy(), st,
		func(slug, bundleDir string, idx int) (*deploy.Result, error) {
			return &deploy.Result{Index: idx, PID: 33, Port: 20033}, nil
		})
	// No SetTracer: tracer stays nil.
	w.handleCrashed("myapp", 0) // must not panic
}
