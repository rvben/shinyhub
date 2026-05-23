package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// stepClock returns a now() that advances by step on each call, so a wait loop
// that calls now() once per iteration sees deterministic elapsed time without
// real sleeping.
func stepClock(step time.Duration) func() time.Time {
	base := time.Unix(0, 0)
	var n int64
	return func() time.Time {
		t := base.Add(time.Duration(n) * step)
		n++
		return t
	}
}

// FLT-7: a long health wait must emit periodic progress lines (naming the app
// and elapsed/timeout) rather than appearing hung, and must still time out.
func TestFleetHealthLoop_ProgressLinesWhileWaiting(t *testing.T) {
	var buf bytes.Buffer
	poll := func() (bool, string, error) { return false, "starting", nil }
	err := waitForFleetHealthLoop("demo", 120*time.Second, 2*time.Second, 30*time.Second,
		poll, stepClock(2*time.Second), func(time.Duration) {}, &buf)
	if err == nil {
		t.Fatal("an app that never becomes healthy must time out")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("timeout error = %v, want it to say 'timed out'", err)
	}
	out := buf.String()
	if n := strings.Count(out, "demo"); n < 2 {
		t.Fatalf("want repeated progress lines naming the app, got %d:\n%s", n, out)
	}
	if !strings.Contains(out, "/2m0s") {
		t.Fatalf("progress line must show elapsed/timeout, got:\n%s", out)
	}
}

// FLT-7: the loop returns as soon as the app reports ready and stops polling.
func TestFleetHealthLoop_ReturnsReadyAndStops(t *testing.T) {
	var buf bytes.Buffer
	var calls int
	poll := func() (bool, string, error) {
		calls++
		return calls >= 3, "starting", nil
	}
	err := waitForFleetHealthLoop("demo", 120*time.Second, time.Second, 30*time.Second,
		poll, stepClock(time.Second), func(time.Duration) {}, &buf)
	if err != nil {
		t.Fatalf("ready app must return nil, got %v", err)
	}
	if calls != 3 {
		t.Fatalf("loop must stop on first ready poll, got %d calls", calls)
	}
	if !strings.Contains(buf.String(), "healthy") {
		t.Fatalf("ready line must confirm health, got:\n%s", buf.String())
	}
}

