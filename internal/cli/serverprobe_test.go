package cli

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// probeServer accepts a healthy shinyhub server-info response (version +
// capabilities) and returns the parsed info.
func TestProbeServer_HealthyShinyhub(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"version":"1.2.3","capabilities":{"fleet_preconditions":true,"content_digest":true}}`))
	}))
	defer srv.Close()

	info, err := probeServer(&cliConfig{Host: srv.URL, Token: "shk_x"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if info.Version != "1.2.3" {
		t.Errorf("version = %q, want 1.2.3", info.Version)
	}
	if !info.Capabilities.ContentDigest {
		t.Errorf("capabilities not parsed: %+v", info.Capabilities)
	}
}

// A 401 (a front proxy on a half-provisioned box) is classified as
// server-not-ready, NOT as a transport/auth error.
func TestProbeServer_NotShinyhub401(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"detail":"No authentication token"}`))
	}))
	defer srv.Close()

	_, err := probeServer(&cliConfig{Host: srv.URL})
	var nr *serverNotReadyError
	if !errors.As(err, &nr) {
		t.Fatalf("want serverNotReadyError, got %v", err)
	}
}

// A 200 whose body is not a shinyhub server-info envelope is also not-ready.
func TestProbeServer_NotShinyhub200Garbage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"hello":"world"}`))
	}))
	defer srv.Close()

	_, err := probeServer(&cliConfig{Host: srv.URL})
	var nr *serverNotReadyError
	if !errors.As(err, &nr) {
		t.Fatalf("want serverNotReadyError for non-shinyhub 200, got %v", err)
	}
}

// An older shinyhub (capabilities but no version field) is still recognized as
// a healthy server, not misclassified as not-ready.
func TestProbeServer_PreVersionServerStillReady(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"capabilities":{"fleet_preconditions":true,"content_digest":true}}`))
	}))
	defer srv.Close()

	if _, err := probeServer(&cliConfig{Host: srv.URL}); err != nil {
		t.Fatalf("pre-version shinyhub must be ready, got %v", err)
	}
}

// A genuine transport failure (host unreachable) passes through as a plain
// error, NOT a serverNotReadyError - that case stays "transport/auth".
func TestProbeServer_TransportErrorNotMisclassified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // connections now refused

	_, err := probeServer(&cliConfig{Host: url})
	if err == nil {
		t.Fatal("expected a transport error")
	}
	var nr *serverNotReadyError
	if errors.As(err, &nr) {
		t.Fatalf("transport error must not be classified as not-ready: %v", err)
	}
}

// waitForServerReady polls until the server responds as a healthy shinyhub,
// retrying through not-ready responses, and returns the parsed info.
func TestWaitForServerReady_BecomesReady(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"version":"1.0.0"}`))
	}))
	defer srv.Close()

	clock := time.Unix(0, 0)
	now := func() time.Time { return clock }
	sleep := func(d time.Duration) { clock = clock.Add(d) }

	info, err := waitForServerReady(&cliConfig{Host: srv.URL}, time.Minute, time.Second, io.Discard, now, sleep)
	if err != nil {
		t.Fatalf("expected server to become ready, got %v", err)
	}
	if info.Version != "1.0.0" {
		t.Errorf("version = %q", info.Version)
	}
	if calls != 3 {
		t.Errorf("expected 3 probes, got %d", calls)
	}
}

// waitForServerReady gives up cleanly with a serverNotReadyError once the
// timeout elapses.
func TestWaitForServerReady_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	clock := time.Unix(0, 0)
	now := func() time.Time { return clock }
	sleep := func(d time.Duration) { clock = clock.Add(d) }

	_, err := waitForServerReady(&cliConfig{Host: srv.URL}, 5*time.Second, 2*time.Second, io.Discard, now, sleep)
	var nr *serverNotReadyError
	if !errors.As(err, &nr) {
		t.Fatalf("want serverNotReadyError on timeout, got %v", err)
	}
}

// A 404 on /api/server-info means an older shinyhub that predates the endpoint,
// not a half-provisioned host. serverReadinessProblem must treat it as
// inconclusive (nil) so a genuine /api/apps error is reported as transport/auth
// rather than being downgraded to "server not ready". This mirrors
// fetchServerCaps, which treats a missing endpoint as "supported (degraded)".
func TestServerReadinessProblem_404IsInconclusive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	if p := serverReadinessProblem(&cliConfig{Host: srv.URL}); p != nil {
		t.Errorf("a 404 (older shinyhub without the endpoint) must be inconclusive, got %v", p)
	}
}

// serverReadinessProblem flags a reachable-but-not-shinyhub host and clears a
// healthy one.
func TestServerReadinessProblem(t *testing.T) {
	notReady := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer notReady.Close()
	if serverReadinessProblem(&cliConfig{Host: notReady.URL}) == nil {
		t.Error("expected a not-ready problem for a non-shinyhub host")
	}

	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"version":"1.0.0","capabilities":{"content_digest":true}}`))
	}))
	defer healthy.Close()
	if serverReadinessProblem(&cliConfig{Host: healthy.URL}) != nil {
		t.Error("a healthy shinyhub must not be flagged not-ready")
	}
}
