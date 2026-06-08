package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// fakeDrainChecker satisfies drainChecker for handler tests without the full
// proxy.Proxy. Fields are set directly by the test.
type fakeDrainChecker struct {
	draining   bool
	syncedOnce bool
}

func (f *fakeDrainChecker) Draining() bool   { return f.draining }
func (f *fakeDrainChecker) SyncedOnce() bool { return f.syncedOnce }

// fakePinger satisfies pinger for handler tests.
type fakePinger struct{ err error }

func (f *fakePinger) PingContext(_ context.Context) error { return f.err }

// closedCh returns a closed channel, simulating a live listener.
func closedCh() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

// openCh returns a channel that is never closed, simulating startup.
func openCh() chan struct{} { return make(chan struct{}) }

// ----------------------------------------------------------------------------
// /readyz tests
// ----------------------------------------------------------------------------

// TestReadyzHandler_Draining asserts 503 with reason "draining" when the proxy
// is shutting down.
func TestReadyzHandler_Draining(t *testing.T) {
	prx := &fakeDrainChecker{draining: true, syncedOnce: true}
	h := readyzHandler(prx, closedCh(), &fakePinger{})

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if got := rec.Body.String(); got != `{"ready":false,"reason":"draining"}` {
		t.Errorf("body = %q", got)
	}
}

// TestReadyzHandler_Starting asserts 503 with reason "starting" before the
// listener is live (readyCh not closed).
func TestReadyzHandler_Starting(t *testing.T) {
	prx := &fakeDrainChecker{draining: false, syncedOnce: true}
	h := readyzHandler(prx, openCh(), &fakePinger{})

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if got := rec.Body.String(); got != `{"ready":false,"reason":"starting"}` {
		t.Errorf("body = %q", got)
	}
}

// TestReadyzHandler_DBFails asserts 503 with reason "db" when the store ping
// fails.
func TestReadyzHandler_DBFails(t *testing.T) {
	prx := &fakeDrainChecker{draining: false, syncedOnce: true}
	h := readyzHandler(prx, closedCh(), &fakePinger{err: errors.New("connection refused")})

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if got := rec.Body.String(); got != `{"ready":false,"reason":"db"}` {
		t.Errorf("body = %q", got)
	}
}

// TestReadyzHandler_NotYetSynced asserts 503 with reason "syncing" when
// SyncedOnce is false (clustered deployment before first pool sync).
func TestReadyzHandler_NotYetSynced(t *testing.T) {
	prx := &fakeDrainChecker{draining: false, syncedOnce: false}
	h := readyzHandler(prx, closedCh(), &fakePinger{err: nil})

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if got := rec.Body.String(); got != `{"ready":false,"reason":"syncing"}` {
		t.Errorf("body = %q, want {\"ready\":false,\"reason\":\"syncing\"}", got)
	}
}

// TestReadyzHandler_Ready asserts 200 when all gates pass (listener live, DB
// healthy, synced). This is the single-node steady state: MarkSynced is
// pre-seeded so syncedOnce is always true.
func TestReadyzHandler_Ready(t *testing.T) {
	prx := &fakeDrainChecker{draining: false, syncedOnce: true}
	h := readyzHandler(prx, closedCh(), &fakePinger{err: nil})

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != `{"ready":true}` {
		t.Errorf("body = %q, want {\"ready\":true}", got)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
}

// TestReadyzHandler_SingleNodeNeverSyncing confirms that a proxy with
// syncedOnce pre-seeded true (single-node path) never returns the "syncing"
// 503. This pins the byte-for-byte single-node regression: /readyz must return
// 200 as long as the other gates pass, regardless of cluster mode logic.
func TestReadyzHandler_SingleNodeNeverSyncing(t *testing.T) {
	prx := &fakeDrainChecker{draining: false, syncedOnce: true}
	h := readyzHandler(prx, closedCh(), &fakePinger{err: nil})

	for range 5 {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("single-node: status = %d, want 200 (no syncing 503)", rec.Code)
		}
	}
}

// ----------------------------------------------------------------------------
// /activez tests
// ----------------------------------------------------------------------------

// TestActivezHandler_Active asserts 200 {"active":true} when ownerAndReady
// returns true.
func TestActivezHandler_Active(t *testing.T) {
	h := activezHandler(func() bool { return true })

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/activez", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != `{"active":true}` {
		t.Errorf("body = %q, want {\"active\":true}", got)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
}

// TestActivezHandler_Standby asserts 503 {"active":false} when ownerAndReady
// returns false (standby instance or gate not yet open).
func TestActivezHandler_Standby(t *testing.T) {
	h := activezHandler(func() bool { return false })

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/activez", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if got := rec.Body.String(); got != `{"active":false}` {
		t.Errorf("body = %q, want {\"active\":false}", got)
	}
}

// TestActivezHandler_ReflectsOwnerAndReadyPredicate wires the real
// ownerAndReadyPredicate to activezHandler and exercises the three state
// transitions: owner-but-not-ready, owner-and-ready, and not-owner.
func TestActivezHandler_ReflectsOwnerAndReadyPredicate(t *testing.T) {
	var ready atomic.Bool
	owner := true
	pred := ownerAndReadyPredicate(func() bool { return owner }, &ready)
	h := activezHandler(pred)

	check := func(label string, wantCode int, wantBody string) {
		t.Helper()
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodGet, "/activez", nil))
		if rec.Code != wantCode {
			t.Errorf("%s: status = %d, want %d", label, rec.Code, wantCode)
		}
		if got := rec.Body.String(); got != wantBody {
			t.Errorf("%s: body = %q, want %q", label, got, wantBody)
		}
	}

	// Owner but not yet ready (ownerWork running, index refresh in progress).
	check("owner+not-ready", http.StatusServiceUnavailable, `{"active":false}`)

	// Owner and ready (index refreshed, gate open).
	ready.Store(true)
	check("owner+ready", http.StatusOK, `{"active":true}`)

	// Not owner (lost lease or standby).
	owner = false
	check("not-owner", http.StatusServiceUnavailable, `{"active":false}`)
}
