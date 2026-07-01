package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/deployfail"
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

func TestConvergeApp_UpdateSourceReassertsAutoscaleOnly(t *testing.T) {
	// A source-only deploy can overwrite the autoscale columns from the new
	// bundle's shinyhub.toml. convergeApp reasserts ONLY autoscale (gated on the
	// promoted digest): autoscale does not trigger a redeploy, so fleet
	// precedence is restored in one pass. Crucially, `replicas` (declared here)
	// must NOT be re-PATCHed - that would cycle the pool a second time.
	var patchBody map[string]any
	var patchDigest string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/deploy"):
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case r.Method == "GET" && r.URL.Path == "/api/apps/srconly":
			_ = json.NewEncoder(w).Encode(map[string]any{"app": map[string]any{"status": "running"}})
		case r.Method == "GET" && r.URL.Path == "/api/apps":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"slug": "srconly", "content_digest": "sha256:PROMOTED"}})
		case r.Method == "PATCH" && r.URL.Path == "/api/apps/srconly":
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &patchBody)
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

	en := true
	entry := fleet.AppEntry{Slug: "srconly", Source: "./x", Visibility: "private",
		Config: fleet.Config{Replicas: stateInt(2), Autoscale: &fleet.AutoscaleConfig{Enabled: &en, MinReplicas: 1, MaxReplicas: 8, Target: 0.8}}}
	d := fleet.AppDiff{Slug: "srconly", Action: fleet.ActionUpdateSource, Owned: true, ServerDigest: "sha256:OLD"}

	r := convergeApp(cfg, d, entry, fleet.ObservedApp{Slug: "srconly"}, dir,
		convergeOpts{preconditions: true, fleetID: "eu", runID: "r"}, "fleet:eu", io.Discard)
	if r.status != statusUpdated {
		t.Fatalf("status = %s (%v), want updated", r.status, r.err)
	}
	if _, ok := patchBody["autoscale"]; !ok {
		t.Errorf("expected an autoscale reassert PATCH, body = %#v", patchBody)
	}
	if _, ok := patchBody["replicas"]; ok {
		t.Error("replicas must NOT be re-PATCHed on a source-only deploy (would cause a second pool cycle)")
	}
	if patchDigest != "sha256:PROMOTED" {
		t.Errorf("reassert precondition = %q, want the promoted digest", patchDigest)
	}
}

