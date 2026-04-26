package access_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
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

	mw := access.Middleware(store, "test-secret", nil)
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

	mw := access.Middleware(store, "test-secret", nil)
	handler := mw(http.HandlerFunc(next))

	req := httptest.NewRequest("GET", "/app/priv/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("private app no auth: expected 401, got %d", rec.Code)
	}
}

// Browser navigation requests must get a styled HTML "Sign in" page rather
// than plain text "unauthorized" — that page is what the user sees when they
// open a private app URL while logged out.
func TestAccess_PrivateApp_BrowserNav_GetsStyledHTMLPage(t *testing.T) {
	store := makeStore(t)
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "h", Role: "admin"})
	owner, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "secret", Name: "Quarterly Report", OwnerID: owner.ID})

	mw := access.Middleware(store, "test-secret", nil)
	handler := mw(http.HandlerFunc(next))

	req := httptest.NewRequest("GET", "/app/secret/", nil)
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("expected text/html, got %q", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Sign in to access this app") {
		t.Errorf("body missing headline: %s", body)
	}
	if !strings.Contains(body, "Quarterly Report") {
		t.Errorf("body should name the app so the user knows what they were trying to open: %s", body)
	}
	if !strings.Contains(body, "/?next=%2Fapp%2Fsecret%2F") {
		t.Errorf("body should link to login with next= param so the user can return after auth: %s", body)
	}
}

// CLI/SDK callers (Authorization header set) must keep getting the legacy
// JSON envelope so existing scripts don't break.
func TestAccess_PrivateApp_APICall_GetsJSON(t *testing.T) {
	store := makeStore(t)
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "h", Role: "admin"})
	owner, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "priv", Name: "Private", OwnerID: owner.ID})

	mw := access.Middleware(store, "test-secret", nil)
	handler := mw(http.HandlerFunc(next))

	req := httptest.NewRequest("GET", "/app/priv/api/data", nil)
	req.Header.Set("Authorization", "Bearer bogus")
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("expected application/json, got %q", ct)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"error":"unauthorized"}` {
		t.Errorf("expected JSON envelope, got %q", got)
	}
}

func TestAccess_PrivateApp_OwnerAccess(t *testing.T) {
	store := makeStore(t)
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "h", Role: "admin"})
	owner, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "priv", Name: "Private", OwnerID: owner.ID})

	token, _ := auth.IssueJWT(owner.ID, "owner", "admin", "test-secret")

	mw := access.Middleware(store, "test-secret", nil)
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

	mw := access.Middleware(store, "test-secret", nil)
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

	mw := access.Middleware(store, "test-secret", nil)
	handler := mw(http.HandlerFunc(next))

	req := httptest.NewRequest("GET", "/app/priv/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("granted user: expected 200, got %d", rec.Code)
	}
}

func TestAccess_PrivateApp_NonMember_Forbidden(t *testing.T) {
	store := makeStore(t)
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "h", Role: "admin"})
	store.CreateUser(db.CreateUserParams{Username: "bob", PasswordHash: "h", Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")
	bob, _ := store.GetUserByUsername("bob")
	store.CreateApp(db.CreateAppParams{Slug: "priv", Name: "Private", OwnerID: owner.ID})
	// Bob is authenticated but NOT granted access and is not an admin.

	token, _ := auth.IssueJWT(bob.ID, "bob", "developer", "test-secret")

	mw := access.Middleware(store, "test-secret", nil)
	handler := mw(http.HandlerFunc(next))

	req := httptest.NewRequest("GET", "/app/priv/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("non-member: expected 403, got %d", rec.Code)
	}
}

func TestAccess_SharedApp_AuthenticatedUser(t *testing.T) {
	store := makeStore(t)
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "h", Role: "developer"})
	store.CreateUser(db.CreateUserParams{Username: "stranger", PasswordHash: "h", Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")
	stranger, _ := store.GetUserByUsername("stranger")
	store.CreateApp(db.CreateAppParams{Slug: "shared-app", Name: "Shared", OwnerID: owner.ID})
	store.SetAppAccess("shared-app", "shared")
	// stranger is NOT in app_members but is authenticated.

	token, _ := auth.IssueJWT(stranger.ID, "stranger", "developer", "test-secret")

	mw := access.Middleware(store, "test-secret", nil)
	handler := mw(http.HandlerFunc(next))

	req := httptest.NewRequest("GET", "/app/shared-app/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("shared app: authenticated user expected 200, got %d", rec.Code)
	}
}

func TestAccess_PrivateApp_OperatorBypasses(t *testing.T) {
	store := makeStore(t)
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "h", Role: "developer"})
	store.CreateUser(db.CreateUserParams{Username: "ops", PasswordHash: "h", Role: "operator"})
	owner, _ := store.GetUserByUsername("owner")
	ops, _ := store.GetUserByUsername("ops")
	store.CreateApp(db.CreateAppParams{Slug: "priv", Name: "Private", OwnerID: owner.ID})
	// ops is NOT granted access — bypass must come from role alone

	token, _ := auth.IssueJWT(ops.ID, "ops", "operator", "test-secret")

	mw := access.Middleware(store, "test-secret", nil)
	handler := mw(http.HandlerFunc(next))

	req := httptest.NewRequest("GET", "/app/priv/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("operator bypass: expected 200, got %d", rec.Code)
	}
}
