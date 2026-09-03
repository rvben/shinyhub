package cli

import (
	"archive/zip"
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

	"github.com/rvben/shinyhub/internal/bundle"
	"github.com/rvben/shinyhub/internal/deployfail"
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

func TestFleetHealthLoop_MeasuresDeadlineAfterSlowPollAndCapsSleep(t *testing.T) {
	base := time.Unix(0, 0)
	nowValue := base
	now := func() time.Time { return nowValue }
	var sleeps []time.Duration
	poll := func() (bool, string, error) {
		nowValue = nowValue.Add(9 * time.Second)
		return false, "starting", nil
	}
	sleep := func(d time.Duration) {
		sleeps = append(sleeps, d)
		nowValue = nowValue.Add(d)
	}
	err := waitForFleetHealthLoop("demo", 10*time.Second, 2*time.Second, time.Hour, poll, now, sleep, io.Discard)
	if err == nil {
		t.Fatal("want timeout")
	}
	if len(sleeps) != 1 || sleeps[0] != time.Second {
		t.Fatalf("sleeps = %v, want exactly the one remaining second", sleeps)
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
	if strings.Contains(err.Error(), "during startup") {
		t.Fatalf("the generic gate must not invent a startup crash: %v", err)
	}
	if calls != 1 {
		t.Fatalf("terminal status must fail on the first poll, got %d calls", calls)
	}
}

func TestFleetHealthFailure_AttributesPreexistingUnchangedCrash(t *testing.T) {
	var observation appHealthObservation
	if err := json.Unmarshal([]byte(`{
		"app":{"status":"crashed","last_deployed_at":"2026-08-26T23:14:00Z"},
		"replicas_status":[{"index":0,"status":"crashed","last_exit":{
			"observed_at":"2026-08-27T16:26:27Z","run_id":"run-dead",
			"exit_signal":"SIGKILL","exit_reason":"kernel OOM-killed replica",
			"oom_killed":true,"crash_count":3}}]
	}`), &observation); err != nil {
		t.Fatal(err)
	}
	err := fleetHealthFailure("demo", "unchanged", time.Date(2026, 8, 27, 17, 0, 51, 0, time.UTC), observation)
	got := err.Error()
	for _, want := range []string{
		"34m24s before this apply", "app unchanged", "this apply did not start it",
		"OOM-killed", `reason="kernel OOM-killed replica"`, "crash_observations=3",
		"--run run-dead",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("error missing %q: %s", want, got)
		}
	}
	if strings.Contains(got, "during startup") {
		t.Errorf("error invents startup timing: %s", got)
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

// FLT-SCH: fleet apply exposes a --wait-for-warm flag so an operator can block
// until deploy-triggered runs complete within the separate warm timeout.
func TestFleetApplyCmd_HasWaitForWarmFlag(t *testing.T) {
	cmd := newFleetApplyCmd()
	f := cmd.Flags().Lookup("wait-for-warm")
	if f == nil {
		t.Fatal("fleet apply must expose a --wait-for-warm flag")
	}
	if f.DefValue != "false" {
		t.Fatalf("--wait-for-warm default = %q, want false", f.DefValue)
	}
}

func TestFleetApplyCmd_HasWarmTimeoutFlag(t *testing.T) {
	cmd := newFleetApplyCmd()
	f := cmd.Flags().Lookup("warm-timeout")
	if f == nil {
		t.Fatal("fleet apply must expose a --warm-timeout flag")
	}
	if f.DefValue != "15m0s" {
		t.Fatalf("--warm-timeout default = %q, want 15m0s", f.DefValue)
	}
}

func TestFleetApplyCmd_HasVerifySchedulesFlag(t *testing.T) {
	cmd := newFleetApplyCmd()
	f := cmd.Flags().Lookup("verify-schedules")
	if f == nil {
		t.Fatal("fleet apply must expose a --verify-schedules flag")
	}
	if f.DefValue != "false" {
		t.Fatalf("--verify-schedules default = %q, want false", f.DefValue)
	}
}

func TestFleetApplyCmd_HasVerifyHealthFlag(t *testing.T) {
	cmd := newFleetApplyCmd()
	f := cmd.Flags().Lookup("verify-health")
	if f == nil || f.DefValue != "false" {
		t.Fatalf("--verify-health = %#v, want an opt-in flag", f)
	}
}

func TestFleetApplyCmd_HasConcurrencyFlag(t *testing.T) {
	cmd := newFleetApplyCmd()
	f := cmd.Flags().Lookup("concurrency")
	if f == nil {
		t.Fatal("fleet apply must expose a --concurrency flag")
	}
	if f.DefValue != "3" {
		t.Fatalf("--concurrency default = %q, want 3", f.DefValue)
	}
}

func TestValidateConcurrency(t *testing.T) {
	for _, ok := range []int{1, 3, 64} {
		if err := validateConcurrency(ok); err != nil {
			t.Errorf("concurrency %d must be valid, got %v", ok, err)
		}
	}
	for _, bad := range []int{0, -1} {
		if err := validateConcurrency(bad); err == nil {
			t.Errorf("concurrency %d must be rejected", bad)
		}
	}
}

// FLT-11: fleet reconciles visibility through its own config-drift path, so
// the deploy-layer "--visibility ignored for existing apps" warning is noise
// in the fleet context (and leaked once per retry). ensureFleetApp must not
// emit it for an app that already exists.
func TestEnsureFleetApp_NoVisibilityWarningForExistingApp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// GET = app exists; nothing else should be hit.
		if r.Method == "GET" && r.URL.Path == "/api/apps/demo" {
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"app":{"slug":"demo","status":"running"}}`))
			return
		}
		if r.Method == "POST" && r.URL.Path == "/api/apps" {
			t.Error("create must not be called for an existing app")
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	var buf bytes.Buffer
	cfg := &cliConfig{Host: srv.URL, Token: "shk_test"}
	if err := ensureFleetApp(cfg, "demo", "private", "", &buf); err != nil {
		t.Fatalf("ensureFleetApp: %v", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("fleet ensure must not warn for existing apps, got: %q", buf.String())
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

	dg, committed, _, _, err := deployAppBundle(cfg, "demo", dir, "private", "", io.Discard, "run-1", 5*time.Second)
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

func TestDeployAppBundleFromSpec_UploadsSharedSnapshot(t *testing.T) {
	var uploaded []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/api/apps/demo/deploy":
			if err := r.ParseMultipartForm(2 << 20); err != nil {
				t.Errorf("ParseMultipartForm: %v", err)
				w.WriteHeader(400)
				return
			}
			f, _, err := r.FormFile("bundle")
			if err != nil {
				t.Errorf("FormFile: %v", err)
				w.WriteHeader(400)
				return
			}
			data, _ := io.ReadAll(f)
			_ = f.Close()
			zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
			if err != nil {
				t.Errorf("zip.NewReader: %v", err)
				w.WriteHeader(400)
				return
			}
			for _, entry := range zr.File {
				uploaded = append(uploaded, entry.Name)
			}
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case r.Method == "GET" && r.URL.Path == "/api/apps":
			_, _ = w.Write([]byte(`[{"slug":"demo","content_digest":"sha256:PROMOTED"}]`))
		case r.Method == "GET" && r.URL.Path == "/api/apps/demo":
			_, _ = w.Write([]byte(`{"app":{"status":"running"}}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "app.py"), "print(1)\n")
	_, _, _, _, err := deployAppBundleFromSpec(&cliConfig{Host: srv.URL, Token: "shk_test"}, "demo", bundleBuildSpec{
		Dir:    dir,
		Inputs: []bundle.FileInputSnapshot{{From: "_shared/theme.py", To: "helpers/theme.py", Mode: 0o644, Data: []byte("orange\n")}},
	}, "private", "", io.Discard, "run-1", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(uploaded, "helpers/theme.py") {
		t.Fatalf("uploaded entries = %v", uploaded)
	}
}

// A fleet deploy to an app the operator stopped leaves it stopped, so it will
// never report healthy. Waiting for health could only run to the deadline and
// then fail - and, through deployWithRetry, re-run - a deploy the server
// already accepted. TestDeployAppBundle_DeploysThenReadsPromotedDigest is the
// positive control: it asserts the same poll DOES happen for a live deploy, so
// an implementation that never polls cannot pass both.
func TestDeployAppBundle_KeptStoppedSkipsHealthWait(t *testing.T) {
	var deployHits, pollsAfterDeploy int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/api/apps/demo/deploy":
			atomic.AddInt32(&deployHits, 1)
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"status":"ok","kept_stopped":true}`))
		case r.Method == "GET" && r.URL.Path == "/api/apps/demo":
			// Also the ensureApp existence check, so only GETs after the
			// deploy landed can be health polls.
			if atomic.LoadInt32(&deployHits) > 0 {
				atomic.AddInt32(&pollsAfterDeploy, 1)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"app": map[string]any{"status": "stopped"},
			})
		case r.Method == "GET" && r.URL.Path == "/api/apps":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"slug": "demo", "content_digest": "sha256:X"},
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

	var buf bytes.Buffer
	// readPromotedDigest re-GETs /api/apps, not /api/apps/demo, so a nonzero
	// pollsAfterDeploy can only come from the health wait.
	dg, committed, _, _, err := deployAppBundle(cfg, "demo", dir, "private", "", &buf, "run-1", time.Second)
	if err != nil {
		t.Fatalf("a kept-stopped deploy must succeed, got: %v", err)
	}
	if !committed {
		t.Error("a kept-stopped deploy is still committed: the server accepted the bundle")
	}
	if dg != "sha256:X" {
		t.Errorf("promoted digest = %q, want sha256:X", dg)
	}
	if n := atomic.LoadInt32(&pollsAfterDeploy); n != 0 {
		t.Errorf("health polls after a kept-stopped deploy = %d, want 0", n)
	}
	if out := buf.String(); !strings.Contains(out, "not serving yet") || !strings.Contains(out, "apps start demo") {
		t.Errorf("fleet deploy must report the app is stopped and name the start command, got:\n%s", out)
	}
}

// The single-app `deploy` surfaces a hooks-skipped warning when the server
// reports post-deploy hooks were skipped under the container runtime. The
// fleet deploy path must do the same so a `fleet apply` operator is not left
// silently unaware that their setup hooks never ran.
func TestDeployAppBundle_EmitsHooksSkippedWarning(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/api/apps/demo/deploy":
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"status":"ok","hooks_skipped":2}`))
		case r.Method == "GET" && r.URL.Path == "/api/apps/demo":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"app": map[string]any{"status": "running"},
			})
		case r.Method == "GET" && r.URL.Path == "/api/apps":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"slug": "demo", "content_digest": "sha256:X"},
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

	var buf bytes.Buffer
	if _, _, _, _, err := deployAppBundle(cfg, "demo", dir, "private", "", &buf, "run-1", 5*time.Second); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "post-deploy") || !strings.Contains(out, "skipped") {
		t.Fatalf("fleet deploy must surface the hooks-skipped warning, got:\n%s", out)
	}
}

