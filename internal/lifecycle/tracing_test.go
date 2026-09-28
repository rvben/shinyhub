package lifecycle

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/spanerr"
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

// bootCall is one observed deploy or resume invocation: which path ran, for
// which replica, and what the context it received carried.
type bootCall struct {
	path  string // "deploy" or "resume"
	index int
	span  trace.SpanContext
	err   error // ctx.Err() at call time
}

// bootRecorder captures the context every deploy/resume call receives, so a
// test can prove the replica boots run under the lifecycle span.
type bootRecorder struct {
	mu    sync.Mutex
	calls []bootCall
}

func (r *bootRecorder) fn(path string) func(context.Context, string, string, int) (*deploy.Result, error) {
	return func(ctx context.Context, _, _ string, idx int) (*deploy.Result, error) {
		r.mu.Lock()
		r.calls = append(r.calls, bootCall{path: path, index: idx, span: trace.SpanContextFromContext(ctx), err: ctx.Err()})
		r.mu.Unlock()
		return &deploy.Result{Index: idx, PID: 33 + idx, Port: 20033 + idx}, nil
	}
}

func (r *bootRecorder) snapshot() []bootCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]bootCall(nil), r.calls...)
}

// endedSpan returns the single ended span named name.
func endedSpan(t *testing.T, sr *tracetest.SpanRecorder, name string) sdktrace.ReadOnlySpan {
	t.Helper()
	var found []sdktrace.ReadOnlySpan
	for _, s := range sr.Ended() {
		if s.Name() == name {
			found = append(found, s)
		}
	}
	if len(found) != 1 {
		t.Fatalf("want exactly one %s span, got %d (spans %v)", name, len(found), spanNames(sr))
	}
	return found[0]
}

// assertBootsUnder checks that calls hold exactly the wanted path per replica
// index, each run under parent and none observing a cancellation.
func assertBootsUnder(t *testing.T, calls []bootCall, parent trace.SpanContext, wantPath map[int]string) {
	t.Helper()
	if len(calls) != len(wantPath) {
		t.Fatalf("got %d boot calls %+v, want %d", len(calls), calls, len(wantPath))
	}
	for _, c := range calls {
		if want, ok := wantPath[c.index]; !ok || c.path != want {
			t.Errorf("replica %d booted via %q, want %q", c.index, c.path, wantPath[c.index])
		}
		if !c.span.Equal(parent) {
			t.Errorf("replica %d %s ran under span %v, want the lifecycle span %v", c.index, c.path, c.span, parent)
		}
		if c.err != nil {
			t.Errorf("replica %d %s context is already done: %v", c.index, c.path, c.err)
		}
	}
}

// TestTracing_WakeEmitsSpan proves waking a hibernated app on a proxy miss emits
// a "lifecycle.wake" span tagged with the slug, so cold-start latency is visible
// in the trace backend, and that every replica boot of the wake (both the warm
// resume of a suspended replica and the cold deploy of the other) runs under
// that span, so the deploy.replica spans nest beneath it.
func TestTracing_WakeEmitsSpan(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()), sdktrace.WithSpanProcessor(sr))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	prx := newFakeProxy()
	st := newFakeStore(
		map[string]*db.App{"app": {ID: 1, Slug: "app", Status: "hibernated", Replicas: 2}},
		[]*db.Deployment{{BundleDir: "/bundles/v1"}},
	)
	st.replicas = map[int64][]*db.Replica{
		1: {{AppID: 1, Index: 0, Status: db.ReplicaStatusSuspended, DesiredState: db.ReplicaDesiredWarm}},
	}
	rec := &bootRecorder{}
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, &fakeManager{}, prx, st, rec.fn("deploy"))
	w.SetResume(rec.fn("resume"))
	w.SetTracer(tp.Tracer("test"))

	w.WakeTrigger(context.Background(), "app")
	waitNotWaking(t, st, "app")
	w.wakeWG.Wait()

	if !hasSpanWithSlug(sr, "lifecycle.wake", "app") {
		t.Fatalf("expected a lifecycle.wake span for slug app, got spans %v", spanNames(sr))
	}
	wake := endedSpan(t, sr, "lifecycle.wake")
	assertBootsUnder(t, rec.snapshot(), wake.SpanContext(), map[int]string{0: "resume", 1: "deploy"})
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
		func(_ context.Context, slug, bundleDir string, idx int) (*deploy.Result, error) {
			return &deploy.Result{Index: idx, PID: 33, Port: 20033}, nil
		})
	w.SetTracer(tp.Tracer("test"))
	return w, st, sr
}

// endedWakeSpan returns the single ended lifecycle.wake span.
func endedWakeSpan(t *testing.T, sr *tracetest.SpanRecorder) sdktrace.ReadOnlySpan {
	t.Helper()
	return endedSpan(t, sr, "lifecycle.wake")
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
	rec := &bootRecorder{}
	w.deploy = rec.fn("deploy")

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
	// The replica boot carries the wake span (so deploy.replica nests under
	// it) but never the request's cancellation.
	assertBootsUnder(t, rec.snapshot(), wake.SpanContext(), map[int]string{0: "deploy"})
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
	rec := &bootRecorder{}
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, mgr, newFakeProxy(), st, rec.fn("deploy"))
	w.SetTracer(tp.Tracer("test"))

	w.handleCrashed("myapp", 0)

	if !hasSpanWithSlug(sr, "lifecycle.restart", "myapp") {
		t.Fatalf("expected a lifecycle.restart span for slug myapp, got spans %v", spanNames(sr))
	}
	restart := endedSpan(t, sr, "lifecycle.restart")
	assertBootsUnder(t, rec.snapshot(), restart.SpanContext(), map[int]string{0: "deploy"})
}

// A failed restart boot exports a reduced copy of the boot error on the
// lifecycle.restart span: a build failure wraps the whole uv output and can
// echo a credentialed package-index URL, neither of which may leave the
// process on a span.
func TestTracing_RestartSpanErrorIsRedactedAndBounded(t *testing.T) {
	const secret = "tok-s3cret"
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
	bootErr := errors.New("uv sync: fetch https://user:" + secret + "@idx.example/simple failed " +
		strings.Repeat("x", 4000) + "\nerror: " + secret)
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, mgr, newFakeProxy(), st,
		func(context.Context, string, string, int) (*deploy.Result, error) { return nil, bootErr })
	w.SetTracer(tp.Tracer("test"))

	w.handleCrashed("myapp", 0)

	restart := endedSpan(t, sr, "lifecycle.restart")
	if restart.Status().Code != codes.Error {
		t.Fatalf("status = %v, want Error", restart.Status().Code)
	}
	texts := []string{restart.Status().Description}
	for _, ev := range restart.Events() {
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
		func(_ context.Context, slug, bundleDir string, idx int) (*deploy.Result, error) {
			return &deploy.Result{Index: idx, PID: 33, Port: 20033}, nil
		})
	// No SetTracer: tracer stays nil.
	w.handleCrashed("myapp", 0) // must not panic
}
