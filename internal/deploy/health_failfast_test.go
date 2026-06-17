package deploy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A crashed process (dead endpoint) is detected via alive() and fails fast,
// rather than burning the full timeout polling a refused connection.
func TestWaitHealthyOrExit_FailsFastWhenProcessExited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // connection refused from here on - simulates a crashed app

	start := time.Now()
	err := waitHealthyOrExit(url, 30*time.Second, nil, func() bool { return false })
	elapsed := time.Since(start)

	if err == nil || !strings.Contains(err.Error(), "crashed on startup") {
		t.Fatalf("err = %v, want a crash error", err)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("took %v - should fail fast, not wait the 30s timeout", elapsed)
	}
}

func TestWaitHealthyOrExit_HealthyReturnsNil(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer srv.Close()
	if err := waitHealthyOrExit(srv.URL, 5*time.Second, nil, func() bool { return true }); err != nil {
		t.Fatalf("healthy endpoint: %v", err)
	}
}

// An alive-but-not-ready app (slow boot, 503) is NOT failed fast; it gets the
// full timeout, then times out with the timeout error (not the crash error).
func TestWaitHealthyOrExit_AliveButUnreadyTimesOut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer srv.Close()
	err := waitHealthyOrExit(srv.URL, 600*time.Millisecond, nil, func() bool { return true })
	if err == nil || !strings.Contains(err.Error(), "did not become healthy") {
		t.Fatalf("err = %v, want a timeout error", err)
	}
}