// The single-app `deploy` surfaces a warning when a manifest icon shadows an
// already-uploaded image. The fleet deploy path must do the same: a fleet
// operator, who is the most likely person to have uploaded that image, would
// otherwise have no way to learn it is still retained.
func TestDeployAppBundle_EmitsIconShadowWarning(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/api/apps/demo/deploy":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "ok",
				"manifest": map[string]any{
					"icon_shadowed_upload": true,
					"app":                  map[string]any{"icon": "\U0001F4CA"},
				},
			})
		case r.Method == "GET" && r.URL.Path == "/api/apps/demo":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"app": map[string]any{"status": "running"},
			})
		case r.Method == "GET" && r.URL.Path == "/api/apps":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"slug": "demo", "content_digest": "sha256:X"},
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

	var buf bytes.Buffer
	if _, _, _, _, err := deployAppBundle(cfg, "demo", dir, "private", "", &buf, "run-1", 5*time.Second); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "still stored") {
		t.Fatalf("fleet deploy must surface the icon-shadow warning, got:\n%s", out)
	}
}

// The counterpart to TestDeployAppBundle_EmitsIconShadowWarning: no manifest
// icon was declared, so the flag is absent and no warning line should print.
func TestDeployAppBundle_NoIconShadowWarningWhenFlagAbsent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/api/apps/demo/deploy":
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case r.Method == "GET" && r.URL.Path == "/api/apps/demo":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"app": map[string]any{"status": "running"},
			})
		case r.Method == "GET" && r.URL.Path == "/api/apps":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"slug": "demo", "content_digest": "sha256:X"},
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

	var buf bytes.Buffer
	if _, _, _, _, err := deployAppBundle(cfg, "demo", dir, "private", "", &buf, "run-1", 5*time.Second); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if out := buf.String(); strings.Contains(out, "still stored") {
		t.Fatalf("no icon-shadow warning expected when the flag is absent, got:\n%s", out)
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
	_, committed, _, _, err := deployAppBundle(cfg, "demo", dir, "", "", io.Discard, "r", 5*time.Second)
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
	_, committed, _, _, err := deployAppBundle(cfg, "demo", dir, "", "", io.Discard, "r", 5*time.Second)
	if err == nil {
		t.Fatal("expected deploy failure to propagate")
	}
	if committed {
		t.Fatal("only a 2xx deploy is known-committed; a 5xx must report committed=false")
	}
}

