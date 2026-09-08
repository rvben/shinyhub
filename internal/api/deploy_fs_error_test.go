package api_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
)

// TestDeployApp_UnwritableAppsDirNamesTheCause drives a real filesystem failure
// through the deploy handler: the configured apps directory is made
// non-writable, so the very first store step (creating the bundles directory)
// fails with EACCES exactly as it would on a host whose apps volume is owned by
// the wrong user.
//
// The defect this pins is that the deployer used to be told "internal error",
// which is indistinguishable from a rejected bundle and sends them back to
// re-zip and re-upload work that was never the problem. The response must
// instead say the server could not write, and it must not leak the host path
// while doing so.
func TestDeployApp_UnwritableAppsDirNamesTheCause(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: directory permissions do not deny writes, so the failure cannot be provoked")
	}
	appsDir := t.TempDir()
	srv, store := newQuotaTestServer(t, appsDir, 0)

	hash, _ := testHashPassword("pass")
	_ = store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"})
	u, _ := store.GetUserByUsername("admin")
	_, _ = store.CreateApp(db.CreateAppParams{Slug: "ro", Name: "RO", OwnerID: u.ID})

	// Restore write permission before the temp dir is cleaned up, or the
	// harness cannot remove what it created.
	t.Cleanup(func() { _ = os.Chmod(appsDir, 0o755) })
	if err := os.Chmod(appsDir, 0o555); err != nil {
		t.Fatalf("make apps dir read-only: %v", err)
	}

	body, ctype := buildBundleUpload(t, "app.py", "print('hi')\n")
	token, _ := auth.IssueJWT(u.ID, u.Username, u.Role, "test-secret")
	req := httptest.NewRequest("POST", "/api/apps/ro/deploy", body)
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("Authorization", "Bearer "+token)

	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 for an unwritable apps dir, got %d: %s", rec.Code, rec.Body.String())
	}
	got := rec.Body.String()
	if !strings.Contains(got, "permission denied") {
		t.Errorf("response must name the permission failure, got %s", got)
	}
	if !strings.Contains(got, "not a problem with your bundle") {
		t.Errorf("response must tell the deployer their bundle is not at fault, got %s", got)
	}
	// The apps directory is the operator's private layout; naming it in a
	// deployer-facing response hands out server topology for free.
	if strings.Contains(got, appsDir) {
		t.Errorf("response leaked the host path %q: %s", appsDir, got)
	}
}
