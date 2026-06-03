package api_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
)

// mkUser creates a user with the given role and returns an authed JWT for them.
func mkUser(t *testing.T, store *db.Store, username, role string) (int64, string) {
	t.Helper()
	hash, _ := auth.HashPassword("pass")
	if err := store.CreateUser(db.CreateUserParams{Username: username, PasswordHash: hash, Role: role}); err != nil {
		t.Fatalf("create user %s: %v", username, err)
	}
	u, err := store.GetUserByUsername(username)
	if err != nil {
		t.Fatalf("get user %s: %v", username, err)
	}
	tok, _ := auth.IssueJWT(u.ID, username, role, "test-secret")
	return u.ID, tok
}

func do(t *testing.T, srv interface{ Router() http.Handler }, method, path, token string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, authedRequest(t, method, path, body, token))
	return rec
}

// TestListAppEnv_RequiresManage verifies env listing is manager-only: a
// non-manager (here an unrelated user on a public app) cannot read env config,
// even the non-secret values, while the owner can.
func TestListAppEnv_RequiresManage(t *testing.T) {
	srv, store := newTestServer(t)
	ownerID, ownerTok := mkUser(t, store, "owner", "developer")
	_, intruderTok := mkUser(t, store, "intruder", "developer")
	if err := store.CreateApp(db.CreateAppParams{Slug: "pub", Name: "Pub", OwnerID: ownerID}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetAppAccess("pub", "public"); err != nil {
		t.Fatal(err)
	}
	app, _ := store.GetAppBySlug("pub")
	if err := store.UpsertAppEnvVar(app.ID, "DB_URL", []byte("postgres://internal-host/db"), false); err != nil {
		t.Fatal(err)
	}

	if rec := do(t, srv, "GET", "/api/apps/pub/env", intruderTok, nil); rec.Code != http.StatusForbidden {
		t.Errorf("non-manager GET env = %d, want 403 (config must not leak); body=%s", rec.Code, rec.Body.String())
	}
	if rec := do(t, srv, "GET", "/api/apps/pub/env", ownerTok, nil); rec.Code != http.StatusOK {
		t.Errorf("owner GET env = %d, want 200", rec.Code)
	}
}

// TestListSchedules_RequiresExplicitAccess verifies schedule listing on a public
// app is not readable by an unrelated authenticated user; explicit access
// (owner/admin/operator/member) is required.
func TestListSchedules_RequiresExplicitAccess(t *testing.T) {
	srv, store := newTestServer(t)
	ownerID, ownerTok := mkUser(t, store, "owner", "developer")
	_, strangerTok := mkUser(t, store, "stranger", "developer")
	if err := store.CreateApp(db.CreateAppParams{Slug: "pub", Name: "Pub", OwnerID: ownerID}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetAppAccess("pub", "public"); err != nil {
		t.Fatal(err)
	}

	if rec := do(t, srv, "GET", "/api/apps/pub/schedules", strangerTok, nil); rec.Code != http.StatusNotFound {
		t.Errorf("unrelated user GET schedules = %d, want 404 (no public-surface leak); body=%s", rec.Code, rec.Body.String())
	}
	if rec := do(t, srv, "GET", "/api/apps/pub/schedules", ownerTok, nil); rec.Code != http.StatusOK {
		t.Errorf("owner GET schedules = %d, want 200", rec.Code)
	}
}

// TestListAppEnv_MemberViewerDenied confirms a viewer-member of a private app
// also cannot read env (env is manager-only, matching the Configuration UI).
func TestListAppEnv_MemberViewerDenied(t *testing.T) {
	srv, store := newTestServer(t)
	ownerID, _ := mkUser(t, store, "owner", "developer")
	memberID, memberTok := mkUser(t, store, "member", "developer")
	if err := store.CreateApp(db.CreateAppParams{Slug: "priv", Name: "Priv", OwnerID: ownerID}); err != nil {
		t.Fatal(err)
	}
	if err := store.GrantAppAccess("priv", memberID); err != nil { // viewer member
		t.Fatal(err)
	}
	if rec := do(t, srv, "GET", "/api/apps/priv/env", memberTok, nil); rec.Code != http.StatusForbidden {
		t.Errorf("viewer-member GET env = %d, want 403 (env is manager-only); body=%s", rec.Code, rec.Body.String())
	}
}

// TestGetUser_NotEnumerableByViewer verifies a plain viewer (e.g. an auto-
// provisioned OAuth account) can no longer resolve arbitrary usernames to user
// ids, while an app operator (developer) still can for the grant flow.
func TestGetUser_NotEnumerableByViewer(t *testing.T) {
	srv, store := newTestServer(t)
	mkUser(t, store, "alice", "developer") // target
	_, viewerTok := mkUser(t, store, "nosy", "viewer")
	_, devTok := mkUser(t, store, "dev", "developer")

	if rec := do(t, srv, "GET", "/api/users/alice", viewerTok, nil); rec.Code != http.StatusForbidden {
		t.Errorf("viewer user lookup = %d, want 403 (no enumeration); body=%s", rec.Code, rec.Body.String())
	}
	if rec := do(t, srv, "GET", "/api/users/alice", devTok, nil); rec.Code != http.StatusOK {
		t.Errorf("developer user lookup = %d, want 200 (grant flow); body=%s", rec.Code, rec.Body.String())
	}
}

// TestGrantAppAccess_ByUsername verifies a manager can grant access by username
// (server-side resolution), so the UI no longer needs a separate user-lookup.
func TestGrantAppAccess_ByUsername(t *testing.T) {
	srv, store := newTestServer(t)
	ownerID, ownerTok := mkUser(t, store, "owner", "developer")
	aliceID, _ := mkUser(t, store, "alice", "developer")
	if err := store.CreateApp(db.CreateAppParams{Slug: "app", Name: "App", OwnerID: ownerID}); err != nil {
		t.Fatal(err)
	}

	rec := do(t, srv, "POST", "/api/apps/app/members", ownerTok, []byte(`{"username":"alice"}`))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("grant by username = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}
	ok, err := store.UserCanAccessApp("app", aliceID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("alice was not granted access after grant-by-username")
	}
}