// FLT-7: a terminal startup failure (crashed) fails fast without burning the
// full timeout.
func TestFleetHealthLoop_TerminalStatusFailsFast(t *testing.T) {
	var buf bytes.Buffer
	var calls int
	poll := func() (bool, string, error) {
		calls++
		return false, "crashed", nil
	}
	err := waitForFleetHealthLoop("demo", 120*time.Second, time.Second, 30*time.Second,
		poll, stepClock(time.Second), func(time.Duration) {}, &buf)
	if err == nil || !strings.Contains(err.Error(), "crashed") {
		t.Fatalf("crashed app must fail with a crashed error, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("terminal status must fail on the first poll, got %d calls", calls)
	}
}

// FLT-7: --health-timeout seconds convert to a duration; a non-positive value
// falls back to the generous fleet default so the flag can't disable the wait.
func TestHealthTimeoutDuration(t *testing.T) {
	if got := healthTimeoutDuration(240); got != 240*time.Second {
		t.Fatalf("healthTimeoutDuration(240) = %v, want 4m0s", got)
	}
	if got := healthTimeoutDuration(0); got != fleetHealthTimeout {
		t.Fatalf("healthTimeoutDuration(0) = %v, want fleet default %v", got, fleetHealthTimeout)
	}
	if got := healthTimeoutDuration(-5); got != fleetHealthTimeout {
		t.Fatalf("healthTimeoutDuration(-5) = %v, want fleet default %v", got, fleetHealthTimeout)
	}
}

// FLT-7: fleet apply exposes a --health-timeout flag so an operator can bound
// or extend the per-app health wait.
func TestFleetApplyCmd_HasHealthTimeoutFlag(t *testing.T) {
	cmd := newFleetApplyCmd()
	f := cmd.Flags().Lookup("health-timeout")
	if f == nil {
		t.Fatal("fleet apply must expose a --health-timeout flag")
	}
	if f.DefValue != "120" {
		t.Fatalf("--health-timeout default = %q, want 120", f.DefValue)
	}
}

func TestDeployAppBundle_DeploysThenReadsPromotedDigest(t *testing.T) {
	var deployHits, listHits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/apps/demo":
			// First GET = ensureApp existence check AND health poll.
			// Return running so the poll completes; include the digest
			// only after a deploy has happened.
			if atomic.LoadInt32(&deployHits) > 0 {
				atomic.AddInt32(&listHits, 1)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"app": map[string]any{"status": "running"},
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"app": map[string]any{"status": "running"},
			})
		case r.Method == "POST" && r.URL.Path == "/api/apps/demo/deploy":
			atomic.AddInt32(&deployHits, 1)
			if r.Header.Get("X-Shinyhub-Run-Id") == "" {
				t.Error("deploy missing run id header")
			}
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case r.Method == "GET" && r.URL.Path == "/api/apps":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"slug": "demo", "access": "private", "content_digest": "sha256:PROMOTED"},
			})
		default:
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "app.py"), "print(1)\n")
	cfg := &cliConfig{Host: srv.URL, Token: "shk_test"}

	dg, committed, err := deployAppBundle(cfg, "demo", dir, "private", io.Discard, "run-1", 5*time.Second)
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if !committed {
		t.Fatal("a successful deploy must report committed=true")
	}
	if dg != "sha256:PROMOTED" {
		t.Fatalf("promoted digest = %q, want sha256:PROMOTED", dg)
	}
	if atomic.LoadInt32(&deployHits) == 0 {
		t.Fatal("deploy endpoint never called")
	}
	if atomic.LoadInt32(&listHits) == 0 {
		t.Fatal("post-deploy health poll never reached the running-state branch")
	}
}

func TestDeployAppBundle_ClientRejectionIsNotCommitted(t *testing.T) {
	// A 4xx is a clean validation rejection: the server refused the request
	// before promoting anything, so committed=false (caller may roll back).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/deploy") {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":"bundle rejected"}`))
			return
		}
		if r.Method == "GET" && r.URL.Path == "/api/apps/demo" {
			w.WriteHeader(200) // app already exists; skip create
			_, _ = w.Write([]byte(`{"app":{"slug":"demo"}}`))
			return
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "app.py"), "print(1)\n")
	cfg := &cliConfig{Host: srv.URL, Token: "shk_test"}
	_, committed, err := deployAppBundle(cfg, "demo", dir, "", io.Discard, "r", 5*time.Second)
	if err == nil {
		t.Fatal("expected deploy failure to propagate")
	}
	if committed {
		t.Fatal("a deploy rejected with HTTP 4xx must report committed=false")
	}
}

func TestDeployAppBundle_ServerErrorIsNotCommitted(t *testing.T) {
	// A 5xx is ambiguous at the deploy layer: the handler returns 500 both
	// before promotion (BeginDeployment, quota) and after it (PromoteDeployment
	// record / schedule apply). committed therefore stays false - only a 2xx is
	// known-committed - and callers that care (adopt) resolve whether the
	// bundle actually went live with a digest readback.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/deploy") {
			w.WriteHeader(500)
			_, _ = w.Write([]byte(`{"error":"deploy succeeded but recording it failed; retry to commit"}`))
			return
		}
		if r.Method == "GET" && r.URL.Path == "/api/apps/demo" {
			w.WriteHeader(200) // app already exists; skip create
			_, _ = w.Write([]byte(`{"app":{"slug":"demo"}}`))
			return
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "app.py"), "print(1)\n")
	cfg := &cliConfig{Host: srv.URL, Token: "shk_test"}
	_, committed, err := deployAppBundle(cfg, "demo", dir, "", io.Discard, "r", 5*time.Second)
	if err == nil {
		t.Fatal("expected deploy failure to propagate")
	}
	if committed {
		t.Fatal("only a 2xx deploy is known-committed; a 5xx must report committed=false")
	}
}
