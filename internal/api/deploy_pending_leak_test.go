package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
)

// stageColocationConflict places app slug on a remote node while granting it
// shared data from a source app pinned to the control plane, so
// checkColocatedShared rejects the next deploy/rollback of slug with 409.
// Mirrors the fixture in colocated_wiring_test.go.
func stageColocationConflict(t *testing.T, srv interface {
	SetNodeForTier(func(tier string) string)
}, store *db.Store, ownerID int64, slug string) {
	t.Helper()
	if err := store.CreateApp(db.CreateAppParams{
		Slug: "colo-source", Name: "Source", OwnerID: ownerID, Access: "private",
	}); err != nil {
		t.Fatalf("create source app: %v", err)
	}
	source, err := store.GetAppBySlug("colo-source")
	if err != nil {
		t.Fatalf("get source: %v", err)
	}
	consumer, err := store.GetAppBySlug(slug)
	if err != nil {
		t.Fatalf("get consumer: %v", err)
	}
	sourcePlacement, _ := json.Marshal(map[string]int{"local": 1})
	if err := store.SetAppPlacement(source.ID, string(sourcePlacement), 1); err != nil {
		t.Fatalf("set source placement: %v", err)
	}
	consumerPlacement, _ := json.Marshal(map[string]int{"burst": 1})
	if err := store.SetAppPlacement(consumer.ID, string(consumerPlacement), 1); err != nil {
		t.Fatalf("set consumer placement: %v", err)
	}
	if err := store.GrantSharedData(consumer.ID, source.ID); err != nil {
		t.Fatalf("grant shared data: %v", err)
	}
	srv.SetNodeForTier(func(tier string) string {
		if tier == "burst" {
			return "node-a"
		}
		return ""
	})
}

// mustNoInflight asserts the pending-row invariant this task establishes: an
// early-returning deploy/rollback must not leave a pending deployment row.
func mustNoInflight(t *testing.T, store *db.Store, when string) {
	t.Helper()
	in, err := store.ListInflightDeployments()
	if err != nil {
		t.Fatalf("%s: ListInflightDeployments: %v", when, err)
	}
	if len(in) != 0 {
		t.Fatalf("%s: leaked pending deployment rows: %+v", when, in)
	}
}

// A deploy rejected by the colocated-shared check must leave no deployment
// record at all: no pending row (a leaked one corrupts last_deployment_status
// and, with the deploy-aware wait page, would pin visitors on "Deploying app"
// until restart) and no failed row either (a rejected FIRST deploy keeps the
// app in the never-deployed state, like every other validation rejection).
func TestDeployApp_ColocatedConflictLeavesNoDeploymentRecord(t *testing.T) {
	appsDir := t.TempDir()
	srv, store := newQuotaTestServer(t, appsDir, 0) // quota disabled

	hash, _ := testHashPassword("pass")
	_ = store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"})
	u, _ := store.GetUserByUsername("admin")
	_ = store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "Demo", OwnerID: u.ID})
	stageColocationConflict(t, srv, store, u.ID, "demo")

	body, ctype := buildBundleUpload(t, "app.py", "print('hi')\n")
	token, _ := auth.IssueJWT(u.ID, u.Username, u.Role, "test-secret")
	req := httptest.NewRequest("POST", "/api/apps/demo/deploy", body)
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("Authorization", "Bearer "+token)

	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 colocated conflict, got %d: %s", rec.Code, rec.Body.String())
	}
	mustNoInflight(t, store, "after 409 deploy")

	// No record of any kind: the app must still count as never-deployed, so
	// the never-deployed page (not the stopped page) keeps serving.
	app, _ := store.GetAppBySlug("demo")
	if app.LastDeploymentStatus != "" {
		t.Errorf("last_deployment_status = %q, want empty (no record)", app.LastDeploymentStatus)
	}
	if has, err := store.HasAnyDeployment(app.ID); err != nil || has {
		t.Errorf("HasAnyDeployment = (%v, %v), want (false, nil)", has, err)
	}
}

// A rollback rejected by the colocated-shared check must leave no deployment
// record and the live pool untouched. Covers the rollback handler's validation
// reordering (the ephemeral-guard returns move in the same edit; staging a
// Fargate tier to drive them independently is not worth the fixture cost, see
// the plan's review log).
func TestRollbackApp_ColocatedConflictLeavesNoDeploymentRecord(t *testing.T) {
	appsDir := t.TempDir()
	srv, store := newQuotaTestServer(t, appsDir, 0)

	hash, _ := testHashPassword("pass")
	_ = store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"})
	u, _ := store.GetUserByUsername("admin")
	_ = store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "Demo", OwnerID: u.ID})
	app, _ := store.GetAppBySlug("demo")

	// Two succeeded deployments with real bundle dirs so the rollback target
	// passes the on-disk validation that runs before BeginDeployment.
	var target *db.Deployment
	for _, v := range []string{"v1", "v2"} {
		dir := filepath.Join(appsDir, "demo", "versions", v)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		dep, err := store.CreateDeployment(db.CreateDeploymentParams{
			AppID: app.ID, Version: v, BundleDir: dir,
		})
		if err != nil {
			t.Fatal(err)
		}
		if v == "v1" {
			target = dep
		}
	}
	stageColocationConflict(t, srv, store, u.ID, "demo")

	token, _ := auth.IssueJWT(u.ID, u.Username, u.Role, "test-secret")
	req := httptest.NewRequest("POST", "/api/apps/demo/rollback",
		strings.NewReader(fmt.Sprintf(`{"deployment_id": %d}`, target.ID)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 colocated conflict, got %d: %s", rec.Code, rec.Body.String())
	}
	mustNoInflight(t, store, "after 409 rollback")

	// The newest record must still be the succeeded v2, not a pending or
	// failed rollback attempt.
	app, _ = store.GetAppBySlug("demo")
	if app.LastDeploymentStatus != db.DeploymentSucceeded {
		t.Errorf("last_deployment_status = %q, want %q", app.LastDeploymentStatus, db.DeploymentSucceeded)
	}
}
