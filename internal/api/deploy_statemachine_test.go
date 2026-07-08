package api_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
)

// TestDeployApp_FailedDeployRestoresPreviousPool verifies the durable deploy
// state machine: a deploy that fails after the running pool was torn down must
// (1) not shift the authoritative live-bundle pointer, (2) mark its pending
// row failed, and (3) bring the previous deployment's pool back up.
func TestDeployApp_FailedDeployRestoresPreviousPool(t *testing.T) {
	appsDir := t.TempDir()
	srv, store := newQuotaTestServer(t, appsDir, 0) // quota disabled

	hash, _ := testHashPassword("pass")
	_ = store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"})
	u, _ := store.GetUserByUsername("admin")
	_ = store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "Demo", OwnerID: u.ID})
	app, _ := store.GetAppBySlug("demo")

	// A previous good deployment that exists on disk.
	v1Dir := filepath.Join(appsDir, "demo", "versions", "v1")
	if err := os.MkdirAll(v1Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateDeployment(db.CreateDeploymentParams{
		AppID: app.ID, Version: "v1", BundleDir: v1Dir,
	}); err != nil {
		t.Fatal(err)
	}

	// The new deploy fails; the restore of v1 (different bundle dir) succeeds.
	srv.SetDeployRunForTest(func(p deploy.Params) (*deploy.PoolResult, error) {
		if p.BundleDir == v1Dir {
			return &deploy.PoolResult{Replicas: []deploy.Result{{Index: 0, PID: 1, Port: 1}}}, nil
		}
		return nil, deploy.ErrBundleRejected
	})

	body, ctype := buildBundleUpload(t, "app.py", "print('hi')\n")
	token, _ := auth.IssueJWT(u.ID, u.Username, u.Role, "test-secret")
	req := httptest.NewRequest("POST", "/api/apps/demo/deploy", body)
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("Authorization", "Bearer "+token)

	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 on failed deploy, got %d: %s", rec.Code, rec.Body.String())
	}

	// The pending row for the failed attempt must have been failed, not left
	// dangling for startup recovery to adopt.
	if in, err := store.ListInflightDeployments(); err != nil || len(in) != 0 {
		t.Fatalf("inflight after failed deploy = %+v err=%v, want none", in, err)
	}

	// The authoritative pointer must still be v1 (never shifted to the failed
	// deploy), so restart/recovery/scheduler keep serving the good bundle.
	live, err := store.ListDeployments(app.ID)
	if err != nil {
		t.Fatalf("ListDeployments: %v", err)
	}
	if len(live) != 1 || live[0].Version != "v1" {
		t.Fatalf("live pointer = %+v, want only v1", live)
	}

	// The previous pool was restored, so the app is running again.
	got, _ := store.GetAppBySlug("demo")
	if got.Status != "running" {
		t.Errorf("app status = %q, want running (previous pool restored)", got.Status)
	}
}
