package deploy_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
	"github.com/rvben/shinyhub/internal/spanerr"
)

// phaseRecorder returns a recorder behind an always-sampling provider, so every
// span a phase opens is observable.
func phaseRecorder(t *testing.T) (*tracetest.SpanRecorder, trace.Tracer) {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()), sdktrace.WithSpanProcessor(rec))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	return rec, tp.Tracer("t")
}

// poolParamsWithHook builds a native-runtime Python deploy whose dependency
// build, launch command and post-deploy hook are all faked, so Run walks every
// phase (build, hooks, per-replica start and health) without uv or a real app.
func poolParamsWithHook(t *testing.T, slug string, replicas int, hook deploy.HookRunnerFunc) deploy.Params {
	t.Helper()
	bundle := t.TempDir()
	files := map[string]string{
		"app.py":                "# app",
		"pyproject.toml":        "[project]\nname = \"demo\"\n",
		deploy.ManifestFilename: "[[hook]]\non = \"post-deploy\"\ncommand = [\"python\", \"-m\", \"scripts.migrate\"]\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(bundle, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(deploy.SetSyncHooksForTest(
		func(context.Context, string, []string) error { return nil },
		func(context.Context, string, []string) error { return nil },
	))
	t.Cleanup(deploy.SetEnsureProjectForTest(func(context.Context, string) error { return nil }))
	t.Cleanup(deploy.SetBuildCommandForTest(func(string, int, int, string, []string, bool) []string {
		return []string{"sleep", "30"}
	}))
	if hook == nil {
		hook = func(context.Context, string, []string, []string, io.Writer) error { return nil }
	}
	t.Cleanup(deploy.SetHookRunnerForTest(hook))

	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	t.Cleanup(func() { _ = mgr.Stop(slug) })
	return deploy.Params{
		Slug: slug, BundleDir: bundle, Replicas: replicas,
		Manager: mgr, Proxy: proxy.New(),
		HealthCheck: func(string, time.Duration, http.RoundTripper) error { return nil },
	}
}

func spansByName(spans []sdktrace.ReadOnlySpan) map[string][]sdktrace.ReadOnlySpan {
	out := map[string][]sdktrace.ReadOnlySpan{}
	for _, s := range spans {
		out[s.Name()] = append(out[s.Name()], s)
	}
	return out
}

func one(t *testing.T, byName map[string][]sdktrace.ReadOnlySpan, name string) sdktrace.ReadOnlySpan {
	t.Helper()
	if n := len(byName[name]); n != 1 {
		t.Fatalf("want exactly one %q span, got %d", name, n)
	}
	return byName[name][0]
}

func childrenOf(spans []sdktrace.ReadOnlySpan, parent sdktrace.ReadOnlySpan) []string {
	var out []string
	for _, s := range spans {
		if s.Parent().SpanID() == parent.SpanContext().SpanID() {
			out = append(out, s.Name())
		}
	}
	return out
}

func spanAttr(s sdktrace.ReadOnlySpan, key string) (attribute.Value, bool) {
	for _, kv := range s.Attributes() {
		if string(kv.Key) == key {
			return kv.Value, true
		}
	}
	return attribute.Value{}, false
}

func TestRun_EmitsPhaseSpansUnderTraceCtx(t *testing.T) {
	rec, tr := phaseRecorder(t)
	parentCtx, parent := tr.Start(context.Background(), "POST /api/apps/{slug}/deploy")
	p := poolParamsWithHook(t, "phase-spans", 2, nil)
	p.Tracer, p.TraceCtx = tr, parentCtx
	if _, err := deploy.Run(p); err != nil {
		t.Fatal(err)
	}
	parent.End()

	ended := rec.Ended()
	byName := spansByName(ended)
	run := one(t, byName, "deploy.run")
	if run.Parent().SpanID() != parent.SpanContext().SpanID() {
		t.Fatal("deploy.run must be a child of the request span")
	}
	if run.SpanContext().TraceID() != parent.SpanContext().TraceID() {
		t.Fatal("deploy.run must share the request's trace")
	}
	if v, ok := spanAttr(run, "shinyhub.deploy.replicas"); !ok || v.AsInt64() != 2 {
		t.Fatalf("deploy.run shinyhub.deploy.replicas = %v (present %v), want 2", v.AsInt64(), ok)
	}
	for _, name := range []string{"deploy.build", "deploy.post_deploy_hooks"} {
		if one(t, byName, name).Parent().SpanID() != run.SpanContext().SpanID() {
			t.Fatalf("%s must be a child of deploy.run", name)
		}
	}
	build := one(t, byName, "deploy.build")
	if v, _ := spanAttr(build, "shinyhub.deploy.build_tool"); v.AsString() != "uv sync" {
		t.Fatalf("deploy.build build_tool = %q, want uv sync", v.AsString())
	}
	hooks := one(t, byName, "deploy.post_deploy_hooks")
	for key, want := range map[string]int64{"shinyhub.deploy.hooks.declared": 1, "shinyhub.deploy.hooks.run": 1, "shinyhub.deploy.hooks.skipped": 0} {
		if v, ok := spanAttr(hooks, key); !ok || v.AsInt64() != want {
			t.Fatalf("%s = %v (present %v), want %d", key, v.AsInt64(), ok, want)
		}
	}
	if n := len(byName["deploy.replica"]); n != 2 {
		t.Fatalf("want 2 replica spans, got %d", n)
	}
	for _, r := range byName["deploy.replica"] {
		if r.Parent().SpanID() != run.SpanContext().SpanID() {
			t.Fatal("deploy.replica must be a child of deploy.run")
		}
		kids := childrenOf(ended, r)
		if !slices.Contains(kids, "deploy.replica.start") || !slices.Contains(kids, "deploy.replica.health") {
			t.Fatalf("replica children: %v", kids)
		}
	}
	// The build and the hooks both finish before any replica starts: two
	// bounds, so a phase moved after the boots fails this check.
	for _, s := range byName["deploy.replica.start"] {
		for _, before := range []string{"deploy.build", "deploy.post_deploy_hooks"} {
			b := one(t, byName, before)
			if b.EndTime().After(s.StartTime()) {
				t.Fatalf("replica started before %s ended", before)
			}
		}
		if s.EndTime().After(run.EndTime()) {
			t.Fatal("replica start outlived deploy.run")
		}
	}
}

