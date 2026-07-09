package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
)

// wsUpgradeRequest builds a GET for the app carrying the client's cookies and
// WebSocket upgrade headers, the shape a scripted client (or a reconnecting
// browser) sends straight to a still-booting worker.
func wsUpgradeRequest(slug string, cookies []*http.Cookie) *http.Request {
	req := httptest.NewRequest("GET", "/app/"+slug+"/websocket/", nil)
	req.Header.Set("Cookie", cookieHeader(cookies))
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "keep-alive, Upgrade")
	return req
}

// TestElasticRouting_WSUpgradeParkedUntilWorkerReady verifies that a
// WebSocket upgrade pinned to a booting worker is held until the worker
// registers and is then forwarded to it, instead of being answered with the
// loading page (a non-101 that hard-fails WS clients, which cannot retry the
// way the splash's reload loop does).
func TestElasticRouting_WSUpgradeParkedUntilWorkerReady(t *testing.T) {
	oldTTL, oldInt := wsBootParkTTL, wsBootParkInterval
	wsBootParkTTL, wsBootParkInterval = 3*time.Second, 20*time.Millisecond
	t.Cleanup(func() { wsBootParkTTL, wsBootParkInterval = oldTTL, oldInt })

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ws-backend")) //nolint:errcheck
	}))
	defer backend.Close()

	const slug = "wsapp"
	p := New()
	p.SetPoolMode(slug, config.IsolationGrouped, 2, 2)
	p.SetSpawnFunc(func(string, int) {})

	// Cold client: binds to booting slot 0, receives the splash + cookies.
	req1 := httptest.NewRequest("GET", "/app/"+slug+"/", nil)
	rec1 := httptest.NewRecorder()
	p.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("cold request: want 200, got %d", rec1.Code)
	}
	cookies := extractCookies(rec1)

	// The worker becomes ready while the upgrade is parked.
	go func() {
		time.Sleep(150 * time.Millisecond)
		if err := p.RegisterElasticWorker(slug, 0, backend.URL, nil, 1); err != nil {
			t.Errorf("RegisterElasticWorker: %v", err)
		}
	}()

	rec2 := httptest.NewRecorder()
	start := time.Now()
	p.ServeHTTP(rec2, wsUpgradeRequest(slug, cookies))
	elapsed := time.Since(start)

	if rec2.Code != http.StatusOK {
		t.Fatalf("parked upgrade: want 200 from backend, got %d: %q", rec2.Code, rec2.Body.String())
	}
	if !strings.Contains(rec2.Body.String(), "ws-backend") {
		t.Fatalf("parked upgrade must be forwarded to the worker once ready, got %q", rec2.Body.String())
	}
	if elapsed < 100*time.Millisecond {
		t.Errorf("upgrade returned in %v; it should have been parked until the worker registered", elapsed)
	}
}

// TestElasticRouting_WSUpgradeParkTimeoutFallsBackToSplash pins the bound:
// when the worker never becomes ready within the park TTL, the upgrade falls
// back to today's behavior (the loading page), so a wedged boot cannot hold
// connections forever.
func TestElasticRouting_WSUpgradeParkTimeoutFallsBackToSplash(t *testing.T) {
	oldTTL, oldInt := wsBootParkTTL, wsBootParkInterval
	wsBootParkTTL, wsBootParkInterval = 150*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { wsBootParkTTL, wsBootParkInterval = oldTTL, oldInt })

	const slug = "wsapp2"
	p := New()
	p.SetPoolMode(slug, config.IsolationGrouped, 2, 2)
	p.SetSpawnFunc(func(string, int) {})

	req1 := httptest.NewRequest("GET", "/app/"+slug+"/", nil)
	rec1 := httptest.NewRecorder()
	p.ServeHTTP(rec1, req1)
	cookies := extractCookies(rec1)

	rec2 := httptest.NewRecorder()
	p.ServeHTTP(rec2, wsUpgradeRequest(slug, cookies))

	if rec2.Code != http.StatusOK {
		t.Fatalf("timed-out upgrade: want 200 loading page, got %d", rec2.Code)
	}
	if !strings.Contains(rec2.Body.String(), LoadingPageSentinel) {
		t.Errorf("timed-out upgrade must fall back to the loading page, got %q", rec2.Body.String())
	}
}
