package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/rvben/shinyhub/internal/fleet"
)

// stateInt makes a *int literal for fleet.Config in tests.
func stateInt(v int) *int { return &v }

func TestConvergeApp_UnchangedIsNoNetwork(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)
	cfg := &cliConfig{Host: srv.URL, Token: "shk_test"}
	d := fleet.AppDiff{Slug: "a", Action: fleet.ActionUnchanged}
	r := convergeApp(cfg, d, fleet.AppEntry{Slug: "a"}, fleet.ObservedApp{}, "",
		convergeOpts{preconditions: true, fleetID: "eu", runID: "r"}, "fleet:eu", io.Discard)
	if r.status != statusUnchanged {
		t.Fatalf("status = %s, want unchanged", r.status)
	}
	if hits != 0 {
		t.Fatalf("unchanged must do zero requests, got %d", hits)
	}
}

func TestConvergeApp_AdoptSkippedWithoutFlag(t *testing.T) {
	cfg := &cliConfig{Host: "http://unused", Token: "x"}
	d := fleet.AppDiff{Slug: "legacy", Action: fleet.ActionAdopt}
	r := convergeApp(cfg, d, fleet.AppEntry{Slug: "legacy"}, fleet.ObservedApp{}, "",
		convergeOpts{adopt: false, preconditions: true, fleetID: "eu", runID: "r"}, "fleet:eu", io.Discard)
	if r.status != statusSkipped || !strings.Contains(r.note, "--adopt") {
		t.Fatalf("want skipped w/ --adopt hint, got %s %q", r.status, r.note)
	}
}

func TestConvergeApp_DeleteSkippedWithoutPrune(t *testing.T) {
	cfg := &cliConfig{Host: "http://unused", Token: "x"}
	d := fleet.AppDiff{Slug: "retired", Action: fleet.ActionDelete}
	r := convergeApp(cfg, d, fleet.AppEntry{}, fleet.ObservedApp{}, "",
		convergeOpts{prune: false, preconditions: true, fleetID: "eu", runID: "r"}, "fleet:eu", io.Discard)
	if r.status != statusSkipped || !strings.Contains(r.note, "--prune") {
		t.Fatalf("want skipped w/ --prune hint, got %s %q", r.status, r.note)
	}
}

func TestConvergeApp_DeleteDegradedRefused(t *testing.T) {
	cfg := &cliConfig{Host: "http://unused", Token: "x"}
	d := fleet.AppDiff{Slug: "retired", Action: fleet.ActionDelete}
	r := convergeApp(cfg, d, fleet.AppEntry{}, fleet.ObservedApp{}, "",
		convergeOpts{prune: true, preconditions: false, allowDegradedPrune: false, fleetID: "eu", runID: "r"},
		"fleet:eu", io.Discard)
	if r.status != statusSkipped || !strings.Contains(r.note, "degraded") {
		t.Fatalf("want skipped (degraded), got %s %q", r.status, r.note)
	}
}

func TestConvergeApp_UpdateConfigPatchesWithServerDigestPrecondition(t *testing.T) {
	var gotDigest, gotMB string
	var patched bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PATCH" && r.URL.Path == "/api/apps/cfg" {
			patched = true
			gotDigest = r.Header.Get("X-Shinyhub-If-Content-Digest")
			gotMB = r.Header.Get("X-Shinyhub-If-Managed-By")
		}
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)
	cfg := &cliConfig{Host: srv.URL, Token: "shk_test"}
	d := fleet.AppDiff{
		Slug: "cfg", Action: fleet.ActionUpdateConfig, Owned: true,
		ServerDigest: "sha256:SERVER",
		ConfigDrift:  []fleet.ConfigDriftItem{{Key: "replicas", Server: "1", Desired: "2"}},
	}
	entry := fleet.AppEntry{Slug: "cfg", Config: fleet.Config{Replicas: stateInt(2)}}
	r := convergeApp(cfg, d, entry, fleet.ObservedApp{Slug: "cfg"}, "",
		convergeOpts{preconditions: true, fleetID: "eu", runID: "r"}, "fleet:eu", io.Discard)
	if r.status != statusUpdated {
		t.Fatalf("status = %s (%v), want updated", r.status, r.err)
	}
	if !patched || gotDigest != "sha256:SERVER" || gotMB != "fleet:eu" {
		t.Fatalf("precondition wrong: patched=%v digest=%q mb=%q", patched, gotDigest, gotMB)
	}
}

