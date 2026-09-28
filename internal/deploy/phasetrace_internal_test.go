package deploy

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
)

// A resume never passes through bootReplica, so it opens its own deploy.replica
// span, marked as a resume so a trace distinguishes it from a cold boot.
func TestResumeReplica_EmitsOneResumeReplicaSpan(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()), sdktrace.WithSpanProcessor(rec))
	defer func() { _ = tp.Shutdown(context.Background()) }()
	tr := tp.Tracer("t")

	fake := &resumeFakeRuntime{
		resumeEP: process.ReplicaEndpoint{URL: "http://127.0.0.1:2500", Provider: "fake", Handle: process.RunHandle{PID: 9}},
	}
	mgr := startAndSuspend(t, fake)
	prx := proxy.New()
	prx.SetPoolSize("app", 1)

	parentCtx, parent := tr.Start(context.Background(), "wake")
	p := Params{
		Slug: "app", Manager: mgr, Proxy: prx,
		HealthCheck: func(string, time.Duration, http.RoundTripper) error { return nil },
		Tracer:      tr, TraceCtx: parentCtx,
	}
	if _, err := ResumeReplica(p, 0); err != nil {
		t.Fatalf("ResumeReplica: %v", err)
	}
	parent.End()

	var replicas []sdktrace.ReadOnlySpan
	for _, s := range rec.Ended() {
		if s.Name() == "deploy.replica" {
			replicas = append(replicas, s)
		}
	}
	if len(replicas) != 1 {
		t.Fatalf("want exactly one deploy.replica span, got %d", len(replicas))
	}
	r := replicas[0]
	if r.Parent().SpanID() != parent.SpanContext().SpanID() {
		t.Fatal("deploy.replica must be a child of the caller's span")
	}
	resume := false
	for _, kv := range r.Attributes() {
		if kv.Key == "shinyhub.deploy.resume" {
			resume = kv.Value.AsBool()
		}
	}
	if !resume {
		t.Fatalf("deploy.replica attributes %v lack shinyhub.deploy.resume=true", r.Attributes())
	}
}

// resumeReplicaSpan runs ResumeReplica against mgr under a recording tracer and
// returns its error and the single deploy.replica span it emitted.
func resumeReplicaSpan(t *testing.T, mgr *process.Manager) (sdktrace.ReadOnlySpan, error) {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()), sdktrace.WithSpanProcessor(rec))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	prx := proxy.New()
	prx.SetPoolSize("app", 1)
	p := Params{
		Slug: "app", Manager: mgr, Proxy: prx,
		HealthCheck: func(string, time.Duration, http.RoundTripper) error { return nil },
		Tracer:      tp.Tracer("t"),
	}
	_, err := ResumeReplica(p, 0)
	var replicas []sdktrace.ReadOnlySpan
	for _, s := range rec.Ended() {
		if s.Name() == "deploy.replica" {
			replicas = append(replicas, s)
		}
	}
	if len(replicas) != 1 {
		t.Fatalf("want exactly one ended deploy.replica span, got %d", len(replicas))
	}
	return replicas[0], err
}

func resumeUnavailableAttr(s sdktrace.ReadOnlySpan) (value, present bool) {
	for _, kv := range s.Attributes() {
		if kv.Key == "shinyhub.deploy.resume_unavailable" {
			return kv.Value.AsBool(), true
		}
	}
	return false, false
}

// A replica that cannot be resumed sends the caller to a cold boot. That is an
// expected fallback, so the span says so without an error status, while the
// caller still receives the sentinel it branches on.
func TestResumeReplica_UnavailableIsNotASpanError(t *testing.T) {
	cases := []struct {
		name string
		mgr  func(*testing.T) *process.Manager
		want error
	}{
		{"not suspended", func(t *testing.T) *process.Manager {
			mgr := process.NewManager(t.TempDir(), &resumeFakeRuntime{})
			if _, err := mgr.Start(process.StartParams{
				Slug: "app", Index: 0, Command: []string{"true"}, Dir: t.TempDir(), Port: 2500,
			}); err != nil {
				t.Fatalf("start: %v", err)
			}
			return mgr
		}, process.ErrReplicaNotSuspended},
		{"no replica", func(t *testing.T) *process.Manager {
			return process.NewManager(t.TempDir(), &resumeFakeRuntime{})
		}, process.ErrReplicaNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			span, err := resumeReplicaSpan(t, tc.mgr(t))
			if !errors.Is(err, tc.want) {
				t.Fatalf("ResumeReplica error = %v, want %v", err, tc.want)
			}
			if st := span.Status(); st.Code == codes.Error {
				t.Fatalf("deploy.replica status = %v, want no error for an unavailable resume", st)
			}
			if len(span.Events()) != 0 {
				t.Fatalf("deploy.replica recorded events %v for an unavailable resume", span.Events())
			}
			if v, ok := resumeUnavailableAttr(span); !ok || !v {
				t.Fatalf("deploy.replica attributes %v lack shinyhub.deploy.resume_unavailable=true", span.Attributes())
			}
		})
	}
}

// A resume the runtime attempted and failed is a genuine error.
func TestResumeReplica_FailedResumeIsASpanError(t *testing.T) {
	mgr := startAndSuspend(t, &resumeFakeRuntime{resumeErr: errors.New("restore failed")})
	span, err := resumeReplicaSpan(t, mgr)
	if err == nil {
		t.Fatal("ResumeReplica must fail when the runtime resume fails")
	}
	if st := span.Status(); st.Code != codes.Error {
		t.Fatalf("deploy.replica status = %v, want Error", st)
	}
	if _, ok := resumeUnavailableAttr(span); ok {
		t.Fatal("a failed resume must not be marked resume_unavailable")
	}
}
