package proxy_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/proxy"
)

// TestForwardErrorWake_ClusteredServesLoadingPage verifies that when
// forwardErrorWake is enabled (clustered mode) and a registered upstream is
// unreachable, the proxy serves the loading page (HTTP 200) and invokes the
// wake trigger rather than returning 502.
func TestForwardErrorWake_ClusteredServesLoadingPage(t *testing.T) {
	// Backend that always refuses connections: start then close immediately.
	closed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	closedURL := closed.URL
	closed.Close()

	p := proxy.New()
	p.SetForwardErrorWake(true)

	done := make(chan struct{})
	p.SetWakeTrigger(func(slug string) { close(done) })

	if err := p.Register("errapp", closedURL); err != nil {
		t.Fatalf("Register: %v", err)
	}

	req := httptest.NewRequest("GET", "/app/errapp/", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("clustered forward-error: expected 200 (loading page), got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Starting app") {
		t.Errorf("clustered forward-error: expected loading page body, got %q", rec.Body.String())
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("clustered forward-error: wake trigger must be called on upstream error")
	}
}

// TestForwardErrorWake_SingleNodeReturns502 verifies that when forwardErrorWake
// is false (single-node default), a forward error to an unreachable upstream
// returns 502 (byte-for-byte unchanged behaviour).
func TestForwardErrorWake_SingleNodeReturns502(t *testing.T) {
	closed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	closedURL := closed.URL
	closed.Close()

	p := proxy.New()
	// forwardErrorWake NOT set: single-node default.

	if err := p.Register("singleapp", closedURL); err != nil {
		t.Fatalf("Register: %v", err)
	}

	req := httptest.NewRequest("GET", "/app/singleapp/", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Errorf("single-node forward-error: expected 502, got %d", rec.Code)
	}
}