func TestRun_HookFailureMarksSpanError(t *testing.T) {
	rec, tr := phaseRecorder(t)
	p := poolParamsWithHook(t, "phase-hook-fail", 1, func(context.Context, string, []string, []string, io.Writer) error {
		return errors.New("migration crashed")
	})
	p.Tracer = tr
	p.HealthCheck = func(string, time.Duration, http.RoundTripper) error {
		t.Error("replica boot should not be reached when the post-deploy hook fails")
		return nil
	}
	if _, err := deploy.Run(p); err == nil {
		t.Fatal("Run must fail when the hook fails")
	}
	byName := spansByName(rec.Ended())
	for _, name := range []string{"deploy.post_deploy_hooks", "deploy.run"} {
		if st := one(t, byName, name).Status(); st.Code != codes.Error {
			t.Fatalf("%s status = %v, want Error", name, st)
		}
	}
	if build := one(t, byName, "deploy.build"); build.Status().Code == codes.Error {
		t.Fatal("deploy.build succeeded and must not carry an error status")
	}
	if n := len(byName["deploy.replica"]); n != 0 {
		t.Fatalf("no replica may boot after a hook failure, got %d replica spans", n)
	}
}

func TestRun_NoTracerNoSpansNoPanic(t *testing.T) {
	p := poolParamsWithHook(t, "phase-no-tracer", 1, nil)
	if p.Tracer != nil || p.TraceCtx != nil {
		t.Fatal("fixture must leave tracing unset")
	}
	res, err := deploy.Run(p)
	if err != nil {
		t.Fatalf("Run without a tracer: %v", err)
	}
	if len(res.Replicas) != 1 {
		t.Fatalf("want 1 replica, got %d", len(res.Replicas))
	}
}

