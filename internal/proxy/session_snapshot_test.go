package proxy_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvben/shinyhub/internal/proxy"
)

// TestPoolSessionSnapshot verifies the per-pool session snapshot used by the
// Prometheus session gauges: it reports total active sessions, the per-replica
// cap, and the count of LIVE (non-nil) replicas - the last of which lets the
// collector compute the admission ceiling as Cap*Replicas.
func TestPoolSessionSnapshot(t *testing.T) {
	p := proxy.New()

	// "busy": size 1, cap 1, one live replica pinned at cap (1 active session).
	done := occupyPoolToCap(t, p, "busy", 1)
	defer done()

	// "free": configured size 2 but only ONE live replica (degraded), cap left
	// at 0 (unlimited). Exercises the live-replica count vs configured size.
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	p.SetPoolSize("free", 2)
	if err := p.RegisterReplica("free", 0, backend.URL, nil, 0); err != nil {
		t.Fatalf("register free: %v", err)
	}

	snap := p.PoolSessionSnapshot()

	busy, ok := snap["busy"]
	if !ok {
		t.Fatalf("snapshot missing 'busy': %+v", snap)
	}
	if busy.Sessions != 1 || busy.Cap != 1 || busy.Replicas != 1 {
		t.Errorf("busy = %+v, want {Sessions:1 Cap:1 Replicas:1}", busy)
	}

	free, ok := snap["free"]
	if !ok {
		t.Fatalf("snapshot missing 'free': %+v", snap)
	}
	if free.Sessions != 0 || free.Cap != 0 || free.Replicas != 1 {
		t.Errorf("free = %+v, want {Sessions:0 Cap:0 Replicas:1} (size 2, one live)", free)
	}
}

// A draining replica (scale-down in progress) still holds its existing sessions
// but admits no new ones, so it must NOT count toward the admission ceiling -
// otherwise sessions_limit overstates capacity during a drain and the
// saturation ratio reads artificially low exactly when the pool is under
// pressure.
func TestPoolSessionSnapshot_ExcludesDrainingFromCeiling(t *testing.T) {
	p := proxy.New()
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	b0 := httptest.NewServer(h)
	b1 := httptest.NewServer(h)
	defer b0.Close()
	defer b1.Close()

	p.SetPoolSize("sd", 2)
	p.SetPoolCap("sd", 10)
	if err := p.RegisterReplica("sd", 0, b0.URL, nil, 0); err != nil {
		t.Fatalf("register 0: %v", err)
	}
	if err := p.RegisterReplica("sd", 1, b1.URL, nil, 0); err != nil {
		t.Fatalf("register 1: %v", err)
	}
	if !p.DrainReplica("sd", 1) {
		t.Fatal("expected replica 1 to be marked draining")
	}

	sd := p.PoolSessionSnapshot()["sd"]
	if sd.Replicas != 1 {
		t.Errorf("Replicas = %d, want 1 (draining replica excluded from admission ceiling)", sd.Replicas)
	}
	if sd.Cap != 10 {
		t.Errorf("Cap = %d, want 10", sd.Cap)
	}
}