// FLT-SCH: deployAppBundle parses deploy-triggered run refs from the deploy response so
// fleet apply can report and optionally wait for deploy-triggered schedule runs.
func TestDeployAppBundle_ReturnsDeployRunRefs(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/apps/warmapp", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"app":{"status":"running"}}`))
	})
	mux.HandleFunc("/api/apps/warmapp/deploy", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"deploy_count":1,"manifest":{"schedules":[{"name":"warm","action":"created","schedule_id":5,"deploy_run":{"run_id":42}}]}}`))
	})
	mux.HandleFunc("/api/apps", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"slug": "warmapp", "content_digest": "sha256:warm"},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cfg := &cliConfig{Host: srv.URL, Token: "t"}
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "app.py"), "print(1)\n")
	var out bytes.Buffer
	_, committed, refs, _, err := deployAppBundle(cfg, "warmapp", dir, "", "", &out, "run-1", time.Minute)
	if err != nil {
		t.Fatalf("deployAppBundle: %v", err)
	}
	if !committed {
		t.Errorf("committed = false, want true")
	}
	if len(refs) != 1 || refs[0].RunID != 42 || refs[0].ScheduleID != 5 || refs[0].Schedule != "warm" {
		t.Fatalf("refs = %+v, want one {warm 5 42}", refs)
	}
}

func TestDeployAppBundle_ReturnsFailureKindFromBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/deploy"):
			w.WriteHeader(500)
			_, _ = w.Write([]byte(`{"error":"deploy failed: ...","failure_kind":"readiness_timeout"}`))
		case r.Method == "GET" && r.URL.Path == "/api/apps/demo":
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"app":{"slug":"demo"}}`))
		default:
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "app.py"), "print(1)\n")
	cfg := &cliConfig{Host: srv.URL, Token: "shk_test"}
	_, _, _, kind, err := deployAppBundle(cfg, "demo", dir, "", "", io.Discard, "r", 5*time.Second)
	if err == nil {
		t.Fatal("expected deploy failure")
	}
	if kind != deployfail.ReadinessTimeout {
		t.Fatalf("kind = %q, want readiness_timeout", kind)
	}
}