func TestRun_TraceCtxCancellationDoesNotAbortBuild(t *testing.T) {
	_, tr := phaseRecorder(t)
	spanCtx, span := tr.Start(context.Background(), "request")
	defer span.End()
	cancelled, cancel := context.WithCancel(spanCtx)
	cancel()

	p := poolParamsWithHook(t, "phase-cancelled", 1, nil)
	var (
		mu      sync.Mutex
		syncErr = errors.New("python sync never ran")
	)
	t.Cleanup(deploy.SetSyncHooksForTest(
		func(ctx context.Context, _ string, _ []string) error {
			mu.Lock()
			syncErr = ctx.Err()
			mu.Unlock()
			return nil
		},
		func(context.Context, string, []string) error { return nil },
	))
	p.Tracer, p.TraceCtx = tr, cancelled
	if _, err := deploy.Run(p); err != nil {
		t.Fatalf("Run with a cancelled TraceCtx: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if syncErr != nil {
		t.Fatalf("the build saw a done context (%v); TraceCtx must carry parentage only", syncErr)
	}
}

func TestRunReplica_EmitsExactlyOneReplicaSpan(t *testing.T) {
	rec, tr := phaseRecorder(t)
	parentCtx, parent := tr.Start(context.Background(), "wake")
	p := poolParamsWithHook(t, "phase-run-replica", 1, nil)
	p.Tracer, p.TraceCtx = tr, parentCtx
	p.Proxy.SetPoolSize(p.Slug, 1)
	if _, err := deploy.RunReplica(p, 0); err != nil {
		t.Fatalf("RunReplica: %v", err)
	}
	parent.End()
	byName := spansByName(rec.Ended())
	r := one(t, byName, "deploy.replica")
	if r.Parent().SpanID() != parent.SpanContext().SpanID() {
		t.Fatal("deploy.replica must be a child of the caller's span")
	}
	if v, ok := spanAttr(r, "shinyhub.replica"); !ok || v.AsInt64() != 0 {
		t.Fatalf("shinyhub.replica = %v (present %v), want 0", v.AsInt64(), ok)
	}
	if n := len(byName["deploy.run"]); n != 0 {
		t.Fatalf("RunReplica must not open deploy.run, got %d", n)
	}
}

// An activation that finds its prepared environment on disk launches against it
// instead of rebuilding, and deploy.run says so. A promotion builds and carries
// no such mark, so the attribute cannot be read as always-on.
func TestRun_MarksBuildReusedOnlyWhenActivationSkipsTheBuild(t *testing.T) {
	for _, tc := range []struct {
		name       string
		mode       deploy.PreparationMode
		wantReused bool
		wantBuilds int
	}{
		{"promotion builds", deploy.PrepareRequired, false, 1},
		{"prepared activation reuses", deploy.PrepareSkip, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec, tr := phaseRecorder(t)
			p := poolParamsWithHook(t, "phase-reuse", 1, nil)
			// Without a pyproject.toml the bundle builds at launch, so there is
			// no on-disk environment to miss and the prepared state is present.
			if err := os.Remove(filepath.Join(p.BundleDir, "pyproject.toml")); err != nil {
				t.Fatal(err)
			}
			p.Tracer, p.Preparation = tr, tc.mode
			if _, err := deploy.Run(p); err != nil {
				t.Fatalf("Run: %v", err)
			}
			byName := spansByName(rec.Ended())
			v, ok := spanAttr(one(t, byName, "deploy.run"), "shinyhub.deploy.build_reused")
			if got := ok && v.AsBool(); got != tc.wantReused {
				t.Fatalf("build_reused = %v, want %v", got, tc.wantReused)
			}
			if n := len(byName["deploy.build"]); n != tc.wantBuilds {
				t.Fatalf("deploy.build spans = %d, want %d", n, tc.wantBuilds)
			}
		})
	}
}

// A failed dependency sync wraps uv's full output, which can run to kilobytes
// and echo a package-index URL with credentials. The span may only carry a
// bounded, redacted first line; the caller still gets the whole error.
func TestRun_BuildFailureSpanErrorIsRedactedAndBounded(t *testing.T) {
	const secret = "tok-s3cret"
	cred := "https://user:" + secret + "@idx.example/simple"
	cases := map[string]error{
		"credential in the output body": fmt.Errorf("%w\n%s", errors.New("exit status 2"),
			"Resolving against "+cred+"\n"+strings.Repeat("uv output line\n", 2000)),
		"credential in a long first line": fmt.Errorf("%w\n%s", errors.New("fetch "+cred+" failed: "+strings.Repeat("x", 4000)),
			"second line "+cred),
	}
	for name, syncErr := range cases {
		t.Run(name, func(t *testing.T) {
			rec, tr := phaseRecorder(t)
			p := poolParamsWithHook(t, "phase-build-fail", 1, nil)
			t.Cleanup(deploy.SetSyncHooksForTest(
				func(context.Context, string, []string) error { return syncErr },
				func(context.Context, string, []string) error { return syncErr },
			))
			p.Tracer = tr
			_, err := deploy.Run(p)
			if err == nil {
				t.Fatal("Run must fail when the dependency sync fails")
			}
			if !strings.Contains(err.Error(), secret) || !errors.Is(err, syncErr) {
				t.Fatalf("the caller's error must be unchanged, got %q", err)
			}
			byName := spansByName(rec.Ended())
			var checked int
			for _, spanName := range []string{"deploy.build", "deploy.run"} {
				s := one(t, byName, spanName)
				st := s.Status()
				if st.Code != codes.Error {
					t.Fatalf("%s status = %v, want Error", spanName, st)
				}
				texts := []string{st.Description}
				for _, ev := range s.Events() {
					for _, kv := range ev.Attributes {
						texts = append(texts, kv.Value.Emit())
					}
				}
				for _, text := range texts {
					checked++
					if strings.Contains(text, secret) {
						t.Fatalf("%s exports the index credential: %q", spanName, text)
					}
					if strings.Contains(text, "\n") {
						t.Fatalf("%s exports more than the first line: %q", spanName, text)
					}
					if len(text) > spanerr.MaxBytes {
						t.Fatalf("%s exports %d bytes of error text, want <= %d", spanName, len(text), spanerr.MaxBytes)
					}
				}
				if st.Description == "" {
					t.Fatalf("%s must still describe the failure", spanName)
				}
			}
			if checked < 4 {
				t.Fatalf("checked only %d exported texts; the exception events are missing", checked)
			}
		})
	}
}
