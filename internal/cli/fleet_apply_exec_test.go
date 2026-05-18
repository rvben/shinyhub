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
	if !deployed || !stamped {
		t.Fatalf("deployed=%v stamped=%v, want both", deployed, stamped)
	}
	if v, _ := stampBody["managed_by"].(string); v != "fleet:eu" {
		t.Fatalf("marker body = %#v", stampBody["managed_by"])
	}
}
