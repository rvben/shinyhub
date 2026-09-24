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
	"github.com/rvben/shinyhub/internal/process"
)

// A first deploy that never comes up (the app crashes on boot, e.g. a Python
// traceback at import time) must leave apps.last_error populated with the
// boot diagnostic. Before this fix, only deployments.failure_reason (a
// separate column the Overview tab's failed-first-deploy box does not read)
// was written: a first deploy failure has no previous deployment to restore
// to, so the app's final status is "stopped", and none of the "stopped" call
// sites wrote a diagnostic into apps.last_error - it stayed "" forever.
func TestDeploy_FirstFailureRecordsLastError(t *testing.T) {
	appsDir := t.TempDir()
	srv, store := newQuotaTestServer(t, appsDir, 0)
	bootErr := errors.New("all replicas failed health check: replica 0: health: app at http://127.0.0.1:1/ crashed on startup before becoming healthy: " +
		`Traceback (most recent call last): File "app.py", line 1, in <module>: raise RuntimeError("boom at import")`)
	srv.SetDeployRunForTest(func(deploy.Params) (*deploy.PoolResult, error) {
		return nil, bootErr
	})

	hash, _ := testHashPassword("pass")
	_ = store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"})
	u, _ := store.GetUserByUsername("admin")
	_, _ = store.CreateApp(db.CreateAppParams{Slug: "boom", Name: "Boom", OwnerID: u.ID})

	body, ctype := buildBundleUpload(t, "app.py", "raise RuntimeError('boom at import')\n")
	token, _ := auth.IssueJWT(u.ID, u.Username, u.Role, "test-secret")
	req := httptest.NewRequest("POST", "/api/apps/boom/deploy", body)
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("Authorization", "Bearer "+token)

	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("deploy returned %d, want 500: %s", rec.Code, rec.Body.String())
	}

	app, err := store.GetApp("boom")
	if err != nil {
		t.Fatalf("GetApp: %v", err)
	}
	if app.Status != "stopped" {
		t.Fatalf("app.status = %q, want %q (a first deploy failure has no previous deployment to restore)", app.Status, "stopped")
	}
	if app.LastDeploymentStatus != "failed" {
		t.Fatalf("app.last_deployment_status = %q, want %q", app.LastDeploymentStatus, "failed")
	}
	if app.LastError == "" {
		t.Fatal("app.last_error is empty; the Overview tab's failed-first-deploy box has no error text to show")
	}
	if !strings.Contains(app.LastError, "boom at import") {
		t.Fatalf("app.last_error = %q, want it to contain the boot error's traceback text", app.LastError)
	}
}

// The diagnostic written to apps.last_error must stay bounded even when the
// boot error is unusually large, so a runaway traceback cannot bloat the apps
// row indefinitely. Exercises the real deploy handler, not BuildCrashDiagnostic
// directly, so the bound is proven on the value actually persisted.
func TestDeploy_LastErrorIsBounded(t *testing.T) {
	appsDir := t.TempDir()
	srv, store := newQuotaTestServer(t, appsDir, 0)
	huge := strings.Repeat("x", process.CrashDiagnosticMaxBytes*2)
	srv.SetDeployRunForTest(func(deploy.Params) (*deploy.PoolResult, error) {
		return nil, errors.New("all replicas failed health check: replica 0: health: app at http://127.0.0.1:1/ crashed on startup before becoming healthy: " + huge)
	})

	hash, _ := testHashPassword("pass")
	_ = store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"})
	u, _ := store.GetUserByUsername("admin")
	_, _ = store.CreateApp(db.CreateAppParams{Slug: "hugeerr", Name: "HugeErr", OwnerID: u.ID})

	body, ctype := buildBundleUpload(t, "app.py", "print(1)\n")
	token, _ := auth.IssueJWT(u.ID, u.Username, u.Role, "test-secret")
	req := httptest.NewRequest("POST", "/api/apps/hugeerr/deploy", body)
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("Authorization", "Bearer "+token)

	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("deploy returned %d, want 500: %s", rec.Code, rec.Body.String())
	}

	app, err := store.GetApp("hugeerr")
	if err != nil {
		t.Fatalf("GetApp: %v", err)
	}
	if app.LastError == "" {
		t.Fatal("app.last_error is empty")
	}
	// BuildCrashDiagnostic prefixes a truncated diagnostic with "...\n" before
	// the trailing CrashDiagnosticMaxBytes, so the persisted bound is
	// CrashDiagnosticMaxBytes plus that 4-byte marker, not an exact cap.
	const maxWithMarker = process.CrashDiagnosticMaxBytes + len("...\n")
	if len(app.LastError) > maxWithMarker {
		t.Fatalf("app.last_error is %d bytes, want at most %d", len(app.LastError), maxWithMarker)
	}
	if len(app.LastError) >= len(huge) {
		t.Fatalf("app.last_error was not truncated: %d bytes, input was %d bytes", len(app.LastError), len(huge))
	}
}