func TestConvergeApp_UpdateSourceConfigGatesOnPostDeployDigest(t *testing.T) {
	// The single most important correctness property: update(source+config)
	// must deploy first, then patch fleet config with a precondition built
	// from the FRESHLY promoted digest - never the stale pre-deploy one. If
	// the ordering were swapped, the patch would carry the stale digest and
	// 409 against the deployment this very run just performed.
	const staleDigest = "sha256:STALE"
	const promotedDigest = "sha256:PROMOTED"

	var deployedAt, patchedAt int
	var seq int
	var patchDigest string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/deploy"):
			seq++
			deployedAt = seq
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case r.Method == "GET" && r.URL.Path == "/api/apps/srccfg":
			_ = json.NewEncoder(w).Encode(map[string]any{"app": map[string]any{"status": "running"}})
		case r.Method == "GET" && r.URL.Path == "/api/apps":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"slug": "srccfg", "content_digest": promotedDigest}})
		case r.Method == "PATCH" && r.URL.Path == "/api/apps/srccfg":
			seq++
			patchedAt = seq
			patchDigest = r.Header.Get("X-Shinyhub-If-Content-Digest")
			w.WriteHeader(200)
		default:
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	cfg := &cliConfig{Host: srv.URL, Token: "shk_test"}
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "app.py"), "print(1)\n")
	d := fleet.AppDiff{
		Slug: "srccfg", Action: fleet.ActionUpdateSourceConfig, Owned: true,
		ServerDigest: staleDigest,
		ConfigDrift:  []fleet.ConfigDriftItem{{Key: "replicas", Server: "1", Desired: "2"}},
	}
	entry := fleet.AppEntry{Slug: "srccfg", Source: "./x", Visibility: "private",
		Config: fleet.Config{Replicas: stateInt(2)}}
	r := convergeApp(cfg, d, entry, fleet.ObservedApp{Slug: "srccfg"}, dir,
		convergeOpts{preconditions: true, fleetID: "eu", runID: "r"}, "fleet:eu", io.Discard)
	if r.status != statusUpdated {
		t.Fatalf("status = %s (%v), want updated", r.status, r.err)
	}
	if deployedAt == 0 || patchedAt == 0 || deployedAt > patchedAt {
		t.Fatalf("ordering wrong: deployedAt=%d patchedAt=%d (deploy must precede patch)", deployedAt, patchedAt)
	}
	if patchDigest != promotedDigest {
		t.Fatalf("config patch precondition = %q, want post-deploy promoted digest %q (not stale %q)",
			patchDigest, promotedDigest, staleDigest)
	}
}

