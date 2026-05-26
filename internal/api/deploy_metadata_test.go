package api_test

// Tests that verify deployment metadata (DeploymentID, AppVersion, ContentDigest)
// is threaded through every launch path and stamped onto replica rows.
//
// Harness modeled on deploy_statemachine_test.go and deploy_quota_test.go:
// newQuotaTestServer + SetDeployRunForTest + buildBundleUpload + ServeHTTP.

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

// TestDeployStampsDeploymentMetadataOnReplicas verifies that a successful
// deploy via the HTTP handler both (a) passes the live deployment's metadata
// through deploy.Params to the runtime and (b) stamps each replica row with
// the live deployment's ID and version.
func TestDeployStampsDeploymentMetadataOnReplicas(t *testing.T) {
	appsDir := t.TempDir()
	srv, store := newQuotaTestServer(t, appsDir, 0)

	// Capture the Params received by the production deploy path so we can
	// assert the metadata fields were threaded through.
	var gotParams deploy.Params
	srv.SetDeployRunForTest(func(p deploy.Params) (*deploy.PoolResult, error) {
		gotParams = p
		return &deploy.PoolResult{Replicas: []deploy.Result{{Index: 0, PID: 1, Port: 20001}}}, nil
	})

	hash, _ := auth.HashPassword("pass")
	_ = store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"})
	u, _ := store.GetUserByUsername("admin")
	_ = store.CreateApp(db.CreateAppParams{Slug: "meta", Name: "Meta", OwnerID: u.ID})
	app, _ := store.GetAppBySlug("meta")

	body, ctype := buildBundleUpload(t, "app.py", "print('v1')\n")
	token, _ := auth.IssueJWT(u.ID, u.Username, u.Role, "test-secret")
	req := httptest.NewRequest("POST", "/api/apps/meta/deploy", body)
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("Authorization", "Bearer "+token)

	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("deploy returned %d: %s", rec.Code, rec.Body.String())
	}

	// Live deployment row must exist.
	deps, err := store.ListDeployments(app.ID)
	if err != nil || len(deps) == 0 {
		t.Fatalf("ListDeployments: %v (len=%d)", err, len(deps))
	}
	live := deps[0]

	// Verify the metadata was threaded through deploy.Params to the runtime.
	if gotParams.DeploymentID != live.ID {
		t.Errorf("deploy.Params.DeploymentID = %d, want %d", gotParams.DeploymentID, live.ID)
	}
	if gotParams.AppVersion != live.Version {
		t.Errorf("deploy.Params.AppVersion = %q, want %q", gotParams.AppVersion, live.Version)
	}
	if gotParams.ContentDigest != live.ContentDigest {
		t.Errorf("deploy.Params.ContentDigest = %q, want %q", gotParams.ContentDigest, live.ContentDigest)
	}

	// Each replica must carry the live deployment's ID and version.
	replicas, err := store.ListReplicas(app.ID)
	if err != nil {
		t.Fatalf("ListReplicas: %v", err)
	}
	if len(replicas) == 0 {
		t.Fatal("no replicas after deploy")
	}
	for _, rep := range replicas {
		if rep.DeploymentID == nil {
			t.Errorf("replica %d: DeploymentID is nil, want %d", rep.Index, live.ID)
			continue
		}
		if *rep.DeploymentID != live.ID {
			t.Errorf("replica %d: DeploymentID = %d, want %d", rep.Index, *rep.DeploymentID, live.ID)
		}
		if rep.AppVersion != live.Version {
			t.Errorf("replica %d: AppVersion = %q, want %q", rep.Index, rep.AppVersion, live.Version)
		}
	}
}

