package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/rvben/shinyhub/internal/config"
)

// TestMemoryGuard_BlocksNewAllocationWhenBelowFloor verifies that when the
// host reports less available memory than the configured floor, a fresh
// client that would need a NEW worker is shed with 503 + Retry-After and the
// dedicated memory-pressure reject reason, and no worker is reserved or
// spawned. Shedding one incoming session is deliberate: the alternative is
// the kernel OOM-killing a live worker together with every session on it.
func TestMemoryGuard_BlocksNewAllocationWhenBelowFloor(t *testing.T) {
	var spawns atomic.Int32
	p := New()
	p.SetPoolMode("myapp", config.IsolationPerSession, 0, 4)
	p.SetSpawnFunc(func(string, int) { spawns.Add(1) })
	p.SetMemoryGuard(512, func() (int, bool) { return 100, true })

	req := httptest.NewRequest("GET", "/app/myapp/", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 under memory pressure, got %d", rec.Code)
	}
	if ra := rec.Header().Get("Retry-After"); ra == "" {
		t.Error("expected Retry-After header on the memory-pressure 503")
	}
	if got := rec.Header().Get("X-Shinyhub-Reject"); got != string(ReasonMemoryPressure) {
		t.Errorf("X-Shinyhub-Reject = %q, want %q (distinct from pool-saturated so capacity automation does not scale up)", got, ReasonMemoryPressure)
	}
	if !strings.Contains(rec.Body.String(), MsgPoolSaturated) {
		t.Errorf("expected %q body, got %q", MsgPoolSaturated, rec.Body.String())
	}
	if n := spawns.Load(); n != 0 {
		t.Errorf("spawn called %d times; a shed request must not spawn", n)
	}
}

// TestMemoryGuard_ExistingClientUnaffected verifies the floor only gates NEW
// worker allocation: a client that already holds a slot keeps being served
// (loading page while its worker boots), while a fresh client is shed.
func TestMemoryGuard_ExistingClientUnaffected(t *testing.T) {
	memLow := atomic.Bool{}
	p := New()
	p.SetPoolMode("myapp", config.IsolationPerSession, 0, 4)
	p.SetSpawnFunc(func(string, int) {})
	p.SetMemoryGuard(512, func() (int, bool) {
		if memLow.Load() {
			return 100, true
		}
		return 4096, true
	})

	// Client A allocates while memory is fine.
	req1 := httptest.NewRequest("GET", "/app/myapp/", nil)
	rec1 := httptest.NewRecorder()
	p.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("first client: want 200 loading page, got %d", rec1.Code)
	}

	memLow.Store(true)

	// Client A retries with its cookies: routed (loading page), never shed.
	req2 := httptest.NewRequest("GET", "/app/myapp/", nil)
	req2.Header.Set("Cookie", cookieHeader(extractCookies(rec1)))
	rec2 := httptest.NewRecorder()
	p.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("pinned client under memory pressure: want 200, got %d", rec2.Code)
	}

	// A fresh client needs a new worker: shed.
	req3 := httptest.NewRequest("GET", "/app/myapp/", nil)
	rec3 := httptest.NewRecorder()
	p.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusServiceUnavailable {
		t.Fatalf("fresh client under memory pressure: want 503, got %d", rec3.Code)
	}
}

// TestMemoryGuard_FailsOpenWhenProbeUnavailable verifies that a probe that
// cannot produce a reading (ok=false) never blocks admission.
func TestMemoryGuard_FailsOpenWhenProbeUnavailable(t *testing.T) {
	var spawns atomic.Int32
	p := New()
	p.SetPoolMode("myapp", config.IsolationPerSession, 0, 4)
	p.SetSpawnFunc(func(string, int) { spawns.Add(1) })
	p.SetMemoryGuard(512, func() (int, bool) { return 0, false })

	req := httptest.NewRequest("GET", "/app/myapp/", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("probe failure must fail open: want 200 loading page, got %d", rec.Code)
	}
}

// TestMemoryGuard_ExactFloorAllows pins the boundary: available == floor is
// NOT pressure; only strictly below the floor sheds.
func TestMemoryGuard_ExactFloorAllows(t *testing.T) {
	p := New()
	p.SetPoolMode("myapp", config.IsolationPerSession, 0, 4)
	p.SetSpawnFunc(func(string, int) {})
	p.SetMemoryGuard(512, func() (int, bool) { return 512, true })

	req := httptest.NewRequest("GET", "/app/myapp/", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("available == floor must allocate: want 200, got %d", rec.Code)
	}
}

// TestMemoryGuard_ClearedByNonPositiveFloor verifies SetMemoryGuard with a
// zero floor disables the guard entirely (the probe is never consulted).
func TestMemoryGuard_ClearedByNonPositiveFloor(t *testing.T) {
	var probed atomic.Int32
	p := New()
	p.SetPoolMode("myapp", config.IsolationPerSession, 0, 4)
	p.SetSpawnFunc(func(string, int) {})
	p.SetMemoryGuard(512, func() (int, bool) { probed.Add(1); return 0, true })
	p.SetMemoryGuard(0, func() (int, bool) { probed.Add(1); return 0, true })

	req := httptest.NewRequest("GET", "/app/myapp/", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("guard cleared: want 200, got %d", rec.Code)
	}
	if n := probed.Load(); n != 0 {
		t.Errorf("probe consulted %d times after the guard was cleared", n)
	}
}