func TestConvergeApp_AdoptReservesOwnershipBeforeDeployAndReleasesOnFailure(t *testing.T) {
	// Two properties at once:
	//  1. Ownership is RESERVED (preconditioned stamp) BEFORE the bundle is
	//     uploaded, so a concurrent ownership change is rejected before any
	//     deploy can overwrite an app we no longer own.
	//  2. If the redeploy then fails, the reservation is RELEASED (managed_by
	//     restored to its observed prior value) so the app is never left
	//     "owned but undeployed" and the next plan proposes a clean adopt.
	var mbValues []any // ordered managed_by values seen on PATCH /api/apps/legacy
	var deployedBeforeReserve bool
	var reserved bool
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/deploy"):
			mu.Lock()
			if !reserved {
				deployedBeforeReserve = true
			}
			mu.Unlock()
			// 400 = clean client-side rejection: nothing committed, so the
			// reservation is safe to release.
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":"bundle rejected"}`))
		case r.Method == "PATCH" && r.URL.Path == "/api/apps/legacy":
			b, _ := io.ReadAll(r.Body)
			var body map[string]any
			_ = json.Unmarshal(b, &body)
			if v, ok := body["managed_by"]; ok {
				mu.Lock()
				mbValues = append(mbValues, v)
				if v == "fleet:eu" {
					reserved = true
				}
				mu.Unlock()
			}
			w.WriteHeader(200)
		default:
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	cfg := &cliConfig{Host: srv.URL, Token: "shk_test"}
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "app.py"), "print(1)\n")
	d := fleet.AppDiff{Slug: "legacy", Action: fleet.ActionAdopt}
	entry := fleet.AppEntry{Slug: "legacy", Source: "./x", Visibility: "private"}
	r := convergeApp(cfg, d, entry, fleet.ObservedApp{Slug: "legacy"}, dir,
		convergeOpts{adopt: true, preconditions: true, retries: 0, fleetID: "eu", runID: "r"},
		"fleet:eu", io.Discard)
	if r.status != statusFailed {
		t.Fatalf("status = %s (%v), want failed", r.status, r.err)
	}
	mu.Lock()
	defer mu.Unlock()
	if deployedBeforeReserve {
		t.Fatalf("bundle was deployed before ownership was reserved (TOCTOU overwrite risk)")
	}
	// Net effect: reserve then release. First managed_by stamp is the marker,
	// last is the cleared (nil) value - so ownership is not left stamped.
	if len(mbValues) < 2 {
		t.Fatalf("want a reserve + release pair, got managed_by sequence %v", mbValues)
	}
	if mbValues[0] != "fleet:eu" {
		t.Fatalf("first managed_by must reserve the marker, got %v", mbValues[0])
	}
	if last := mbValues[len(mbValues)-1]; last != nil {
		t.Fatalf("ownership must be released (managed_by=null) after a failed adopt deploy, got %v", last)
	}
}

func TestConvergeApp_AdoptDegradedDoesNotReleaseUnguarded(t *testing.T) {
	// In degraded mode (no precondition support) the release PATCH would carry
	// no If-Managed-By guard, so it could clear or overwrite a new owner that
	// took the app between our reservation and the deploy failure. We therefore
	// must NOT issue an unguarded release in degraded mode - the documented
	// degraded race is accepted rather than risking a clobber.
	var mbValues []any
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/deploy"):
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":"bundle rejected"}`))
		case r.Method == "PATCH" && r.URL.Path == "/api/apps/legacy":
			b, _ := io.ReadAll(r.Body)
			var body map[string]any
			_ = json.Unmarshal(b, &body)
			if v, ok := body["managed_by"]; ok {
				mu.Lock()
				mbValues = append(mbValues, v)
				mu.Unlock()
			}
			w.WriteHeader(200)
		default:
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	cfg := &cliConfig{Host: srv.URL, Token: "shk_test"}
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "app.py"), "print(1)\n")
	d := fleet.AppDiff{Slug: "legacy", Action: fleet.ActionAdopt}
	entry := fleet.AppEntry{Slug: "legacy", Source: "./x", Visibility: "private"}
	r := convergeApp(cfg, d, entry, fleet.ObservedApp{Slug: "legacy"}, dir,
		convergeOpts{adopt: true, preconditions: false, retries: 0, fleetID: "eu", runID: "r"},
		"fleet:eu", io.Discard)
	if r.status != statusFailed {
		t.Fatalf("status = %s (%v), want failed", r.status, r.err)
	}
	mu.Lock()
	defer mu.Unlock()
	for i := 1; i < len(mbValues); i++ {
		t.Fatalf("degraded mode must not issue a release PATCH; managed_by sequence = %v", mbValues)
	}
}

