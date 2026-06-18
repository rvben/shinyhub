package proxy_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/proxy"
)

// When a replica registers during the hold window (a warm/fast resume), the
// request is served INLINE from the backend - no loading page, no reload bounce.
func TestWakeHold_ServesInlineWhenReplicaRegistersDuringHold(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "INLINE_APP_BODY")
	}))
	defer backend.Close()

	p := proxy.New()
	p.SetWakeHoldTimeout(3 * time.Second)
	p.SetPoolSize("demo", 1) // pool exists but empty -> a miss until the wake registers a replica
	p.SetWakeTrigger(func(slug string) {
		time.Sleep(100 * time.Millisecond) // simulate the resume latency
		_ = p.RegisterReplica(slug, 0, backend.URL, nil, 0)
	})

	req := httptest.NewRequest(http.MethodGet, "/app/demo/", nil)
	rec := httptest.NewRecorder()
	start := time.Now()
	p.ServeHTTP(rec, req)
	elapsed := time.Since(start)

	if !strings.Contains(rec.Body.String(), "INLINE_APP_BODY") {
		t.Fatalf("expected the app served inline, got body: %q", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "shinyhub-box") {
		t.Error("served the loading page instead of holding for the inline resume")
	}
	if elapsed > time.Second {
		t.Errorf("hold took %v, want ~100ms (held only until the replica registered)", elapsed)
	}
}

// When the hold window expires without a replica (a genuinely slow cold boot),
// the request falls back to the loading page.
func TestWakeHold_FallsBackToLoadingPageOnTimeout(t *testing.T) {
	p := proxy.New()
	p.SetWakeHoldTimeout(150 * time.Millisecond)
	p.SetPoolSize("slow", 1) // empty pool, no replica ever registers

	req := httptest.NewRequest(http.MethodGet, "/app/slow/", nil)
	rec := httptest.NewRecorder()
	start := time.Now()
	p.ServeHTTP(rec, req)
	elapsed := time.Since(start)

	if !strings.Contains(rec.Body.String(), "shinyhub-box") {
		t.Errorf("expected the loading page after the hold expired, got: %q", rec.Body.String())
	}
	if elapsed < 150*time.Millisecond {
		t.Errorf("served the loading page after %v, want >= the 150ms hold", elapsed)
	}
}

// A client that disconnects during the hold releases it immediately, instead of
// pinning the connection until the deadline.
func TestWakeHold_ReleasesOnClientDisconnect(t *testing.T) {
	p := proxy.New()
	p.SetWakeHoldTimeout(10 * time.Second) // a long grace that must be cut short
	p.SetPoolSize("slow", 1)               // never registers a replica

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/app/slow/", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	go func() { time.Sleep(100 * time.Millisecond); cancel() }() // client gives up

	start := time.Now()
	p.ServeHTTP(rec, req)
	elapsed := time.Since(start)

	if elapsed > 2*time.Second {
		t.Errorf("hold did not release on client disconnect: took %v (want ~100ms)", elapsed)
	}
}

// A crashed app must NOT be held for the wake grace: it will not come up, so it
// gets the crash page immediately.
func TestWakeHold_CrashedNotHeld(t *testing.T) {
	p := proxy.New()
	p.SetWakeHoldTimeout(5 * time.Second) // a long grace that must be skipped
	p.SetPoolSize("broke", 1)
	p.SetAppStatusLookup(func(_ string) (string, string) { return "crashed", "boom" })

	req := httptest.NewRequest(http.MethodGet, "/app/broke/", nil)
	rec := httptest.NewRecorder()
	start := time.Now()
	p.ServeHTTP(rec, req)
	elapsed := time.Since(start)

	if elapsed > 500*time.Millisecond {
		t.Errorf("a crashed app was held for %v; it must not be held for the wake grace", elapsed)
	}
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "This app crashed") {
		t.Errorf("expected the crash page (503), got %d: %q", rec.Code, rec.Body.String())
	}
}