func TestConvergeApp_UpdateSourceConfigReassertsAutoscale(t *testing.T) {
	// Source+config change where autoscale matched at plan time (so it is NOT in
	// d.ConfigDrift) but another key (replicas) drifted. The redeployed bundle can
	// still overwrite autoscale, so it must be reasserted after the deploy even
	// though it was absent from the pre-deploy drift list.
	var sawAutoscalePatch bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/deploy"):
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case r.Method == "GET" && r.URL.Path == "/api/apps/srccfg":
			_ = json.NewEncoder(w).Encode(map[string]any{"app": map[string]any{"status": "running"}})
		case r.Method == "GET" && r.URL.Path == "/api/apps":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"slug": "srccfg", "content_digest": "sha256:PROMOTED"}})
		case r.Method == "PATCH" && r.URL.Path == "/api/apps/srccfg":
			b, _ := io.ReadAll(r.Body)
			var body map[string]any
			_ = json.Unmarshal(b, &body)
			if _, ok := body["autoscale"]; ok {
				sawAutoscalePatch = true
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

	en := true
	entry := fleet.AppEntry{Slug: "srccfg", Source: "./x", Visibility: "private",
		Config: fleet.Config{Replicas: stateInt(2), Autoscale: &fleet.AutoscaleConfig{Enabled: &en, MinReplicas: 1, MaxReplicas: 8, Target: 0.8}}}
	// Pre-deploy drift is replicas only; autoscale matched at plan time.
	d := fleet.AppDiff{Slug: "srccfg", Action: fleet.ActionUpdateSourceConfig, Owned: true,
		ServerDigest: "sha256:OLD", ConfigDrift: []fleet.ConfigDriftItem{{Key: "replicas", Server: "1", Desired: "2"}}}

	r := convergeApp(cfg, d, entry, fleet.ObservedApp{Slug: "srccfg"}, dir,
		convergeOpts{preconditions: true, fleetID: "eu", runID: "r"}, "fleet:eu", io.Discard)
	if r.status != statusUpdated {
		t.Fatalf("status = %s (%v), want updated", r.status, r.err)
	}
	if !sawAutoscalePatch {
		t.Error("autoscale must be reasserted after a source+config deploy even when absent from the pre-deploy drift")
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

// A deploy that fails its first attempt with a readiness timeout, then succeeds
// on retry, must still record WHY attempt 1 failed - that is the motivating case
// (an app that eventually came up but flaked once).
func TestConvergeApp_RetriedSuccessRecordsFailedAttemptKind(t *testing.T) {
	var deployHits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/deploy"):
			if atomic.AddInt32(&deployHits, 1) == 1 {
				w.WriteHeader(500)
				_, _ = w.Write([]byte(`{"error":"deploy failed: ...","failure_kind":"readiness_timeout"}`))
				return
			}
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case r.Method == "GET" && r.URL.Path == "/api/apps/flaky":
			_ = json.NewEncoder(w).Encode(map[string]any{"app": map[string]any{"status": "running"}})
		case r.Method == "GET" && r.URL.Path == "/api/apps":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"slug": "flaky", "content_digest": "sha256:NEW"}})
		default:
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	cfg := &cliConfig{Host: srv.URL, Token: "shk_test"}
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "app.py"), "print(1)\n")
	d := fleet.AppDiff{Slug: "flaky", Action: fleet.ActionCreate}
	entry := fleet.AppEntry{Slug: "flaky", Source: "./x", Visibility: "private"}
	r := convergeApp(cfg, d, entry, fleet.ObservedApp{}, dir,
		convergeOpts{preconditions: true, retries: 1, fleetID: "eu", runID: "r"}, "fleet:eu", io.Discard)

	if r.status != statusCreated {
		t.Fatalf("status = %s (%v), want created (second attempt succeeds)", r.status, r.err)
	}
	if len(r.attemptsDetail) != 1 {
		t.Fatalf("want exactly one failed-attempt record, got %d: %+v", len(r.attemptsDetail), r.attemptsDetail)
	}
	if r.attemptsDetail[0].Kind != deployfail.ReadinessTimeout || r.attemptsDetail[0].Attempt != 1 {
		t.Fatalf("attempt 1 record = %+v, want {Attempt:1 Kind:readiness_timeout}", r.attemptsDetail[0])
	}
}

// buildConcurrencyPreflight makes a preflightResult of n ActionUpdateSource apps
// (app0..app(n-1)), all sourced from dir, for exercising convergeFleet.
func buildConcurrencyPreflight(n int, dir string) *preflightResult {
	apps := make([]fleet.AppEntry, n)
	diff := make([]fleet.AppDiff, n)
	observed := make(map[string]fleet.ObservedApp, n)
	sources := make(map[string]string, n)
	for i := 0; i < n; i++ {
		s := fmt.Sprintf("app%d", i)
		apps[i] = fleet.AppEntry{Slug: s, Source: "./x", Visibility: "private"}
		diff[i] = fleet.AppDiff{Slug: s, Action: fleet.ActionUpdateSource, Owned: true, ServerDigest: "sha256:OLD"}
		observed[s] = fleet.ObservedApp{Slug: s}
		sources[s] = dir
	}
	return &preflightResult{
		manifest: &fleet.Manifest{FleetID: "eu", Apps: apps},
		diff:     diff, observed: observed, sources: sources,
	}
}

