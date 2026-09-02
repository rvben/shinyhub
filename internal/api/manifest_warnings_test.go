package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

// deployManifestBundle deploys a one-file app with the given shinyhub.toml
// and returns the decoded deploy response.
func deployManifestBundle(t *testing.T, srv *Server, token, slug, manifest string) map[string]any {
	t.Helper()
	body, ctype := buildMultiFileBundleUpload(t, map[string]string{
		"app.py":        "from shiny import App\n",
		"shinyhub.toml": manifest,
	})
	req := httptest.NewRequest("POST", "/api/apps/"+slug+"/deploy", body)
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-ShinyHub-Allow-Downtime", "1")
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("deploy %s: expected 200, got %d: %s", slug, rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse deploy response: %v: %s", err, rec.Body.String())
	}
	return resp
}

// manifestWarnings extracts manifest.warnings from a deploy response, or nil
// when the block or the field is absent.
func manifestWarnings(resp map[string]any) []string {
	m, _ := resp["manifest"].(map[string]any)
	raw, _ := m["warnings"].([]any)
	var out []string
	for _, item := range raw {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// TestDeploy_ManifestMinWarmReplicasUnderElasticWarns pins the deploy-time
// half of the keep-warm advisory: a bundle whose manifest declares a
// min_warm_replicas floor together with elastic isolation deploys
// successfully (the floor is stored, since it applies again under multiplex)
// and the response's manifest block carries a warning naming the inert floor
// and worker.warm_spares. Without it the only symptom is a health wait that
// never sees a running replica.
func TestDeploy_ManifestMinWarmReplicasUnderElasticWarns(t *testing.T) {
	srv, store, token := newManifestE2EServer(t)
	admin, _ := store.GetUserByUsername("admin")
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "warmapp", Name: "Warm App", OwnerID: admin.ID}); err != nil {
		t.Fatal(err)
	}

	resp := deployManifestBundle(t, srv, token, "warmapp", `
[app]
min_warm_replicas = 1

[app.worker]
isolation = "grouped"
grouped_size = 4
max_workers = 3
`)
	warnings := manifestWarnings(resp)
	if len(warnings) != 1 {
		t.Fatalf("manifest.warnings = %v, want exactly one keep-warm advisory", warnings)
	}
	for _, needle := range []string{"min_warm_replicas=1", "worker.isolation=grouped", "worker.warm_spares"} {
		if !strings.Contains(warnings[0], needle) {
			t.Errorf("warning %q should mention %q", warnings[0], needle)
		}
	}
	app, err := store.GetAppBySlug("warmapp")
	if err != nil {
		t.Fatal(err)
	}
	if app.MinWarmReplicas != 1 || app.WorkerIsolation != "grouped" {
		t.Errorf("stored min_warm_replicas=%d isolation=%q; the manifest must still be applied as declared",
			app.MinWarmReplicas, app.WorkerIsolation)
	}
}

// TestDeploy_ManifestWarningScopedToDeclaringManifest pins the two boundaries
// of the advisory. It is silent when the floor is effective (multiplex), and
// once the inert combination is stored it does not repeat on a redeploy whose
// manifest declares neither knob: the advisory belongs to the manifest that
// produced the combination, not to every deploy of the app afterwards.
func TestDeploy_ManifestWarningScopedToDeclaringManifest(t *testing.T) {
	srv, store, token := newManifestE2EServer(t)
	admin, _ := store.GetUserByUsername("admin")
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "scoped", Name: "Scoped App", OwnerID: admin.ID}); err != nil {
		t.Fatal(err)
	}

	// Multiplex: the floor is effective, so no advisory and (with nothing
	// else to report beyond the applied fields) no warnings key at all.
	resp := deployManifestBundle(t, srv, token, "scoped", "[app]\nmin_warm_replicas = 1\n")
	if w := manifestWarnings(resp); len(w) != 0 {
		t.Fatalf("multiplex floor must not warn, got %v", w)
	}

	// Switching the same app to grouped over the stored floor: the manifest
	// declares isolation, so the combination it produces is reported.
	resp = deployManifestBundle(t, srv, token, "scoped", `
[app.worker]
isolation = "grouped"
grouped_size = 4
max_workers = 3
`)
	if w := manifestWarnings(resp); len(w) != 1 || !strings.Contains(w[0], "min_warm_replicas=1") {
		t.Fatalf("declaring grouped over a stored floor must warn once, got %v", w)
	}

	// A redeploy declaring neither knob leaves the stored combination as it
	// was and stays quiet about it.
	resp = deployManifestBundle(t, srv, token, "scoped", "[app]\nmax_sessions_per_replica = 5\n")
	if w := manifestWarnings(resp); len(w) != 0 {
		t.Errorf("a redeploy that declares neither knob must not repeat the advisory, got %v", w)
	}
}
