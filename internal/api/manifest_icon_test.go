package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
)

// A declared icon reconciles; an absent key leaves the stored value alone; ""
// clears the emoji and lets a retained image resurface.
func TestManifestIconDeclaredOnly(t *testing.T) {
	srv, store, token := newManifestE2EServer(t)
	admin, _ := store.GetUserByUsername("admin")

	if err := store.CreateApp(db.CreateAppParams{
		Slug: "iconapp", Name: "Icon App", OwnerID: admin.ID,
	}); err != nil {
		t.Fatal(err)
	}

	emoji := "\U0001F4CA"
	manifest1 := "[app]\nicon = \"" + emoji + "\"\n"
	body, ctype := buildMultiFileBundleUpload(t, map[string]string{
		"app.py":        "from shiny import App\n",
		"shinyhub.toml": manifest1,
	})
	req := httptest.NewRequest("POST", "/api/apps/iconapp/deploy", body)
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("first deploy: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	app, err := store.GetAppBySlug("iconapp")
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	if app.IconEmoji != emoji {
		t.Fatalf("icon_emoji = %q, want %q", app.IconEmoji, emoji)
	}

	// Redeploy with no `icon` key declared: the stored emoji must survive
	// untouched. A harmless sibling field keeps the manifest non-empty so
	// applyManifestAppSettings actually runs.
	manifest2 := "[app]\nmax_sessions_per_replica = 5\n"
	body2, ctype2 := buildMultiFileBundleUpload(t, map[string]string{
		"app.py":        "from shiny import App\n",
		"shinyhub.toml": manifest2,
	})
	req2 := httptest.NewRequest("POST", "/api/apps/iconapp/deploy", body2)
	req2.Header.Set("Content-Type", ctype2)
	req2.Header.Set("Authorization", "Bearer "+token)
	rec2 := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("second deploy: expected 200, got %d: %s", rec2.Code, rec2.Body.String())
	}
	app2, err := store.GetAppBySlug("iconapp")
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	if app2.IconEmoji != emoji {
		t.Errorf("icon_emoji after undeclared deploy = %q, want unchanged %q", app2.IconEmoji, emoji)
	}

	// Seed an uploaded image directly (bypassing the emoji this sets to ""),
	// then deploy icon = "" and confirm the image survives while the emoji
	// clears.
	if err := store.SetAppIcon("iconapp", "image/png", []byte("original-bytes")); err != nil {
		t.Fatalf("seed image: %v", err)
	}
	manifest3 := "[app]\nicon = \"\"\n"
	body3, ctype3 := buildMultiFileBundleUpload(t, map[string]string{
		"app.py":        "from shiny import App\n",
		"shinyhub.toml": manifest3,
	})
	req3 := httptest.NewRequest("POST", "/api/apps/iconapp/deploy", body3)
	req3.Header.Set("Content-Type", ctype3)
	req3.Header.Set("Authorization", "Bearer "+token)
	rec3 := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusOK {
		t.Fatalf("third deploy: expected 200, got %d: %s", rec3.Code, rec3.Body.String())
	}
	app3, err := store.GetAppBySlug("iconapp")
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	if app3.IconEmoji != "" {
		t.Errorf("icon_emoji after icon=\"\" deploy = %q, want empty", app3.IconEmoji)
	}
	mime, data, err := store.GetAppIcon("iconapp")
	if err != nil {
		t.Fatalf("get app icon: %v", err)
	}
	if mime != "image/png" || string(data) != "original-bytes" {
		t.Errorf("uploaded image lost: mime=%q data=%q", mime, data)
	}
}