func TestDeployAppBundle_FailureKindFallbackForOldServer(t *testing.T) {
	// Old server: no failure_kind field; the CLI classifies the body text. A
	// build error preserves its raw substring, so it is recoverable.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/deploy"):
			w.WriteHeader(500)
			_, _ = w.Write([]byte(`{"error":"deploy failed: uv sync: resolution failed for pandas"}`))
		case r.Method == "GET" && r.URL.Path == "/api/apps/demo":
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"app":{"slug":"demo"}}`))
		default:
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "app.py"), "print(1)\n")
	cfg := &cliConfig{Host: srv.URL, Token: "shk_test"}
	_, _, _, kind, err := deployAppBundle(cfg, "demo", dir, "", "", io.Discard, "r", 5*time.Second)
	if err == nil {
		t.Fatal("expected deploy failure")
	}
	if kind != deployfail.BuildFailed {
		t.Fatalf("kind = %q, want build_failed (fallback classification)", kind)
	}
}

// A 4xx bundle rejection (too large / bad request) with no failure_kind must be
// classified bundle_invalid, NOT server_error: it is a non-retryable client-side
// problem and labeling it a server failure misleads operators and automation.
func TestDeployAppBundle_BundleRejectionClassifiedBundleInvalid(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/deploy"):
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			_, _ = w.Write([]byte(`{"error":"bundle exceeds extracted size limit"}`))
		case r.Method == "GET" && r.URL.Path == "/api/apps/demo":
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"app":{"slug":"demo"}}`))
		default:
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "app.py"), "print(1)\n")
	cfg := &cliConfig{Host: srv.URL, Token: "shk_test"}
	_, committed, _, kind, err := deployAppBundle(cfg, "demo", dir, "", "", io.Discard, "r", 5*time.Second)
	if err == nil {
		t.Fatal("expected deploy failure")
	}
	if committed {
		t.Fatal("a 4xx rejection must report committed=false")
	}
	if kind != deployfail.BundleInvalid {
		t.Fatalf("kind = %q, want bundle_invalid for a 4xx bundle rejection", kind)
	}
}