// concurrencyTestServer answers the deploy/health/list calls convergeApp makes
// for an ActionUpdateSource app, tracking the max number of concurrent deploys.
func concurrencyTestServer(t *testing.T, n int, maxInflight *atomic.Int32, sleep time.Duration) *httptest.Server {
	t.Helper()
	var inflight atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/deploy"):
			cur := inflight.Add(1)
			for {
				m := maxInflight.Load()
				if cur <= m || maxInflight.CompareAndSwap(m, cur) {
					break
				}
			}
			time.Sleep(sleep)
			inflight.Add(-1)
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case r.Method == "GET" && r.URL.Path == "/api/apps":
			apps := make([]map[string]any, n)
			for i := 0; i < n; i++ {
				apps[i] = map[string]any{"slug": fmt.Sprintf("app%d", i), "content_digest": "sha256:NEW"}
			}
			_ = json.NewEncoder(w).Encode(apps)
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/api/apps/"):
			_ = json.NewEncoder(w).Encode(map[string]any{"app": map[string]any{"status": "running"}})
		default:
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestConvergeFleet_BoundedConcurrency(t *testing.T) {
	const n, limit = 6, 4
	var maxInflight atomic.Int32
	srv := concurrencyTestServer(t, n, &maxInflight, 40*time.Millisecond)
	cfg := &cliConfig{Host: srv.URL, Token: "shk_test"}
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "app.py"), "print(1)\n")

	pf := buildConcurrencyPreflight(n, dir)
	opt := convergeOpts{preconditions: true, concurrency: limit, fleetID: "eu", runID: "r"}
	results := convergeFleet(cfg, pf, opt, io.Discard)

	if got := maxInflight.Load(); got <= 1 || int(got) > limit {
		t.Fatalf("max concurrent deploys = %d, want >1 and <=%d", got, limit)
	}
	if len(results) != n {
		t.Fatalf("got %d results, want %d", len(results), n)
	}
	for i, r := range results {
		if want := fmt.Sprintf("app%d", i); r.slug != want {
			t.Fatalf("results[%d].slug = %q, want %q (manifest order preserved)", i, r.slug, want)
		}
		if r.status != statusUpdated {
			t.Fatalf("app%d status = %s (%v), want updated", i, r.status, r.err)
		}
	}
}

func TestConvergeFleet_SerialWhenConcurrencyOne(t *testing.T) {
	const n = 4
	var maxInflight atomic.Int32
	srv := concurrencyTestServer(t, n, &maxInflight, 10*time.Millisecond)
	cfg := &cliConfig{Host: srv.URL, Token: "shk_test"}
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "app.py"), "print(1)\n")

	pf := buildConcurrencyPreflight(n, dir)
	opt := convergeOpts{preconditions: true, concurrency: 1, fleetID: "eu", runID: "r"}
	results := convergeFleet(cfg, pf, opt, io.Discard)

	if got := maxInflight.Load(); got != 1 {
		t.Fatalf("concurrency 1 must be serial; max concurrent deploys = %d, want 1", got)
	}
	if len(results) != n {
		t.Fatalf("got %d results, want %d", len(results), n)
	}
}

// Parallelism must not change the exit code: a mixed diff (one app fails its
// deploy, the rest succeed) must tally to the same applyExitCode under serial
// and parallel convergence (spec section 8).
func TestConvergeFleet_ExitCodeParityParallelSerial(t *testing.T) {
	const n = 4
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "app.py"), "print(1)\n")
	// app0 always fails its deploy (500); app1..app3 succeed.
	mkServer := func() *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/api/apps/app0/"):
				w.WriteHeader(500)
				_, _ = w.Write([]byte(`{"error":"deploy app0 failed"}`))
			case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/deploy"):
				w.WriteHeader(200)
				_, _ = w.Write([]byte(`{"status":"ok"}`))
			case r.URL.Path == "/api/apps":
				apps := make([]map[string]any, n)
				for i := 0; i < n; i++ {
					apps[i] = map[string]any{"slug": fmt.Sprintf("app%d", i), "content_digest": "sha256:NEW"}
				}
				_ = json.NewEncoder(w).Encode(apps)
			case strings.HasSuffix(r.URL.Path, "/logs"):
				_, _ = io.WriteString(w, "boom\n")
			case strings.HasPrefix(r.URL.Path, "/api/apps/"):
				_ = json.NewEncoder(w).Encode(map[string]any{"app": map[string]any{"status": "running"}})
			default:
				w.WriteHeader(200)
				_, _ = w.Write([]byte(`{}`))
			}
		}))
	}
	run := func(concurrency int) (int, string) {
		srv := mkServer()
		defer srv.Close()
		cfg := &cliConfig{Host: srv.URL, Token: "shk_test"}
		opt := convergeOpts{preconditions: true, concurrency: concurrency, fleetID: "eu", runID: "r"}
		return applyExitCode(convergeFleet(cfg, buildConcurrencyPreflight(n, dir), opt, io.Discard))
	}
	sCode, sReason := run(1)
	pCode, pReason := run(4)
	if sCode != pCode || sReason != pReason {
		t.Fatalf("exit differs: serial=(%d,%q) parallel=(%d,%q)", sCode, sReason, pCode, pReason)
	}
	if sCode != 4 {
		t.Fatalf("one failing app must yield exit 4, got %d (%q)", sCode, sReason)
	}
}
