package cloudflaredemo

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// isNavigation mirrors the header shape classifyColdRequest treats as a
// browser opening the demo (src/edge-policy.ts, isNavigation): a
// Sec-Fetch-Dest of "document". A bare curl sends neither this header nor any
// Sec-Fetch-* header at all, so it is never mistaken for one.
func isNavigation(r *http.Request) bool {
	return r.Header.Get("Sec-Fetch-Dest") == "document"
}

// newDemoEdgeMock reproduces just enough of the Worker's edge behaviour for
// scripts/demo-smoke.sh to run against it: an entry page that always answers,
// a container that only becomes healthy in response to a browser-navigation
// style request (matching classifyColdRequest's "wake" verdict), and every
// other path refusing while unhealthy. It forces exactly one unhealthy
// transition on the FIRST call to /api/server-info, after the script's own
// wake() has already reported the container awake - the shape of a rollout
// swapping the container out from under a smoke test that already passed its
// earlier checks. Recovery requires a second navigation-style request; a bare
// status re-check can never do it, matching classifyColdRequest's "refuse"
// verdict for any path outside "/" and "/login".
func newDemoEdgeMock(t *testing.T, version string) (*httptest.Server, *atomic.Int32) {
	t.Helper()

	var healthy atomic.Bool
	var navCount atomic.Int32
	var serverInfoCalls atomic.Int32

	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if isNavigation(r) {
			navCount.Add(1)
			healthy.Store(true)
		}
		if !healthy.Load() {
			w.Header().Set("X-Shinyhub-Demo-State", "asleep")
			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, "<html>asleep, open /__demo/start to begin</html>")
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "<html>already awake</html>")
	})

	mux.HandleFunc("/__demo/ready", func(w http.ResponseWriter, r *http.Request) {
		if !healthy.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if !healthy.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		if !healthy.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "<html><link rel=\"stylesheet\" href=\"/__demo/assets/v1/login.css\">"+
			"<form action=\"/__demo/session\" method=\"post\"></form>"+
			"<script src=\"/__demo/assets/v1/login.js\"></script></html>")
	})

	mux.HandleFunc("/__demo/assets/v1/login.css", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/__demo/assets/v1/login.js", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("/__demo/start", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusSeeOther)
	})

	mux.HandleFunc("/__demo/session", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "shiny_session", Value: "mock-session", Path: "/"})
		w.WriteHeader(http.StatusSeeOther)
	})

	mux.HandleFunc("/api/auth/me", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"username":"demo-viewer","role":"viewer","display_name":"Demo Viewer"}`)
	})

	// The FIRST call always lands on a container the smoke test already
	// proved awake in the checks above - this is the drop, not the initial
	// cold start. Every call after that reads the current state, which only
	// a fresh navigation-style request (nudge) can bring back: it restarts the
	// container, and the Worker forwards the next call to the running one.
	mux.HandleFunc("/api/server-info", func(w http.ResponseWriter, r *http.Request) {
		if serverInfoCalls.Add(1) == 1 {
			healthy.Store(false)
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if !healthy.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"version":"%s","protocol_version":1}`, version)
	})

	mux.HandleFunc("/app/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server, &navCount
}

// TestDemoSmokeRecoversFromPostWakeUnhealthyWindow is the regression test for
// the root cause behind demo.yml's "Smoke-test the deployed release" failures:
// every retry loop in scripts/demo-smoke.sh polled with a bare curl, and a
// bare curl to anything but "/" or "/login" is refused outright by the edge
// (classifyColdRequest, src/edge-policy.ts) while the container is unhealthy,
// which never starts it. Once wake() had already succeeded once, there was no
// request left in the script that could recover from the container dropping
// unhealthy again - as a rollout swap onto the just-deployed image does. This
// runs the real script against a mock that forces exactly that drop right as
// the version check begins, and requires the script's own retries to recover
// without any help beyond what it already sends for the first wake.
func TestDemoSmokeRecoversFromPostWakeUnhealthyWindow(t *testing.T) {
	const version = "9.9.9-smoke-test"
	server, navCount := newDemoEdgeMock(t, version)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "../../scripts/demo-smoke.sh")
	cmd.Env = append(cmd.Environ(),
		"SHINYHUB_DEMO_URL="+server.URL,
		"SHINYHUB_DEMO_APP_URL="+server.URL,
		"SHINYHUB_DEMO_EXPECTED_VERSION="+version,
		"SHINYHUB_DEMO_SMOKE_ATTEMPTS=5",
		"SHINYHUB_DEMO_SMOKE_RETRY_DELAY=0",
	)
	output, err := cmd.CombinedOutput()
	t.Logf("demo-smoke.sh output:\n%s", output)
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		t.Fatalf("run demo-smoke.sh: %v", err)
	}
	// A later step (the WebSocket smoke, which this mock does not implement)
	// is allowed to fail - it is out of scope for this test. What matters
	// happened earlier: assert on that below rather than on the script's
	// final exit code.

	got := string(output)
	successLine := fmt.Sprintf("-> version %s", version)
	failureLine := fmt.Sprintf("expected version %s after", version)

	if strings.Contains(got, failureLine) {
		t.Fatalf("demo-smoke.sh gave up waiting for version %s instead of recovering; output:\n%s", version, got)
	}
	if !strings.Contains(got, successLine) {
		t.Fatalf("demo-smoke.sh never reported %q; output:\n%s", successLine, got)
	}
	if n := navCount.Load(); n < 2 {
		t.Fatalf("expected at least 2 navigation-style requests (initial wake + recovery nudge), got %d", n)
	}
}