// A 5xx on the pre-deploy existence check must stop the deploy and be reported
// as a server failure. Creating on an unproven absence turns the server fault
// into a slug conflict, which sends the operator to the manifest for a problem
// that is entirely on the server; labelling it "unknown" hides it just as well.
func TestDeployAppBundle_ExistenceCheckServerErrorStopsDeploy(t *testing.T) {
	var created, deployed bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/apps/demo":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"internal server error"}`))
		case r.Method == "POST" && r.URL.Path == "/api/apps":
			created = true
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":"slug already has on-disk state"}`))
		case r.Method == "POST" && r.URL.Path == "/api/apps/demo/deploy":
			deployed = true
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "app.py"), "print(1)\n")
	cfg := &cliConfig{Host: srv.URL, Token: "shk_test"}
	_, committed, _, kind, err := deployAppBundle(cfg, "demo", dir, "", "", io.Discard, "r", 5*time.Second)
	if err == nil {
		t.Fatal("expected the deploy to fail on the broken existence check")
	}
	if created {
		t.Error("the app was created after a check that never proved it absent")
	}
	if deployed {
		t.Error("a bundle was uploaded despite the failed existence check")
	}
	if committed {
		t.Error("a failed existence check must report committed=false")
	}
	if kind != deployfail.ServerError {
		t.Errorf("kind = %q, want server_error", kind)
	}
	if !strings.Contains(err.Error(), "internal server error") {
		t.Errorf("error = %q, want the server message, not a slug conflict", err)
	}
}

// TestDeployAppBundle_EmitsManifestWarningsBeforeHealthWait covers the
// reported grouped-app scenario end to end on the CLI side: the server accepts
// the deploy, attaches a keep-warm advisory to the manifest block, and then
// reports the app as idle (an elastic pool with no worker booted). The fleet
// output must print the advisory as a note ahead of the health line, and the
// idle status must satisfy the health wait rather than run it to the timeout.
func TestDeployAppBundle_EmitsManifestWarningsBeforeHealthWait(t *testing.T) {
	const advisory = "min_warm_replicas=1 has no effect under worker.isolation=grouped: set worker.warm_spares to keep workers pre-booted"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/api/apps/demo/deploy":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "ok",
				"manifest": map[string]any{
					"app":      map[string]any{"min_warm_replicas": 1},
					"warnings": []string{advisory},
				},
			})
		case r.Method == "GET" && r.URL.Path == "/api/apps/demo":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"app": map[string]any{"status": "idle", "desired_status": "running"},
			})
		case r.Method == "GET" && r.URL.Path == "/api/apps":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"slug": "demo", "content_digest": "sha256:X"},
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

	var buf bytes.Buffer
	if _, _, _, _, err := deployAppBundle(cfg, "demo", dir, "private", "", &buf, "run-1", 5*time.Second); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	out := buf.String()
	note := strings.Index(out, "demo: Note: "+advisory)
	healthy := strings.Index(out, "healthy")
	if note < 0 {
		t.Fatalf("fleet deploy must print the server's manifest warning as a note, got:\n%s", out)
	}
	if healthy < 0 {
		t.Fatalf("an idle elastic app must satisfy the health wait, got:\n%s", out)
	}
	if note > healthy {
		t.Errorf("the advisory must precede the health line so it explains what follows, got:\n%s", out)
	}
}
