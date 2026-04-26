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

	mw := access.Middleware(store, "test-secret", nil, nil)
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

	mw := access.Middleware(store, "test-secret", nil, nil)
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
//
// Critically, the page must NOT include the app's name. Doing so would let
// an unauthenticated caller enumerate private app titles by guessing slugs.
// The test apps are deliberately given a recognisable name so a regression
// that re-leaks it fails this assertion loudly.
func TestAccess_PrivateApp_BrowserNav_GetsStyledHTMLPage(t *testing.T) {
	store := makeStore(t)
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "h", Role: "admin"})
	owner, _ := store.GetUserByUsername("owner")
	const privateAppName = "Quarterly Report"
	store.CreateApp(db.CreateAppParams{Slug: "secret", Name: privateAppName, OwnerID: owner.ID})

	mw := access.Middleware(store, "test-secret", nil, nil)
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
	if strings.Contains(body, privateAppName) {
		t.Errorf("body LEAKS private app name %q — anyone guessing the slug can enumerate titles. Body: %s", privateAppName, body)
	}
	if !strings.Contains(body, "/?next=%2Fapp%2Fsecret%2F") {
		t.Errorf("body should link to login with next= param so the user can return after auth: %s", body)
	}
}

// 403 page (logged in as the wrong account) must NOT just link to /?next=.
// Re-using the existing session would re-authorise the same wrong user and
// loop back to the same 403. The link must include logout=1 so the SPA's
// bootstrap calls /api/auth/logout before showing the login form. Without
// this distinction, the "Log in" CTA on the 403 page is a literal infinite
// loop for any user who is signed in to the wrong account.
func TestAccess_Forbidden_BrowserNav_LinksToLogoutThenLogin(t *testing.T) {
	store := makeStore(t)
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "h", Role: "admin"})
	store.CreateUser(db.CreateUserParams{Username: "bob", PasswordHash: "h", Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")
	bob, _ := store.GetUserByUsername("bob")
	const privateAppName = "Quarterly Report"
	store.CreateApp(db.CreateAppParams{Slug: "secret", Name: privateAppName, OwnerID: owner.ID})

	bobToken, _ := auth.IssueJWT(bob.ID, "bob", "developer", "test-secret")

	mw := access.Middleware(store, "test-secret", nil, nil)
	handler := mw(http.HandlerFunc(next))

	req := httptest.NewRequest("GET", "/app/secret/", nil)
	req.AddCookie(&http.Cookie{Name: "shiny_session", Value: bobToken})
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "logout=1") {
		t.Errorf("403 page must link to /?logout=1&next=... so the SPA logs the wrong-account session out before showing login. Body: %s", body)
	}
	if !strings.Contains(body, "next=%2Fapp%2Fsecret%2F") {
		t.Errorf("403 page must preserve next= so the user lands back on the app after re-auth: %s", body)
	}
	if strings.Contains(body, privateAppName) {
		t.Errorf("403 body LEAKS app name %q to a non-member: %s", privateAppName, body)
	}
	// The CTA must set a same-tab sessionStorage marker before navigating.
	// Without this, an external link to /?logout=1&next=/app/anything/
	// could trigger a GET-driven logout in any tab. The SPA's
	// consumeLogoutParam refuses to act unless the marker is present, so
	// the producer must plant it on real clicks. The literal apostrophes
	// in the JS are HTML-escaped to &#39; in the rendered attribute, so we
	// match against the escaped form.
	if !strings.Contains(body, "shiny_logout_intent") {
		t.Errorf("403 CTA must set sessionStorage `shiny_logout_intent` via onclick so the SPA can distinguish a real click on this page from an external/forged link. Body: %s", body)
	}
	if !strings.Contains(body, "sessionStorage.setItem(") {
		t.Errorf("403 CTA must call sessionStorage.setItem so the marker survives the in-tab navigation. Body: %s", body)
	}
}

// 401 page (no session) must NOT plant the logout-intent marker. The
// marker is exclusively a 403-handoff signal — leaking it on the 401 path
// would let a logged-out user trigger the logout flow on a different
// session if they happened to share the tab via account switch.
func TestAccess_Unauthorized_BrowserNav_DoesNotPlantLogoutMarker(t *testing.T) {
	store := makeStore(t)
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "h", Role: "admin"})
	owner, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "secret", Name: "Quarterly Report", OwnerID: owner.ID})

	mw := access.Middleware(store, "test-secret", nil, nil)
	handler := mw(http.HandlerFunc(next))

	req := httptest.NewRequest("GET", "/app/secret/", nil)
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "shiny_logout_intent") {
		t.Errorf("401 page must not plant the logout-intent marker — that's a 403-only signal. Body: %s", body)
	}
}

