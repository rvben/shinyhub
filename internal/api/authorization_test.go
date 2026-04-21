package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
)

// requireExplicitAppAccess is unexported, so this test runs in-package to
// exercise it without inventing test-only HTTP routes.
func newAuthTestServer(t *testing.T) (*Server, *db.Store) {
	t.Helper()
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Auth:    config.AuthConfig{Secret: "test-secret"},
		Storage: config.StorageConfig{AppsDir: t.TempDir(), AppDataDir: t.TempDir()},
	}
	srv := New(cfg, store, nil, nil)
	t.Cleanup(func() { store.Close() })
	return srv, store
}

func reqWithUser(u *auth.ContextUser) *http.Request {
	r := httptest.NewRequest("GET", "/", nil)
	if u != nil {
		r = r.WithContext(auth.WithUser(r.Context(), u))
	}
	return r
}

func TestRequireExplicitAppAccess_OwnerPasses(t *testing.T) {
	srv, store := newAuthTestServer(t)
	hash, _ := auth.HashPassword("pw")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "Demo", OwnerID: owner.ID})

	rr := httptest.NewRecorder()
	app, u, ok := srv.requireExplicitAppAccess(rr, reqWithUser(&auth.ContextUser{ID: owner.ID, Username: "owner", Role: "developer"}), "demo")
	if !ok {
		t.Fatalf("owner should pass, got %d %s", rr.Code, rr.Body.String())
	}
	if app == nil || u == nil {
		t.Fatalf("expected app and user, got %v %v", app, u)
	}
}

func TestRequireExplicitAppAccess_StrangerOnPublicAppRejected(t *testing.T) {
	srv, store := newAuthTestServer(t)
	hash, _ := auth.HashPassword("pw")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "Demo", OwnerID: owner.ID})
	// Make the app public so we can verify public visibility is NOT sufficient.
	if err := store.SetAppAccess("demo", "public"); err != nil {
		t.Fatalf("SetAppAccess: %v", err)
	}

	store.CreateUser(db.CreateUserParams{Username: "rando", PasswordHash: hash, Role: "developer"})
	rando, _ := store.GetUserByUsername("rando")

	rr := httptest.NewRecorder()
	_, _, ok := srv.requireExplicitAppAccess(rr, reqWithUser(&auth.ContextUser{ID: rando.ID, Username: "rando", Role: "developer"}), "demo")
	if ok {
		t.Fatal("stranger on public app must be rejected")
	}
	// Match requireViewApp's convention: 404, not 403.
	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rr.Code)
	}
}

func TestRequireExplicitAppAccess_StrangerOnSharedAppRejected(t *testing.T) {
	srv, store := newAuthTestServer(t)
	hash, _ := auth.HashPassword("pw")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "Demo", OwnerID: owner.ID})
	if err := store.SetAppAccess("demo", "shared"); err != nil {
		t.Fatalf("SetAppAccess: %v", err)
	}

	store.CreateUser(db.CreateUserParams{Username: "rando", PasswordHash: hash, Role: "developer"})
	rando, _ := store.GetUserByUsername("rando")

	rr := httptest.NewRecorder()
	_, _, ok := srv.requireExplicitAppAccess(rr, reqWithUser(&auth.ContextUser{ID: rando.ID, Username: "rando", Role: "developer"}), "demo")
	if ok {
		t.Fatal("stranger on shared app must be rejected (shared visibility alone is not explicit access)")
	}
	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rr.Code)
	}
}

func TestRequireExplicitAppAccess_ExplicitMemberPasses(t *testing.T) {
	srv, store := newAuthTestServer(t)
	hash, _ := auth.HashPassword("pw")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "Demo", OwnerID: owner.ID})
	if err := store.SetAppAccess("demo", "private"); err != nil {
		t.Fatalf("SetAppAccess: %v", err)
	}

	store.CreateUser(db.CreateUserParams{Username: "viewer1", PasswordHash: hash, Role: "developer"})
	viewer, _ := store.GetUserByUsername("viewer1")
	if err := store.GrantAppAccess("demo", viewer.ID); err != nil {
		t.Fatalf("GrantAppAccess: %v", err)
	}

	rr := httptest.NewRecorder()
	_, _, ok := srv.requireExplicitAppAccess(rr, reqWithUser(&auth.ContextUser{ID: viewer.ID, Username: "viewer1", Role: "developer"}), "demo")
	if !ok {
		t.Fatalf("explicit viewer should pass, got %d %s", rr.Code, rr.Body.String())
	}
}

func TestRequireExplicitAppAccess_AdminPasses(t *testing.T) {
	srv, store := newAuthTestServer(t)
	hash, _ := auth.HashPassword("pw")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "Demo", OwnerID: owner.ID})
	if err := store.SetAppAccess("demo", "private"); err != nil {
		t.Fatalf("SetAppAccess: %v", err)
	}

	store.CreateUser(db.CreateUserParams{Username: "ops", PasswordHash: hash, Role: "admin"})
	admin, _ := store.GetUserByUsername("ops")

	rr := httptest.NewRecorder()
	_, _, ok := srv.requireExplicitAppAccess(rr, reqWithUser(&auth.ContextUser{ID: admin.ID, Username: "ops", Role: "admin"}), "demo")
	if !ok {
		t.Fatalf("admin should pass, got %d %s", rr.Code, rr.Body.String())
	}
}

func TestRequireExplicitAppAccess_UnauthenticatedRejected(t *testing.T) {
	srv, store := newAuthTestServer(t)
	hash, _ := auth.HashPassword("pw")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "Demo", OwnerID: owner.ID})
	if err := store.SetAppAccess("demo", "public"); err != nil {
		t.Fatalf("SetAppAccess: %v", err)
	}

	rr := httptest.NewRecorder()
	_, _, ok := srv.requireExplicitAppAccess(rr, reqWithUser(nil), "demo")
	if ok {
		t.Fatal("unauthenticated request must be rejected")
	}
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rr.Code)
	}
}

func TestJITOAuthRole_DefaultsToViewer(t *testing.T) {
	srv, _ := newAuthTestServer(t)
	// newAuthTestServer leaves Auth.OAuthDefaultRole unset; the helper must
	// fall back to the safe default rather than panic or return "developer".
	if got, want := srv.jitOAuthRole(), "viewer"; got != want {
		t.Errorf("jitOAuthRole() = %q, want %q", got, want)
	}
}

func TestJITOAuthRole_HonorsConfig(t *testing.T) {
	srv, _ := newAuthTestServer(t)
	srv.cfg.Auth.OAuthDefaultRole = "developer"
	if got, want := srv.jitOAuthRole(), "developer"; got != want {
		t.Errorf("jitOAuthRole() = %q, want %q", got, want)
	}
}

func TestRequireExplicitAppAccess_MissingSlugIs404(t *testing.T) {
	srv, store := newAuthTestServer(t)
	hash, _ := auth.HashPassword("pw")
	store.CreateUser(db.CreateUserParams{Username: "ops", PasswordHash: hash, Role: "admin"})
	admin, _ := store.GetUserByUsername("ops")

	rr := httptest.NewRecorder()
	_, _, ok := srv.requireExplicitAppAccess(rr, reqWithUser(&auth.ContextUser{ID: admin.ID, Username: "ops", Role: "admin"}), "missing")
	if ok {
		t.Fatal("missing slug must yield false")
	}
	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rr.Code)
	}
}
