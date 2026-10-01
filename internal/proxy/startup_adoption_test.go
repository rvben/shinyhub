package proxy_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/proxy"
)

func TestStartupAdoptionWaitsForReadiness(t *testing.T) {
	var ready atomic.Bool
	var requests atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if !ready.Load() {
			w.WriteHeader(503)
			return
		}
		w.Write([]byte("live app"))
	}))
	defer backend.Close()
	rr := makeReplica("demo", 1, 0, backend.URL, 42)
	p := proxy.New()
	s := proxy.NewPoolSyncer(p, &staticSource{rows: []db.RoutableReplica{rr}}, noopTransport{}, slog.Default(), false)
	bundle := t.TempDir()
	s.SetReplicaValidator(func(ctx context.Context, r db.RoutableReplica, tr http.RoundTripper) error {
		return deploy.ProbeReadiness(ctx, r.Replica.EndpointURL, bundle, tr)
	})
	s.RunOnce(context.Background())
	if p.ReplicaTargetURL("demo", 0) != "" {
		t.Fatal("unready route published")
	}
	var entry proxy.AccessLogEntry
	p.SetAccessLogger(func(e proxy.AccessLogEntry) { entry = e })
	req := httptest.NewRequest("GET", "/app/demo/", nil)
	req.AddCookie(&http.Cookie{Name: "shinyhub_rep_demo", Value: "0.42"})
	p.ServeHTTP(httptest.NewRecorder(), req)
	if requests.Load() != 1 {
		t.Fatal("visitor forwarded to unready endpoint")
	}
	if !entry.Fallback || entry.FallbackReason != "replica-starting" {
		t.Fatalf("fallback = %+v", entry)
	}
	if got := p.RejectsByReason("demo", 10*time.Minute); len(got) != 0 {
		t.Fatalf("startup raised admissions: %v", got)
	}
	ready.Store(true)
	s.RunOnce(context.Background())
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	if rec.Body.String() != "live app" || entry.Fallback {
		t.Fatalf("ready app not routed: %s, %+v", rec.Body.String(), entry)
	}
}

func TestStartupAdoptionRejectsDeadPersistedEndpoint(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := dead.URL
	dead.Close()
	p := proxy.New()
	s := proxy.NewPoolSyncer(p, &staticSource{rows: []db.RoutableReplica{makeReplica("demo", 1, 0, url, 42)}}, noopTransport{}, slog.Default(), false)
	bundle := t.TempDir()
	s.SetReplicaValidator(func(ctx context.Context, r db.RoutableReplica, tr http.RoundTripper) error {
		return deploy.ProbeReadiness(ctx, r.Replica.EndpointURL, bundle, tr)
	})
	s.RunOnce(context.Background())
	if p.ReplicaTargetURL("demo", 0) != "" {
		t.Fatal("dead persisted endpoint became routable")
	}
}

func TestStartingUpgradeIsRetryableWithoutAdmissionWarning(t *testing.T) {
	p := proxy.New()
	p.SetPoolSize("demo", 1)
	p.SetAppStatusLookup(func(string) (string, string) { return "waking", "" })
	var entry proxy.AccessLogEntry
	p.SetAccessLogger(func(e proxy.AccessLogEntry) { entry = e })
	req := httptest.NewRequest("GET", "/app/demo/websocket/", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	if rec.Code != 503 || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("response = %d, %v", rec.Code, rec.Header())
	}
	if entry.Reject != proxy.ReasonReplicaStarting || !entry.Fallback {
		t.Fatalf("entry = %+v", entry)
	}
	if got := p.RejectsByReason("demo", 10*time.Minute); len(got) != 0 {
		t.Fatalf("startup admissions = %v", got)
	}
}

