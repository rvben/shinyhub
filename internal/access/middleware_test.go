package access_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvben/shinyhub/internal/access"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
)

func makeStore(t *testing.T) *db.Store {
	t.Helper()
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	return store
}

func next(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }

func TestAccess_PublicApp_NoAuth(t *testing.T) {
	store := makeStore(t)
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "h", Role: "admin"})
	owner, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "pub", Name: "Public", OwnerID: owner.ID})
	store.SetAppAccess("pub", "public")

	mw := access.Middleware(store, "test-secret")
	handler := mw(http.HandlerFunc(next))

	req := httptest.NewRequest("GET", "/app/pub/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("public app: expected 200, got %d", rec.Code)
	}
}

func TestAccess_PrivateApp_NoAuth_Rejected(t *testing.T) {
	store := makeStore(t)
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "h", Role: "admin"})
	owner, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "priv", Name: "Private", OwnerID: owner.ID})

	mw := access.Middleware(store, "test-secret")
	handler := mw(http.HandlerFunc(next))

	req := httptest.NewRequest("GET", "/app/priv/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("private app no auth: expected 401, got %d", rec.Code)
	}
}

func TestAccess_PrivateApp_OwnerAccess(t *testing.T) {
	store := makeStore(t)
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "h", Role: "admin"})
	owner, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "priv", Name: "Private", OwnerID: owner.ID})

	token, _ := auth.IssueJWT(owner.ID, "owner", "admin", "test-secret")

	mw := access.Middleware(store, "test-secret")
	handler := mw(http.HandlerFunc(next))

	req := httptest.NewRequest("GET", "/app/priv/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("owner access: expected 200, got %d", rec.Code)
	}
}

func TestAccess_PrivateApp_CookieAuth(t *testing.T) {
	store := makeStore(t)
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "h", Role: "admin"})
	owner, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "priv", Name: "Private", OwnerID: owner.ID})

	token, _ := auth.IssueJWT(owner.ID, "owner", "admin", "test-secret")

	mw := access.Middleware(store, "test-secret")
	handler := mw(http.HandlerFunc(next))

	req := httptest.NewRequest("GET", "/app/priv/", nil)
	req.AddCookie(&http.Cookie{Name: "shiny_session", Value: token})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("cookie auth: expected 200, got %d", rec.Code)
	}
}

func TestAccess_PrivateApp_GrantedUser(t *testing.T) {
	store := makeStore(t)
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "h", Role: "admin"})
	store.CreateUser(db.CreateUserParams{Username: "alice", PasswordHash: "h", Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")
	alice, _ := store.GetUserByUsername("alice")
	store.CreateApp(db.CreateAppParams{Slug: "priv", Name: "Private", OwnerID: owner.ID})
	store.GrantAppAccess("priv", alice.ID)

	token, _ := auth.IssueJWT(alice.ID, "alice", "developer", "test-secret")

	mw := access.Middleware(store, "test-secret")
	handler := mw(http.HandlerFunc(next))

	req := httptest.NewRequest("GET", "/app/priv/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("granted user: expected 200, got %d", rec.Code)
	}
}