// TestRollbackUsesPendingIDAndTargetDigest verifies the rollback two-row rule:
// the new live deployment row must carry the target's ContentDigest and
// Version, but its own (new) ID; replica rows must point to the new live ID;
// and deploy.Params must carry the target digest, target version, and the new
// pending ID (not the original target row ID).
func TestRollbackUsesPendingIDAndTargetDigest(t *testing.T) {
	appsDir := t.TempDir()
	srv, store := newQuotaTestServer(t, appsDir, 0)

	// The stub is invoked for both the v2 forward deploy and the rollback.
	// It overwrites gotParams on each call, so after the rollback the var
	// holds the Params from the rollback invocation, which is what we assert.
	var gotParams deploy.Params
	srv.SetDeployRunForTest(func(p deploy.Params) (*deploy.PoolResult, error) {
		gotParams = p
		return &deploy.PoolResult{Replicas: []deploy.Result{{Index: 0, PID: 2, Port: 20002}}}, nil
	})

	hash, _ := auth.HashPassword("pass")
	_ = store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"})
	u, _ := store.GetUserByUsername("admin")
	_ = store.CreateApp(db.CreateAppParams{Slug: "rbtgt", Name: "RBTgt", OwnerID: u.ID})
	app, _ := store.GetAppBySlug("rbtgt")

	// v1 bundle dir must exist on disk (rollback validates before tearing down the pool).
	v1Dir := filepath.Join(appsDir, "rbtgt", "versions", "v1")
	if err := os.MkdirAll(v1Dir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Seed a v1 deployment directly (already promoted / succeeded).
	v1Dep, err := store.CreateDeployment(db.CreateDeploymentParams{
		AppID:     app.ID,
		Version:   "v1",
		BundleDir: v1Dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Give v1 a known digest so we can verify it propagates through rollback.
	const v1Digest = "sha256:aabbcc"
	if err := store.SetDeploymentDigest(v1Dep.ID, v1Digest); err != nil {
		t.Fatal(err)
	}

	// Deploy v2 via HTTP so there are two succeeded rows and v2 is live.
	body2, ctype2 := buildBundleUpload(t, "app.py", "print('v2')\n")
	token, _ := auth.IssueJWT(u.ID, u.Username, u.Role, "test-secret")
	req2 := httptest.NewRequest("POST", "/api/apps/rbtgt/deploy", body2)
	req2.Header.Set("Content-Type", ctype2)
	req2.Header.Set("Authorization", "Bearer "+token)
	rec2 := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("v2 deploy returned %d: %s", rec2.Code, rec2.Body.String())
	}

	// Confirm v2 is live and v1 is the rollback target (index 1).
	deps, err := store.ListDeployments(app.ID)
	if err != nil || len(deps) < 2 {
		t.Fatalf("expected >= 2 deployments; len=%d err=%v", len(deps), err)
	}
	target := deps[1] // v1 is the rollback target
	if target.Version != "v1" {
		t.Fatalf("expected rollback target version v1, got %q", target.Version)
	}

	// Roll back via HTTP.
	reqRB := httptest.NewRequest("POST", "/api/apps/rbtgt/rollback", nil)
	reqRB.Header.Set("Authorization", "Bearer "+token)
	recRB := httptest.NewRecorder()
	srv.Router().ServeHTTP(recRB, reqRB)
	if recRB.Code != http.StatusOK {
		t.Fatalf("rollback returned %d: %s", recRB.Code, recRB.Body.String())
	}

	// New live deployment: different ID than v1 target, same digest and version.
	newDeps, err := store.ListDeployments(app.ID)
	if err != nil || len(newDeps) == 0 {
		t.Fatalf("ListDeployments after rollback: %v (len=%d)", err, len(newDeps))
	}
	newLive := newDeps[0]

	if newLive.ID == target.ID {
		t.Errorf("new live deployment ID == target ID (%d); expected a new pending row", target.ID)
	}
	if newLive.ContentDigest != v1Digest {
		t.Errorf("new live ContentDigest = %q, want %q", newLive.ContentDigest, v1Digest)
	}
	if newLive.Version != "v1" {
		t.Errorf("new live Version = %q, want %q", newLive.Version, "v1")
	}

	// Verify the rollback's deploy.Params carried the target digest/version
	// and the new pending deployment ID (not the original v1 target row ID).
	if gotParams.ContentDigest != v1Digest {
		t.Errorf("deploy.Params.ContentDigest = %q, want %q", gotParams.ContentDigest, v1Digest)
	}
	if gotParams.AppVersion != "v1" {
		t.Errorf("deploy.Params.AppVersion = %q, want v1", gotParams.AppVersion)
	}
	if gotParams.DeploymentID != newLive.ID {
		t.Errorf("deploy.Params.DeploymentID = %d, want new live ID %d", gotParams.DeploymentID, newLive.ID)
	}

	// Replica rows must point to the new live deployment ID, not the original v1 target ID.
	replicas, err := store.ListReplicas(app.ID)
	if err != nil {
		t.Fatalf("ListReplicas: %v", err)
	}
	if len(replicas) == 0 {
		t.Fatal("no replicas after rollback")
	}
	for _, rep := range replicas {
		if rep.DeploymentID == nil {
			t.Errorf("replica %d: DeploymentID is nil after rollback, want %d", rep.Index, newLive.ID)
			continue
		}
		if *rep.DeploymentID != newLive.ID {
			t.Errorf("replica %d: DeploymentID = %d, want new live ID %d", rep.Index, *rep.DeploymentID, newLive.ID)
		}
	}
}