func TestRecoveredRouteGatesTrafficAndKeepsRealFailuresVisible(t *testing.T) {
	var ready atomic.Bool
	var forwarded atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			if !ready.Load() {
				w.WriteHeader(503)
				return
			}
		} else {
			forwarded.Add(1)
		}
		w.Write([]byte("app"))
	}))
	defer backend.Close()
	p := proxy.New()
	bundle := t.TempDir()
	transport := p.NewReadinessTransport(nil, func(ctx context.Context) error { return deploy.ProbeReadiness(ctx, backend.URL, bundle, nil) })
	p.SetPoolSize("demo", 1)
	if err := p.RegisterReplica("demo", 0, backend.URL, transport, 42); err != nil {
		t.Fatal(err)
	}
	var entry proxy.AccessLogEntry
	p.SetAccessLogger(func(e proxy.AccessLogEntry) { entry = e })
	req := httptest.NewRequest("GET", "/app/demo/data", nil)
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	if forwarded.Load() != 0 || rec.Code != 503 || entry.FallbackReason != "replica-starting" || entry.Reject != proxy.ReasonReplicaStarting {
		t.Fatalf("unready request forwarded: %+v, code=%d", entry, rec.Code)
	}
	ready.Store(true)
	p.ServeHTTP(httptest.NewRecorder(), req)
	if forwarded.Load() != 1 || entry.Fallback {
		t.Fatalf("ready traffic not forwarded: %+v", entry)
	}
	backend.Close()
	p.ServeHTTP(httptest.NewRecorder(), req)
	if entry.FallbackReason != "upstream-error" || entry.Reject == proxy.ReasonReplicaStarting {
		t.Fatalf("real failure masked as startup: %+v", entry)
	}
}

func TestRecoveredRouteHoldsUntilReady(t *testing.T) {
	p := proxy.New()
	p.SetWakeHoldTimeout(time.Second)
	var checks atomic.Int32
	probed := make(chan struct{})
	ready := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ready inline")) }))
	defer backend.Close()
	transport := p.NewReadinessTransport(nil, func(ctx context.Context) error {
		if checks.Add(1) == 1 {
			close(probed)
		}
		select {
		case <-ready:
			return nil
		default:
			return context.DeadlineExceeded
		}
	})
	p.SetPoolSize("demo", 1)
	if err := p.RegisterReplica("demo", 0, backend.URL, transport, 42); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { p.ServeHTTP(rec, httptest.NewRequest("GET", "/app/demo/", nil)); close(done) }()
	select {
	case <-probed:
	case <-time.After(time.Second):
		t.Fatal("readiness not checked")
	}
	select {
	case <-done:
		t.Fatal("request was not held")
	default:
	}
	close(ready)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("request did not resume")
	}
	if rec.Code != 200 || rec.Body.String() != "ready inline" {
		t.Fatalf("held response=%d %s", rec.Code, rec.Body.String())
	}
}

func TestPartialStartupCapacityDoesNotRaiseAdmissionWarning(t *testing.T) {
	p := proxy.New()
	p.SetPoolSize("demo", 2)
	if err := p.RegisterReplica("demo", 0, "http://127.0.0.1:29999", nil, 0); err != nil {
		t.Fatal(err)
	}
	p.DrainReplica("demo", 0)
	status := "waking"
	p.SetAppStatusLookup(func(string) (string, string) { return status, "" })
	var entry proxy.AccessLogEntry
	p.SetAccessLogger(func(e proxy.AccessLogEntry) { entry = e })
	p.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/app/demo/", nil))
	if entry.Reject != proxy.ReasonReplicaStarting || len(p.RejectsByReason("demo", time.Minute)) != 0 {
		t.Fatalf("partial startup counted as degradation: %+v", entry)
	}
	status = "running"
	p.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/app/demo/", nil))
	if entry.Reject != proxy.ReasonPoolDegraded || p.RejectsByReason("demo", time.Minute)[proxy.ReasonPoolDegraded] != 1 {
		t.Fatalf("real degradation hidden: %+v", entry)
	}
}

func TestRecoveredRouteHoldIsBounded(t *testing.T) {
	p := proxy.New()
	p.SetWakeHoldTimeout(30 * time.Millisecond)
	transport := p.NewReadinessTransport(nil, func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})
	p.SetPoolSize("demo", 1)
	if err := p.RegisterReplica("demo", 0, "http://127.0.0.1:29999", transport, 42); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/app/demo/data", nil)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { p.ServeHTTP(rec, req); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("readiness hold exceeded bound")
	}
	if rec.Code != 503 || rec.Header().Get("Retry-After") == "" || len(p.RejectsByReason("demo", time.Minute)) != 0 {
		t.Fatalf("expired hold = %d, %v", rec.Code, rec.Header())
	}
}
