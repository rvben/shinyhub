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
// loop back to the same 403. The CTA is an HTML <form> POSTing to
// /api/auth/handoff so the server clears the cookie before the user lands on
// the login form. The previous implementation used a /?logout=1 anchor gated
// by a sessionStorage marker planted via onclick — that broke when the
// access-denied page was opened in a brand-new tab (Cmd+Click), because the
// new tab had no marker and the SPA refused to log out, bouncing the user
// straight back to the same 403. Form POSTs have no per-tab dependency.
func TestAccess_Forbidden_BrowserNav_HandsOffViaFormPOST(t *testing.T) {
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
	if !strings.Contains(body, `<form method="POST" action="/api/auth/handoff">`) {
		t.Errorf("403 CTA must be a <form method=POST action=/api/auth/handoff> so the server clears the cookie regardless of which tab the page is in. Body: %s", body)
	}
	if !strings.Contains(body, `<input type="hidden" name="next" value="/app/secret/">`) {
		t.Errorf("403 CTA must carry the original RequestURI as a hidden `next` field so the user lands back on the app after re-auth. Body: %s", body)
	}
	if strings.Contains(body, privateAppName) {
		t.Errorf("403 body LEAKS app name %q to a non-member: %s", privateAppName, body)
	}
	// The previous design's failure modes must not regress.
	if strings.Contains(body, "logout=1") {
		t.Errorf("403 page must not link to /?logout=1 — handoff is server-side now. Body: %s", body)
	}
	if strings.Contains(body, "shiny_logout_intent") {
		t.Errorf("403 page must not plant a sessionStorage marker — the form POST does the handoff server-side, no per-tab marker needed. Body: %s", body)
	}
	if strings.Contains(body, "<a class=\"btn\"") {
		t.Errorf("403 CTA must be a <form>+<button>, not an <a>: an anchor opens in a new tab on Cmd+Click and the handoff is lost. Body: %s", body)
	}
}

// 401 page (no session) keeps the simple anchor → /?next=<original> pattern
// because there's no session to revoke. It must NOT carry the handoff form
// (which is a 403-only signal) and must NOT plant the now-removed
// sessionStorage marker.
func TestAccess_Unauthorized_BrowserNav_LinksToLoginWithNext(t *testing.T) {
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
	if !strings.Contains(body, `href="/?next=%2Fapp%2Fsecret%2F"`) {
		t.Errorf("401 page must link to /?next=<original> so the user lands back on the app after login. Body: %s", body)
	}
	if strings.Contains(body, "/api/auth/handoff") {
		t.Errorf("401 page must not carry the handoff form — there's no session to revoke. Body: %s", body)
	}
	if strings.Contains(body, "shiny_logout_intent") {
		t.Errorf("401 page must not plant the logout-intent marker — that's a 403-only signal and the marker is no longer used at all. Body: %s", body)
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
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
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
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
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
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
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
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
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
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
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

// /app/* is the path a Shiny app's own frontend uses to talk back to its
// own backend, so it commonly carries an `Authorization: Bearer ...`
// header meant for the embedded app — not for ShinyHub. The access
// middleware MUST authenticate strictly from the session cookie on
// /app/* and ignore any Authorization header, otherwise the embedded
// app's header gets routed into ShinyHub's JWT validator and rejects a
// perfectly valid browser session with a 401.
func TestAccess_PrivateApp_IgnoresAppAuthorizationHeader(t *testing.T) {
	store := makeStore(t)
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "h", Role: "admin"})
	owner, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "priv", Name: "Private", OwnerID: owner.ID})

	token, _ := auth.IssueJWT(owner.ID, "owner", "admin", "test-secret")

	mw := access.Middleware(store, "test-secret", nil, nil)
	handler := mw(http.HandlerFunc(next))

	req := httptest.NewRequest("GET", "/app/priv/", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	// The embedded Shiny app sends its OWN authorization header. ShinyHub
	// must ignore it and use the cookie instead.
	req.Header.Set("Authorization", "Bearer some-other-systems-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("cookie-with-foreign-Authorization: expected 200, got %d — Authorization header on /app/* must not block a valid session cookie", rec.Code)
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
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("operator bypass: expected 200, got %d", rec.Code)
	}
}

// An embedded Shiny app may forward its own `Authorization: Bearer ...`
// header on a top-level navigation to /app/<slug>/. If the user is signed out
// (no session cookie), the access middleware must still respect the browser
// fetch-metadata signals and serve the styled HTML access-denied page —
// otherwise the foreign Authorization header silently swaps the page for a
// raw `{"error":"unauthorized"}` JSON body in the browser tab.
func TestAccess_Unauthorized_BrowserNav_WithForeignAuthHeader_GetsHTML(t *testing.T) {
	store := makeStore(t)
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "h", Role: "admin"})
	owner, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "secret", Name: "Quarterly Report", OwnerID: owner.ID})

	mw := access.Middleware(store, "test-secret", nil, nil)
	handler := mw(http.HandlerFunc(next))

	req := httptest.NewRequest("GET", "/app/secret/", nil)
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	// The embedded app forwards its OWN bearer token. The middleware already
	// ignores Authorization on /app/* (cookie-only auth); the response format
	// must equally not be skewed by the header's presence.
	req.Header.Set("Authorization", "Bearer some-other-systems-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Authorization header on a browser navigation must NOT downgrade the styled HTML page to JSON, got Content-Type %q", ct)
	}
	if !strings.Contains(rec.Body.String(), "Sign in to access this app") {
		t.Errorf("body should be the styled HTML page, got %q", rec.Body.String())
	}
}

// Mirror image for the 403 case: an embedded app forwarding its own bearer
// token while the user is signed in as the wrong account must still see the
// styled HTML handoff form, not a JSON envelope.
func TestAccess_Forbidden_BrowserNav_WithForeignAuthHeader_GetsHTML(t *testing.T) {
	store := makeStore(t)
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "h", Role: "admin"})
	store.CreateUser(db.CreateUserParams{Username: "bob", PasswordHash: "h", Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")
	bob, _ := store.GetUserByUsername("bob")
	store.CreateApp(db.CreateAppParams{Slug: "secret", Name: "Quarterly Report", OwnerID: owner.ID})

	bobToken, _ := auth.IssueJWT(bob.ID, "bob", "developer", "test-secret")

	mw := access.Middleware(store, "test-secret", nil, nil)
	handler := mw(http.HandlerFunc(next))

	req := httptest.NewRequest("GET", "/app/secret/", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: bobToken})
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("Authorization", "Bearer some-other-systems-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Authorization header on a browser navigation must NOT downgrade the styled HTML page to JSON, got Content-Type %q", ct)
	}
	if !strings.Contains(rec.Body.String(), `<form method="POST" action="/api/auth/handoff">`) {
		t.Errorf("body should carry the handoff form, got %q", rec.Body.String())
	}
}
