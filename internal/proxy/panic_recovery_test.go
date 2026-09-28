package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
)

// TestElasticSpawn_PanicInSpawnCallbackDoesNotCrash proves that a panic
// raised by the spawn callback the proxy dispatches from ServeHTTP's
// decisionBind/placedBind path (proxy.go, "if pl.spawned && spawnFn != nil")
// does not crash the process. The callback runs in a goroutine the proxy
// launches itself; a panic there is unrecoverable from any caller's frame,
// so the dispatch site must recover it locally (via safego.Go).
//
// Failing-first: with that site reverted to a bare `go spawnFn(slug,
// pl.slotID)`, running this test alone in a re-exec'd subprocess
// (`GOWORK=off go test ./internal/proxy/ -run
// TestElasticSpawn_PanicInSpawnCallbackDoesNotCrash`) crashed the whole test
// binary: stderr showed "panic: boom-spawn-panic" with no PASS/FAIL line at
// all, and the process exited non-zero. The safego.Go wrapper was restored
// immediately after capturing that output.
func TestElasticSpawn_PanicInSpawnCallbackDoesNotCrash(t *testing.T) {
	const slug = "panicspawn"
	called := make(chan struct{})

	p := New()
	p.SetPoolMode(slug, config.IsolationPerSession, 0, 1)
	p.SetSpawnFunc(func(string, int) {
		close(called)
		panic("boom-spawn-panic")
	})

	req := httptest.NewRequest("GET", "/app/"+slug+"/", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 (loading page) even though the dispatched spawn panics, got %d", rec.Code)
	}

	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("spawn callback was never invoked")
	}

	// Give the panicking goroutine time to actually unwind and be recovered.
	// If the panic were not recovered, the whole test binary would crash
	// during this window rather than reaching the line below.
	time.Sleep(100 * time.Millisecond)
}

// TestGraceExpiry_PanicInTerminateCallbackDoesNotCrash proves that a panic
// raised by the terminate callback graceExpiry dispatches (proxy/elastic.go,
// "if term != nil { ... go term(slug, slotID) }" inside the release-timer
// closure) does not crash the process. graceExpiry itself runs inside a
// safego.AfterFunc-wrapped timer callback, but that recover cannot catch a
// panic raised in the SEPARATE goroutine the terminate dispatch launches
// (a panic never crosses a goroutine boundary to a recover in a different
// goroutine), so the terminate dispatch needs its own safego.Go wrapper.
//
// Failing-first: with that dispatch reverted to a bare `go term(slug,
// slotID)`, running this test alone in a re-exec'd subprocess
// (`GOWORK=off go test ./internal/proxy/ -run
// TestGraceExpiry_PanicInTerminateCallbackDoesNotCrash`) crashed the whole
// test binary: stderr showed "panic: boom-terminate-panic" with no PASS/FAIL
// line at all, and the process exited non-zero. The safego.Go wrapper was
// restored immediately after capturing that output.
func TestGraceExpiry_PanicInTerminateCallbackDoesNotCrash(t *testing.T) {
	old := clientGraceTTL
	clientGraceTTL = 20 * time.Millisecond
	t.Cleanup(func() { clientGraceTTL = old })

	const slug = "panicterminate"
	const clientID = "c1"
	called := make(chan struct{})

	p := New()
	p.SetPoolMode(slug, config.IsolationPerSession, 0, 5)
	p.SetTerminateFunc(func(string, int) {
		close(called)
		panic("boom-terminate-panic")
	})

	slotID := p.reserveWorker(slug, clientID)
	if slotID < 0 {
		t.Fatal("reserveWorker returned -1 unexpectedly")
	}
	p.bindClient(slug, clientID, slotID)
	p.clientConnOpened(slug, clientID)
	p.clientConnClosed(slug, clientID) // arms the grace-expiry release timer

	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("terminate callback was never invoked")
	}

	// Give the panicking goroutine time to actually unwind and be recovered.
	time.Sleep(100 * time.Millisecond)
}