func TestConvergeApp_AdoptDoesNotReleaseAfterCommittedDeploy(t *testing.T) {
	// If the bundle POST is accepted (deploy committed) but the post-deploy
	// health wait then fails, this fleet's source is now running on the app.
	// Releasing ownership back to the prior owner would leave the app marked
	// as theirs while running OUR bundle - an inconsistent state worse than
	// keeping the marker. The reservation must be kept once the deploy commits.
	var mbValues []any
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/deploy"):
			w.WriteHeader(200) // deploy COMMITS
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case r.Method == "GET" && r.URL.Path == "/api/apps/legacy":
			// Health wait sees a terminal crash -> deploy fails post-commit.
			_ = json.NewEncoder(w).Encode(map[string]any{"app": map[string]any{"status": "crashed"}})
		case r.Method == "PATCH" && r.URL.Path == "/api/apps/legacy":
			b, _ := io.ReadAll(r.Body)
			var body map[string]any
			_ = json.Unmarshal(b, &body)
			if v, ok := body["managed_by"]; ok {
				mu.Lock()
				mbValues = append(mbValues, v)
				mu.Unlock()
			}
			w.WriteHeader(200)
		default:
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	cfg := &cliConfig{Host: srv.URL, Token: "shk_test"}
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "app.py"), "print(1)\n")
	d := fleet.AppDiff{Slug: "legacy", Action: fleet.ActionAdopt}
	entry := fleet.AppEntry{Slug: "legacy", Source: "./x", Visibility: "private"}
	r := convergeApp(cfg, d, entry, fleet.ObservedApp{Slug: "legacy"}, dir,
		convergeOpts{adopt: true, preconditions: true, retries: 0, fleetID: "eu", runID: "r"},
		"fleet:eu", io.Discard)
	if r.status != statusFailed {
		t.Fatalf("status = %s (%v), want failed", r.status, r.err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(mbValues) != 1 || mbValues[0] != "fleet:eu" {
		t.Fatalf("after a committed deploy, ownership must be reserved and NOT released; managed_by sequence = %v", mbValues)
	}
}

func TestConvergeApp_AdoptKeepsOwnershipWhenBundleWentLive(t *testing.T) {
	// The deploy endpoint returns 500 on post-promotion paths (e.g. manifest
	// schedule apply) with the new bundle already live. The HTTP status cannot
	// distinguish that from a pre-promotion 500, so the adopt path reads back
	// the live digest: here it advanced past the pre-deploy digest, proving the
	// bundle went live, so the ownership reservation must be KEPT (not released).
	var mbValues []any
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/deploy"):
			w.WriteHeader(500) // ambiguous: post-promotion failure
			_, _ = w.Write([]byte(`{"error":"schedule apply failed: boom"}`))
		case r.Method == "GET" && r.URL.Path == "/api/apps":
			// Live digest advanced past the pre-deploy one -> bundle went live.
			_ = json.NewEncoder(w).Encode([]map[string]any{{"slug": "legacy", "content_digest": "sha256:NEW"}})
		case r.Method == "PATCH" && r.URL.Path == "/api/apps/legacy":
			b, _ := io.ReadAll(r.Body)
			var body map[string]any
			_ = json.Unmarshal(b, &body)
			if v, ok := body["managed_by"]; ok {
				mu.Lock()
				mbValues = append(mbValues, v)
				mu.Unlock()
			}
			w.WriteHeader(200)
		default:
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	cfg := &cliConfig{Host: srv.URL, Token: "shk_test"}
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "app.py"), "print(1)\n")
	d := fleet.AppDiff{Slug: "legacy", Action: fleet.ActionAdopt, ServerDigest: "sha256:OLD"}
	entry := fleet.AppEntry{Slug: "legacy", Source: "./x", Visibility: "private"}
	r := convergeApp(cfg, d, entry, fleet.ObservedApp{Slug: "legacy"}, dir,
		convergeOpts{adopt: true, preconditions: true, retries: 0, fleetID: "eu", runID: "r"},
		"fleet:eu", io.Discard)
	if r.status != statusFailed {
		t.Fatalf("status = %s (%v), want failed", r.status, r.err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(mbValues) != 1 || mbValues[0] != "fleet:eu" {
		t.Fatalf("a bundle that went live must keep its ownership reservation; managed_by sequence = %v", mbValues)
	}
}

func TestConvergeApp_ConflictRecordedNotRetried(t *testing.T) {
	var patchCalls int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PATCH" {
			mu.Lock()
			patchCalls++
			mu.Unlock()
			w.WriteHeader(409)
			_, _ = w.Write([]byte(`{"error":"precondition failed (re-run plan)"}`))
			return
		}
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)
	cfg := &cliConfig{Host: srv.URL, Token: "shk_test"}
	d := fleet.AppDiff{Slug: "cfg", Action: fleet.ActionUpdateConfig, Owned: true,
		ServerDigest: "sha256:S", ConfigDrift: []fleet.ConfigDriftItem{{Key: "replicas", Desired: "2"}}}
	entry := fleet.AppEntry{Slug: "cfg", Config: fleet.Config{Replicas: stateInt(2)}}
	r := convergeApp(cfg, d, entry, fleet.ObservedApp{}, "",
		convergeOpts{preconditions: true, retries: 3, fleetID: "eu", runID: "r"}, "fleet:eu", io.Discard)
	if r.status != statusConflict {
		t.Fatalf("status = %s, want conflict", r.status)
	}
	if patchCalls != 1 {
		t.Fatalf("conflict must NOT be retried, patch called %d times", patchCalls)
	}
}