// A failed deploy keeps the image and leaves the emoji alone. This fails
// against an implementation that uses the exclusive setter on the manifest
// path, and against one that adds the emoji to either revert store call.
func TestManifestIconSurvivesFailedDeploy(t *testing.T) {
	srv, store, token := newManifestE2EServer(t)
	admin, _ := store.GetUserByUsername("admin")

	if err := store.CreateApp(db.CreateAppParams{
		Slug: "failicon", Name: "Fail Icon App", OwnerID: admin.ID,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetAppIcon("failicon", "image/png", []byte("original-bytes")); err != nil {
		t.Fatalf("seed image: %v", err)
	}

	srv.SetDeployRunForTest(func(p deploy.Params) (*deploy.PoolResult, error) {
		return nil, errors.New("pool boot failed")
	})

	emoji := "\U0001F4CA"
	manifest := "[app]\nicon = \"" + emoji + "\"\n"
	body, ctype := buildMultiFileBundleUpload(t, map[string]string{
		"app.py":        "from shiny import App\n",
		"shinyhub.toml": manifest,
	})
	req := httptest.NewRequest("POST", "/api/apps/failicon/deploy", body)
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 on pool boot failure, got %d: %s", rec.Code, rec.Body.String())
	}

	mime, data, err := store.GetAppIcon("failicon")
	if err != nil {
		t.Fatalf("get app icon: %v", err)
	}
	if mime != "image/png" || string(data) != "original-bytes" {
		t.Errorf("uploaded image lost after failed deploy: mime=%q data=%q", mime, data)
	}
	app, err := store.GetAppBySlug("failicon")
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	if app.IconEmoji != emoji {
		t.Errorf("icon_emoji = %q, want %q (the revert deliberately skips it)", app.IconEmoji, emoji)
	}
}

// Phase A is all-or-nothing: a sibling field that fails INSIDE the
// transaction leaves icon_emoji unchanged.
//
// Use replicas = 33. Three checks read that value:
//  1. normalizeAndValidateApp rejects only < 1 (internal/deploy/hooks.go:317),
//     so the manifest loads clean.
//  2. validateManifestForServer rejects replicas > Runtime.MaxReplicas only
//     when that is > 0 (internal/api/manifest_apply.go:41, called at
//     internal/api/apps.go:1357). THE TEST SERVER'S Runtime.MaxReplicas MUST
//     BE 0. newManifestE2EServer gives exactly that: it passes a zero-value
//     config.RuntimeConfig{} (manifest_deploy_test.go:144), so the field is 0
//     and this gate is inert. So use it plain. Do NOT reach for
//     newManifestE2EServerCfg with a non-zero MaxReplicas, nor
//     newServerWithOwnedAppAndMaxReplicas
//     (manifest_apply_fixtures_test.go:25) - either caps replicas and the
//     deploy dies here with a 400, never reaching Phase A. A loader-built
//     config would also default it to 32 (internal/config/config.go:1995),
//     which is why the hand-built config in this harness is load-bearing
//     rather than incidental.
//  3. The UPDATE at internal/db/queries.go:3097 then violates the SQLite CHECK
//     replicas >= 1 AND replicas <= 32
//     (internal/db/migrations/sqlite/010_replicas.sql:2), inside the
//     transaction. That is the failure this test needs.
//
// Those three are only the checks that read replicas. The handler also runs a
// Fargate R-app guard (apps.go:1368), an ephemeral-data guard (:1379), a
// colocated-shared check (:1391), and BeginDeployment (:1405). All are inert
// for a plain Python app on a plain test server, but if the fixture app targets
// Fargate, has a pending ephemeral-data ack, or sits in a shared tier, the
// deploy dies at one of those and this test goes green without reaching Phase A.
//
// Do not assert on a status code: paths above Phase A return 400/500/422/409/500
// and Phase A itself returns 500 (apps.go:1469). Assert the positive observable
// instead - a deployment row in `failed` state (FailDeployment at apps.go:1467)
// and a response body of `manifest apply failed`. Every earlier exit leaves no
// deployment record at all (apps.go:1388-1390).
func TestManifestIconPhaseAAtomic(t *testing.T) {
	srv, store, token := newManifestE2EServer(t)
	admin, _ := store.GetUserByUsername("admin")
	if err := store.CreateApp(db.CreateAppParams{
		Slug: "atomic", Name: "Atomic", OwnerID: admin.ID,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetAppIconEmoji("atomic", "\U0001F4C8"); err != nil {
		t.Fatalf("seed emoji: %v", err)
	}

	manifest := "\n[app]\nicon = \"\U0001F4CA\"\nreplicas = 33\n"
	body, ctype := buildMultiFileBundleUpload(t, map[string]string{
		"app.py":        "from shiny import App\n",
		"shinyhub.toml": manifest,
	})
	req := httptest.NewRequest("POST", "/api/apps/atomic/deploy", body)
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	// Positive observable that the deploy died INSIDE Phase A, not above it.
	if !strings.Contains(rec.Body.String(), "manifest apply failed") {
		t.Fatalf("deploy did not fail in Phase A: %d %s", rec.Code, rec.Body.String())
	}
	app, err := store.GetAppBySlug("atomic")
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	// ListDeployments deliberately excludes pending/failed rows (it is the
	// "authoritative live-bundle history" view), so it can never see the row
	// this assertion needs; ListDeploymentsBySlug returns every status.
	deps, err := store.ListDeploymentsBySlug("atomic")
	if err != nil {
		t.Fatalf("list deployments: %v", err)
	}
	var sawFailed bool
	for _, d := range deps {
		if d.Status == db.DeploymentFailed {
			sawFailed = true
		}
	}
	if !sawFailed {
		t.Error("no failed deployment row; every exit above Phase A leaves no record at all, so this means the deploy never reached Phase A")
	}

	// The whole point: the emoji write is inside the transaction that failed.
	if app.IconEmoji != "\U0001F4C8" {
		t.Errorf("icon_emoji = %q, want the seeded value; Phase A was not atomic", app.IconEmoji)
	}
}

// A concurrent icon write during a failed deploy is not stomped. This is the
// race the design accepts rather than locks, pinned so a later reader does not
// "fix" it by adding the emoji to the revert.
func TestManifestIconConcurrentWriteWins(t *testing.T) {
	srv, store, token := newManifestE2EServer(t)
	admin, _ := store.GetUserByUsername("admin")

	if err := store.CreateApp(db.CreateAppParams{
		Slug: "raceicon", Name: "Race Icon App", OwnerID: admin.ID,
	}); err != nil {
		t.Fatal(err)
	}

	raceEmoji := "\U0001F525"
	// The pool-boot closure runs after Phase A has committed and before the
	// revert - exactly the window the design accepts as raced-but-not-locked.
	srv.SetDeployRunForTest(func(p deploy.Params) (*deploy.PoolResult, error) {
		if err := store.SetAppIconEmoji("raceicon", raceEmoji); err != nil {
			t.Fatalf("concurrent write: %v", err)
		}
		return nil, errors.New("pool boot failed")
	})

	manifest := "[app]\nicon = \"\U0001F4CA\"\n"
	body, ctype := buildMultiFileBundleUpload(t, map[string]string{
		"app.py":        "from shiny import App\n",
		"shinyhub.toml": manifest,
	})
	req := httptest.NewRequest("POST", "/api/apps/raceicon/deploy", body)
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 on pool boot failure, got %d: %s", rec.Code, rec.Body.String())
	}

	app, err := store.GetAppBySlug("raceicon")
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	if app.IconEmoji != raceEmoji {
		t.Errorf("icon_emoji = %q, want the concurrent write %q to survive the revert", app.IconEmoji, raceEmoji)
	}
}
