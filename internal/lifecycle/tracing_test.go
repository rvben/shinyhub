package lifecycle

import (
	"context"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/process"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
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

	w.OnMiss("app")
	waitNotWaking(t, st, "app")

	if !hasSpanWithSlug(sr, "lifecycle.wake", "app") {
		t.Fatalf("expected a lifecycle.wake span for slug app, got spans %v", spanNames(sr))
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
