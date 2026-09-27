package proxy_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/proxy"
)

// TestRevertGenerationKeepsFailedGenerationVisibleToHibernation reproduces a
// control-plane commit failure: v2 is activated, a request is still being
// served by it, and the commit step that follows activation fails, so the
// caller reverts back to v1. RevertGeneration must leave v2 exactly as
// draining as ActivateGeneration would have left v1 in the mirror case,
// because BeginHibernate (and TryRetireGeneration) only ever look at
// pool.replicas, pool.workers and pool.drainingGenerations - never at
// pool.candidates. Before the fix, revert filed the failed generation under
// pool.candidates, so its still-open connection became invisible to the
// idle watchdog and the app could be hibernated out from under it.
func TestRevertGenerationKeepsFailedGenerationVisibleToHibernation(t *testing.T) {
	t.Parallel()

	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseHold := func() { releaseOnce.Do(func() { close(release) }) }
	v1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "v1")
	}))
	defer v1.Close()
	v2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/hold" {
			close(entered)
			<-release
		}
		_, _ = io.WriteString(w, "v2")
	}))
	// releaseHold must run before the servers are closed: httptest.Server.Close
	// blocks until every in-flight connection finishes, and the held /hold
	// request only finishes once release is closed. Deferred last so it runs
	// first (LIFO), this keeps a failed assertion below from hanging the test.
	defer releaseHold()
	defer v1.Close()
	defer v2.Close()

	p := proxy.New()
	p.SetPoolSize("dashboard", 1)
	if err := p.RegisterReplica("dashboard", 0, v1.URL, nil, 101); err != nil {
		t.Fatalf("register v1: %v", err)
	}
	if err := p.StageGeneration("dashboard", 202, 1); err != nil {
		t.Fatalf("stage v2: %v", err)
	}
	if err := p.RegisterGenerationReplica("dashboard", 202, 0, v2.URL, nil); err != nil {
		t.Fatalf("register v2: %v", err)
	}
	if _, err := p.ActivateGeneration("dashboard", 202); err != nil {
		t.Fatalf("activate v2: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		req := httptest.NewRequest(http.MethodGet, "/app/dashboard/hold", nil)
		p.ServeHTTP(httptest.NewRecorder(), req)
	}()
	<-entered

	// The commit step after activation failed: revert back to v1 while v2's
	// request is still open.
	if err := p.RevertGeneration("dashboard", 202, 101); err != nil {
		releaseHold()
		t.Fatalf("revert to v1: %v", err)
	}

	// Errorf, not Fatalf: the hold must be released below regardless of these
	// results, or the deferred server Close calls above hang forever.
	if !p.IsGenerationDraining("dashboard", 202) {
		t.Error("reverted generation 202 is not draining: it is unroutable and invisible to idle accounting")
	}
	if sessions, ok := p.GenerationDrainingSessions("dashboard", 202); !ok || sessions != 1 {
		t.Errorf("draining sessions for reverted generation 202 = %d, ok=%v, want 1, true", sessions, ok)
	}
	if p.BeginHibernate("dashboard", time.Now().Add(time.Second)) {
		t.Error("hibernated the app while a request on the reverted generation was still open")
	}
	if p.TryRetireGeneration("dashboard", 202) {
		t.Error("retired the reverted generation while its request was still active")
	}

	releaseHold()
	<-done

	if !p.TryRetireGeneration("dashboard", 202) {
		t.Fatal("reverted generation 202 did not retire after its request drained")
	}

	// A fresh request now goes to the restored generation.
	req := httptest.NewRequest(http.MethodGet, "/app/dashboard/", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	if body := rec.Body.String(); body != "v1" {
		t.Fatalf("post-revert request = %q, want v1", body)
	}
}

// TestRevertGenerationPreservesGroupedClientBindings reproduces the grouped-
// isolation case: a client is pinned to v2 while it is active, the commit
// step fails, and the caller reverts to v1. Before the fix, revert deleted
// the client's binding outright (contradicting its own comment, "old client
// bindings continue to name old slots"), so the client's next request landed
// on a fresh binding to whichever generation was newly active - silently
// losing its session - instead of continuing to reach its own worker like an
// ordinary demotion (ActivateGeneration) would have allowed.
func TestRevertGenerationPreservesGroupedClientBindings(t *testing.T) {
	t.Parallel()

	v1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "v1")
	}))
	defer v1.Close()

	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseHold := func() { releaseOnce.Do(func() { close(release) }) }
	v2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/hold" {
			close(entered)
			<-release
		}
		_, _ = io.WriteString(w, "v2")
	}))
	// releaseHold deferred last so it runs first (LIFO), before the server
	// Close calls that would otherwise block on the held /hold connection.
	defer releaseHold()
	defer v2.Close()

	p := proxy.New()
	p.SetPoolMode("dashboard", config.IsolationGrouped, 4, 2)
	if err := p.RegisterElasticWorker("dashboard", 0, v1.URL, nil, 101); err != nil {
		t.Fatalf("register v1: %v", err)
	}

	slot, err := p.StageGroupedGeneration("dashboard", 202)
	if err != nil {
		t.Fatalf("stage v2: %v", err)
	}
	if err := p.RegisterGenerationReplica("dashboard", 202, slot, v2.URL, nil); err != nil {
		t.Fatalf("register v2: %v", err)
	}
	if _, err := p.ActivateGeneration("dashboard", 202); err != nil {
		t.Fatalf("activate v2: %v", err)
	}

	// A client connects while v2 is active and is pinned to it.
	pinReq := httptest.NewRequest(http.MethodGet, "/app/dashboard/", nil)
	pinRec := httptest.NewRecorder()
	p.ServeHTTP(pinRec, pinReq)
	if body := pinRec.Body.String(); body != "v2" {
		t.Fatalf("initial pin = %q, want v2", body)
	}
	clientCookies := pinRec.Result().Cookies()

	// That client has a request open when the commit step fails and the
	// caller reverts back to v1.
	done := make(chan struct{})
	go func() {
		defer close(done)
		req := httptest.NewRequest(http.MethodGet, "/app/dashboard/hold", nil)
		for _, c := range clientCookies {
			req.AddCookie(c)
		}
		p.ServeHTTP(httptest.NewRecorder(), req)
	}()
	<-entered

	if err := p.RevertGeneration("dashboard", 202, 101); err != nil {
		releaseHold()
		t.Fatalf("revert to v1: %v", err)
	}
	releaseHold()
	<-done

	// The client reconnects using its original cookies. Its binding must
	// still name its own worker on the reverted (now draining) generation,
	// not a fresh pin onto the newly-restored active generation.
	reconnect := httptest.NewRequest(http.MethodGet, "/app/dashboard/", nil)
	for _, c := range clientCookies {
		reconnect.AddCookie(c)
	}
	reconnectRec := httptest.NewRecorder()
	p.ServeHTTP(reconnectRec, reconnect)
	if body := reconnectRec.Body.String(); body != "v2" {
		t.Fatalf("reconnect after revert = %q, want v2 (client binding must survive revert)", body)
	}
}
