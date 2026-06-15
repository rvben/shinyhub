package proxy_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/proxy"
)

// TestProxy_UpstreamErrorWakesAndServesLoadingPage verifies that a pre-response
// upstream error (the registered replica is unreachable: hibernated, stopped, or
// died between pool registration and this request) makes the proxy trigger a
// wake and serve the loading page (HTTP 200) so the client retries while the
// replica is (re)started, instead of a dead-end 502.
//
// This recovery is unconditional. It was previously gated to clustered mode,
// which left single-node deployments returning a *permanent* 502: the wake was
// never triggered, so the dead replica was never restarted and every subsequent
// request 502'd until manual intervention.
func TestProxy_UpstreamErrorWakesAndServesLoadingPage(t *testing.T) {
	// A backend that refuses connections: start then close immediately so the
	// reverse proxy's dial fails (a pre-response upstream error).
	closed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	closedURL := closed.URL
	closed.Close()

	p := proxy.New() // no clustered wiring: this is the single-node default

	woke := make(chan string, 1)
	p.SetWakeTrigger(func(slug string) { woke <- slug })

	if err := p.Register("errapp", closedURL); err != nil {
		t.Fatalf("Register: %v", err)
	}

	req := httptest.NewRequest("GET", "/app/errapp/", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("upstream error: expected 200 (loading page), got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Starting app") {
		t.Errorf("upstream error: expected loading page body, got %q", rec.Body.String())
	}
	select {
	case slug := <-woke:
		if slug != "errapp" {
			t.Errorf("wake trigger slug = %q, want errapp", slug)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("upstream error: wake trigger must fire so the dead replica is (re)started")
	}
}