// TestConvergeApp_FailedDeployAttachesLogTail verifies that when a deploy fails
// its health check (the app crashed on startup), the per-app result and the
// JSON envelope carry the app's log tail - including the exception line - so
// the operator does not have to SSH to the host to read app-0.log.
func TestConvergeApp_FailedDeployAttachesLogTail(t *testing.T) {
	const crashLine = "ModuleNotFoundError: No module named 'pandas'"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/deploy"):
			w.WriteHeader(500)
			_, _ = w.Write([]byte(`{"error":"deploy failed: the app did not pass its health check - it likely crashed on startup. Check the app logs for the cause."}`))
		case r.Method == "GET" && r.URL.Path == "/api/apps/demo/logs":
			_, _ = io.WriteString(w, "Traceback (most recent call last):\n  File \"app.py\", line 12, in <module>\n"+crashLine+"\n")
		case r.Method == "GET" && r.URL.Path == "/api/apps/demo":
			_ = json.NewEncoder(w).Encode(map[string]any{"app": map[string]any{"status": "crashed"}})
		default:
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	cfg := &cliConfig{Host: srv.URL, Token: "shk_test"}
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "app.py"), "print(1)\n")
	d := fleet.AppDiff{Slug: "demo", Action: fleet.ActionCreate}
	entry := fleet.AppEntry{Slug: "demo", Source: "./x", Visibility: "private"}
	r := convergeApp(cfg, d, entry, fleet.ObservedApp{}, dir,
		convergeOpts{preconditions: true, fleetID: "eu", runID: "r"}, "fleet:eu", io.Discard)

	if r.status != statusFailed {
		t.Fatalf("status = %s (%v), want failed", r.status, r.err)
	}
	if !strings.Contains(strings.Join(r.logTail, "\n"), crashLine) {
		t.Fatalf("result log tail must contain the crash exception; got %q", r.logTail)
	}

	// The JSON envelope must carry log_tail too, so non-TTY/CI callers get the
	// cause without a second call.
	var buf strings.Builder
	m := &fleet.Manifest{FleetID: "eu"}
	if err := writeFleetApplyJSON(&buf, m, cfg.Host, []fleet.AppDiff{d}, []applyResult{r}, 4, "PARTIAL"); err != nil {
		t.Fatalf("writeFleetApplyJSON: %v", err)
	}
	if !strings.Contains(buf.String(), crashLine) {
		t.Fatalf("JSON envelope must include log_tail with the exception; got %s", buf.String())
	}
	if !strings.Contains(buf.String(), `"log_tail"`) {
		t.Fatalf("JSON envelope must use the log_tail key; got %s", buf.String())
	}
}