// CLI/SDK callers (Authorization header set) must keep getting the legacy
// JSON envelope so existing scripts don't break.
func TestAccess_PrivateApp_APICall_GetsJSON(t *testing.T) {
	store := makeStore(t)
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "h", Role: "admin"})
	owner, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "priv", Name: "Private", OwnerID: owner.ID})

	mw := access.Middleware(store, "test-secret", nil, nil)
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

	mw := access.Middleware(store, "test-secret", nil, nil)
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

	mw := access.Middleware(store, "test-secret", nil, nil)
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

	mw := access.Middleware(store, "test-secret", nil, nil)
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

	mw := access.Middleware(store, "test-secret", nil, nil)
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

	mw := access.Middleware(store, "test-secret", nil, nil)
	handler := mw(http.HandlerFunc(next))

	req := httptest.NewRequest("GET", "/app/shared-app/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("shared app: authenticated user expected 200, got %d", rec.Code)
	}
}

// TestAccess_PrivateApp_DemotedAdmin_LosesBypass guards the live-DB
// re-resolution path. An admin's JWT carries role="admin" until it
// expires (potentially hours). Without a live userLookup, that stale
// claim keeps the admin-bypass open after the user has been demoted —
// the same staleness bug the API middleware fixes via its own
// userLookup wiring (internal/api/router.go). The access middleware
// MUST behave the same way for /app/* traffic; otherwise revoking
// admin powers doesn't actually revoke access to any private app.
//
// We exercise both paths:
//   - With nil userLookup: the stale "admin" claim wins (legacy /
//     test-only behaviour) and the request goes through.
//   - With a live userLookup that returns the post-demotion role:
//     the bypass is gone and a non-member 403 is returned.
func TestAccess_PrivateApp_DemotedAdmin_LosesBypass(t *testing.T) {
	store := makeStore(t)
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "h", Role: "developer"})
	store.CreateUser(db.CreateUserParams{Username: "exadmin", PasswordHash: "h", Role: "admin"})
	owner, _ := store.GetUserByUsername("owner")
	exadmin, _ := store.GetUserByUsername("exadmin")
	store.CreateApp(db.CreateAppParams{Slug: "priv", Name: "Private", OwnerID: owner.ID})

	// JWT was minted while exadmin was still admin.
	token, _ := auth.IssueJWT(exadmin.ID, "exadmin", "admin", "test-secret")

	// Demote the admin in the DB. The token is unchanged.
	if err := store.UpdateUserRole(exadmin.ID, "developer"); err != nil {
		t.Fatalf("demote: %v", err)
	}

	// Live lookup: read the current role from DB on every request.
	live := func(id int64) (*auth.ContextUser, error) {
		u, err := store.GetUserByID(id)
		if err != nil {
			return nil, err
		}
		return &auth.ContextUser{ID: u.ID, Username: u.Username, Role: u.Role}, nil
	}

	doRequest := func(t *testing.T, lookup auth.UserLookup) int {
		t.Helper()
		mw := access.Middleware(store, "test-secret", nil, lookup)
		handler := mw(http.HandlerFunc(next))
		req := httptest.NewRequest("GET", "/app/priv/", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}

	// Sanity: with no live lookup, the stale "admin" claim still wins.
	// This pins the *unfixed* behaviour so a future regression that
	// quietly disables userLookup wiring shows up as the test pair
	// flipping in lockstep instead of silently re-opening the bypass.
	if got := doRequest(t, nil); got != http.StatusOK {
		t.Fatalf("nil userLookup: stale admin claim should bypass (got %d, want 200) — this case documents the pre-fix behaviour", got)
	}

	// With the live lookup, the demotion takes effect immediately.
	if got := doRequest(t, live); got != http.StatusForbidden {
		t.Fatalf("live userLookup: demoted admin should be 403 (got %d) — role demotion must take effect without waiting for token expiry", got)
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

	mw := access.Middleware(store, "test-secret", nil, nil)
	handler := mw(http.HandlerFunc(next))

	req := httptest.NewRequest("GET", "/app/priv/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("operator bypass: expected 200, got %d", rec.Code)
	}
}
