package api_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
)

// A deploy that fails its readiness wait must return failure_kind:readiness_timeout
// in the 500 body so the CLI can distinguish it from a real crash.
func TestDeploy_FailureKindReadinessTimeout(t *testing.T) {
	appsDir := t.TempDir()
	srv, store := newQuotaTestServer(t, appsDir, 0)
	srv.SetDeployRunForTest(func(deploy.Params) (*deploy.PoolResult, error) {
		return nil, errors.New("all replicas failed health check: replica 0: health: app at http://127.0.0.1:1/ did not become healthy within 120s")
	})

	hash, _ := testHashPassword("pass")
	_ = store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"})
	u, _ := store.GetUserByUsername("admin")
	_ = store.CreateApp(db.CreateAppParams{Slug: "ff", Name: "FF", OwnerID: u.ID})

	body, ctype := buildBundleUpload(t, "app.py", "print(1)\n")
	token, _ := auth.IssueJWT(u.ID, u.Username, u.Role, "test-secret")
	req := httptest.NewRequest("POST", "/api/apps/ff/deploy", body)
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("Authorization", "Bearer "+token)

	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("deploy returned %d, want 500: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"failure_kind":"readiness_timeout"`) {
		t.Fatalf("500 body must carry failure_kind:readiness_timeout, got: %s", rec.Body.String())
	}
}

// A failed post-deploy hook must return failure_kind:hook_failed. This pins the
// invariant the classifier's anchored marker depends on: failure_kind is
// computed from the RAW deploy error, while the human "error" text is the
// wrapped "deploy failed: a post-deploy hook ... - hook[0] (...)" form. That
// wrapped text does NOT start with the anchored "hook[" marker, so a client
// falling back to classifying it would misclassify - here specifically back to
// runtime_missing, the exact bug hook_failed exists to remove. The fallback stays
// unreachable only while this response always carries the kind.
func TestDeploy_FailureKindHookFailed(t *testing.T) {
	appsDir := t.TempDir()
	srv, store := newQuotaTestServer(t, appsDir, 0)
	srv.SetDeployRunForTest(func(deploy.Params) (*deploy.PoolResult, error) {
		// A hook invoking a binary the host lacks: the shape that used to be
		// reported as a missing server runtime.
		return nil, errors.New(`hook[0] (Rscript setup.R): exec: "Rscript": executable file not found in $PATH`)
	})

	hash, _ := testHashPassword("pass")
	_ = store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"})
	u, _ := store.GetUserByUsername("admin")
	_ = store.CreateApp(db.CreateAppParams{Slug: "hf", Name: "HF", OwnerID: u.ID})

	body, ctype := buildBundleUpload(t, "app.py", "print(1)\n")
	token, _ := auth.IssueJWT(u.ID, u.Username, u.Role, "test-secret")
	req := httptest.NewRequest("POST", "/api/apps/hf/deploy", body)
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("Authorization", "Bearer "+token)

	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("deploy returned %d, want 500: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"failure_kind":"hook_failed"`) {
		t.Fatalf("500 body must carry failure_kind:hook_failed, got: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "R runtime not found") {
		t.Errorf("hook failure must not blame the server runtime, got: %s", rec.Body.String())
	}
}