// TestConvergeApp_PostDeployPatchFailureHasNoLogTail verifies the log tail is
// attached only when the deploy itself failed. When the bundle deploys fine but
// a follow-up config PATCH fails, the app is running, so its log tail would be
// misleading and must NOT be fetched or attached.
func TestConvergeApp_PostDeployPatchFailureHasNoLogTail(t *testing.T) {
	var logsHit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/deploy"):
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case r.Method == "GET" && r.URL.Path == "/api/apps":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"slug": "sc", "content_digest": "sha256:NEW"}})
		case r.Method == "PATCH" && r.URL.Path == "/api/apps/sc":
			w.WriteHeader(500)
			_, _ = w.Write([]byte(`{"error":"patch boom"}`))
		case r.URL.Path == "/api/apps/sc/logs":
			logsHit = true
			_, _ = io.WriteString(w, "should not be fetched\n")
		case r.Method == "GET" && r.URL.Path == "/api/apps/sc":
			_ = json.NewEncoder(w).Encode(map[string]any{"app": map[string]any{"status": "running"}})
		default:
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	cfg := &cliConfig{Host: srv.URL, Token: "shk_test"}
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "app.py"), "print(1)\n")
	d := fleet.AppDiff{
		Slug: "sc", Action: fleet.ActionUpdateSourceConfig, Owned: true,
		ServerDigest: "sha256:OLD",
		ConfigDrift:  []fleet.ConfigDriftItem{{Key: "replicas", Server: "1", Desired: "2"}},
	}
	entry := fleet.AppEntry{Slug: "sc", Config: fleet.Config{Replicas: stateInt(2)}}
	r := convergeApp(cfg, d, entry, fleet.ObservedApp{Slug: "sc"}, dir,
		convergeOpts{preconditions: true, fleetID: "eu", runID: "r"}, "fleet:eu", io.Discard)

	if r.status != statusFailed {
		t.Fatalf("status = %s (%v), want failed", r.status, r.err)
	}
	if len(r.logTail) != 0 {
		t.Fatalf("post-deploy patch failure must not attach a log tail, got %v", r.logTail)
	}
	if logsHit {
		t.Errorf("the logs endpoint must not be queried for a post-deploy patch failure")
	}
}

func TestConvergeApp_CreateDeploysThenStampsMarker(t *testing.T) {
	var deployed, stamped bool
	var stampBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/deploy"):
			deployed = true
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case r.Method == "GET" && r.URL.Path == "/api/apps/new":
			_ = json.NewEncoder(w).Encode(map[string]any{"app": map[string]any{"status": "running"}})
		case r.Method == "GET" && r.URL.Path == "/api/apps":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"slug": "new", "content_digest": "sha256:NEW"}})
		case r.Method == "PATCH" && r.URL.Path == "/api/apps/new":
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &stampBody)
			stamped = true
			w.WriteHeader(200)
		default:
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	cfg := &cliConfig{Host: srv.URL, Token: "shk_test"}
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "app.py"), "print(1)\n")
	d := fleet.AppDiff{Slug: "new", Action: fleet.ActionCreate}
	entry := fleet.AppEntry{Slug: "new", Source: "./x", Visibility: "private"}
	r := convergeApp(cfg, d, entry, fleet.ObservedApp{}, dir,
		convergeOpts{preconditions: true, fleetID: "eu", runID: "r"}, "fleet:eu", io.Discard)
	if r.status != statusCreated {
		t.Fatalf("status = %s (%v), want created", r.status, r.err)
	}
	if len(r.logTail) != 0 {
		t.Fatalf("a successful create must not attach a log tail, got %v", r.logTail)
	}
	if !deployed || !stamped {
		t.Fatalf("deployed=%v stamped=%v, want both", deployed, stamped)
	}
	if v, _ := stampBody["managed_by"].(string); v != "fleet:eu" {
		t.Fatalf("marker body = %#v", stampBody["managed_by"])
	}
}
