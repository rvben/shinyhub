package access_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/access"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

func makeStore(t *testing.T) *db.Store {
	t.Helper()
	return dbtest.New(t)
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

func TestSupportGuardPreventsIdentityFallbackOutsideBoundApp(t *testing.T) {
	store := makeStore(t)
	if err := store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "h", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	owner, _ := store.GetUserByUsername("owner")
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "other", Name: "Other", OwnerID: owner.ID, Access: "public"}); err != nil {
		t.Fatal(err)
	}
	token, err := auth.IssueJWT(owner.ID, owner.Username, owner.Role, "test-secret")
	if err != nil {
		t.Fatal(err)
	}

	reached := false
	handler := access.Middleware(store, "test-secret", nil, store.LookupContextUser)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/app/other/", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	req.AddCookie(&http.Cookie{Name: auth.SupportSessionGuardCookieName, Value: "unknown-support-id"})
	// Model a forward-auth deployment too: neither upstream identity nor the
	// ordinary cookie may punch through the support-session boundary, and the
	// public app must not quietly serve the request anonymously either.
	req = req.WithContext(auth.WithUser(req.Context(), &auth.ContextUser{ID: owner.ID, Username: owner.Username, Role: owner.Role}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict || reached {
		t.Fatalf("status=%d reached=%v, want an explicit guard page and no app access", rec.Code, reached)
	}
	if !strings.Contains(rec.Body.String(), "Support session guard active") {
		t.Fatalf("guard page missing for an unknown session id: %s", rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", cc)
	}
}

// TestSupportGuardOnlyRequestNamesTheBoundSession covers the browser state
// after a support launch: the root guard travels to every app on the origin,
// while the support cookie only travels to the bound app. A request that
// carries the guard alone must end on a page that names the session and
// offers a way back or out, never on the anonymous rendering of the app.
func TestSupportGuardOnlyRequestNamesTheBoundSession(t *testing.T) {
	store := makeStore(t)
	for _, user := range []db.CreateUserParams{
		{Username: "admin", PasswordHash: "h", Role: "admin"},
		{Username: "alice", PasswordHash: "h", Role: "viewer"},
	} {
		if err := store.CreateUser(user); err != nil {
			t.Fatal(err)
		}
	}
	admin, _ := store.GetUserByUsername("admin")
	alice, _ := store.GetUserByUsername("alice")
	for _, slug := range []string{"sales", "other"} {
		if _, err := store.CreateApp(db.CreateAppParams{Slug: slug, Name: slug, OwnerID: admin.ID, Access: "public"}); err != nil {
			t.Fatal(err)
		}
	}
	sales, _ := store.GetAppBySlug("sales")
	expires := time.Now().UTC().Add(15 * time.Minute)
	if err := store.CreateSupportSession(db.CreateSupportSessionParams{
		ID: "support-id", ActorUserID: admin.ID, ActorUsername: admin.Username, ActorTokenEpoch: admin.TokenEpoch,
		SubjectUserID: alice.ID, SubjectUsername: alice.Username, SubjectRole: alice.Role, SubjectTokenEpoch: alice.TokenEpoch,
		AppID: sales.ID, AppSlug: "sales", Reason: "Investigating SUP-4001", LaunchCodeHash: "launch-hash", ExpiresAt: expires,
	}); err != nil {
		t.Fatal(err)
	}
	reached := false
	handler := access.Middleware(store, "test-secret", nil, store.LookupContextUser)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		_, _ = w.Write([]byte("app content"))
	}))
	serve := func(slug string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/app/"+slug+"/", nil)
		req.AddCookie(&http.Cookie{Name: auth.SupportSessionGuardCookieName, Value: "support-id"})
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusConflict || reached || strings.Contains(rec.Body.String(), "app content") {
			t.Fatalf("/app/%s/: status=%d reached=%v body=%s", slug, rec.Code, reached, rec.Body.String())
		}
		return rec
	}

	// Live session, request on a different app: point back to the bound app
	// and keep the stop control, which posts to the bound app's stop endpoint
	// where the support cookie does travel.
	body := serve("other").Body.String()
	for _, want := range []string{"Support session paused", "alice", "admin", `href="/app/sales/"`,
		`action="/app/sales/.shinyhub/support-session/stop"`, "End support session"} {
		if !strings.Contains(body, want) {
			t.Fatalf("guard page on another app missing %q: %s", want, body)
		}
	}

	// Live session, request on the bound app itself without its cookie: the
	// stop endpoint could not act on it, so no stop form is offered.
	body = serve("sales").Body.String()
	if !strings.Contains(body, "Support session paused") || !strings.Contains(body, "app-scoped cookie is missing") {
		t.Fatalf("guard page on the bound app has the wrong copy: %s", body)
	}
	if strings.Contains(body, "<form") {
		t.Fatalf("guard page on the bound app offers a stop form that cannot succeed: %s", body)
	}

	// Ended session: the guard outlives the stop by design, and the page says
	// so instead of the app quietly rendering for nobody in particular.
	if _, err := store.StopSupportSession("support-id", "ended_by_actor", "192.0.2.10"); err != nil {
		t.Fatal(err)
	}
	body = serve("other").Body.String()
	for _, want := range []string{"Support session ended", "original deadline", "alice", "admin"} {
		if !strings.Contains(body, want) {
			t.Fatalf("guard page after stop missing %q: %s", want, body)
		}
	}
	if strings.Contains(body, "<form") || strings.Contains(body, "Return to the support session") {
		t.Fatalf("guard page after stop still offers session controls: %s", body)
	}
}

func TestRevokedAppAccessKeepsSupportSafetyRail(t *testing.T) {
	store := makeStore(t)
	for _, user := range []db.CreateUserParams{
		{Username: "admin", PasswordHash: "h", Role: "admin"},
		{Username: "owner", PasswordHash: "h", Role: "developer"},
		{Username: "alice", PasswordHash: "h", Role: "viewer"},
	} {
		if err := store.CreateUser(user); err != nil {
			t.Fatal(err)
		}
	}
	admin, _ := store.GetUserByUsername("admin")
	owner, _ := store.GetUserByUsername("owner")
	alice, _ := store.GetUserByUsername("alice")
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "sales", Name: "Sales", OwnerID: owner.ID, Access: "private"}); err != nil {
		t.Fatal(err)
	}
	app, _ := store.GetAppBySlug("sales")
	if err := store.CreateSupportSession(db.CreateSupportSessionParams{
		ID: "support-id", ActorUserID: admin.ID, ActorUsername: admin.Username, ActorTokenEpoch: admin.TokenEpoch,
		SubjectUserID: alice.ID, SubjectUsername: alice.Username, SubjectRole: alice.Role, SubjectTokenEpoch: alice.TokenEpoch,
		AppID: app.ID, AppSlug: app.Slug, Reason: "Investigating SUP-6001", LaunchCodeHash: "access-hash",
		ExpiresAt: time.Now().Add(15 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	issued := &auth.ContextUser{ID: alice.ID, Username: alice.Username, Role: alice.Role, TokenEpoch: alice.TokenEpoch,
		SupportSession: &auth.SupportSessionContext{
			ID: "support-id", ActorID: admin.ID, ActorUsername: admin.Username, ActorTokenEpoch: admin.TokenEpoch,
			AppID: app.ID, AppSlug: "sales", ExpiresAt: time.Now().Add(15 * time.Minute),
		},
	}
	token, _, err := auth.IssueSessionTokenWithInfo(issued, "test-secret")
	if err != nil {
		t.Fatal(err)
	}
	handler := access.Middleware(store, "test-secret", nil, store.LookupContextUser)(http.HandlerFunc(next))
	req := httptest.NewRequest(http.MethodGet, "/app/sales/", nil)
	req.Header.Set("Accept", "text/html")
	req.AddCookie(&http.Cookie{Name: auth.SupportSessionCookieName, Value: token})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d, want 403", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "shinyhub-support-session-loader") || !strings.Contains(body, "End support session") {
		t.Fatalf("support safety rail missing from revoked-access page: %s", body)
	}
}

func TestSupportSessionRejectsPublicAppRecreatedAtSameSlug(t *testing.T) {
	store := makeStore(t)
	for _, user := range []db.CreateUserParams{
		{Username: "admin", PasswordHash: "h", Role: "admin"},
		{Username: "alice", PasswordHash: "h", Role: "viewer"},
	} {
		if err := store.CreateUser(user); err != nil {
			t.Fatal(err)
		}
	}
	admin, _ := store.GetUserByUsername("admin")
	alice, _ := store.GetUserByUsername("alice")
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "sales", Name: "Original", OwnerID: admin.ID, Access: "public"}); err != nil {
		t.Fatal(err)
	}
	original, _ := store.GetAppBySlug("sales")
	// Keep a higher row alive so SQLite cannot recycle the deleted rowid.
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "anchor", Name: "Anchor", OwnerID: admin.ID, Access: "private"}); err != nil {
		t.Fatal(err)
	}
	issued := &auth.ContextUser{ID: alice.ID, Username: alice.Username, Role: alice.Role, TokenEpoch: alice.TokenEpoch,
		SupportSession: &auth.SupportSessionContext{
			ID: "support-id", ActorID: admin.ID, ActorUsername: admin.Username, ActorTokenEpoch: admin.TokenEpoch,
			AppID: original.ID, AppSlug: "sales", ExpiresAt: time.Now().Add(15 * time.Minute),
		},
	}
	token, _, err := auth.IssueSessionTokenWithInfo(issued, "test-secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteApp("sales"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "sales", Name: "Replacement", OwnerID: admin.ID, Access: "public"}); err != nil {
		t.Fatal(err)
	}
	reached := false
	handler := access.Middleware(store, "test-secret", nil, store.LookupContextUser)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		_, _ = w.Write([]byte("replacement content"))
	}))
	req := httptest.NewRequest(http.MethodGet, "/app/sales/", nil)
	req.AddCookie(&http.Cookie{Name: auth.SupportSessionCookieName, Value: token})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict || reached || strings.Contains(rec.Body.String(), "replacement content") {
		t.Fatalf("status=%d reached=%v body=%s", rec.Code, reached, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "no longer the app approved") || !strings.Contains(rec.Body.String(), "End support session") {
		t.Fatalf("scope-blocked safety page missing: %s", rec.Body.String())
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
	if !strings.Contains(body, `<title>Sign in to access this app · ShinyHub</title>`) {
		t.Errorf("access-denied browser title is not generic ShinyHub identity: %s", body)
	}
	if strings.Contains(body, privateAppName) {
		t.Errorf("body LEAKS private app name %q — anyone guessing the slug can enumerate titles. Body: %s", privateAppName, body)
	}
	if !strings.Contains(body, "/login?next=%2Fapp%2Fsecret%2F") {
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

// 401 page (no session) keeps the simple anchor to /login?next=<original>
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
	if !strings.Contains(body, `href="/login?next=%2Fapp%2Fsecret%2F"`) {
		t.Errorf("401 page must link to /login?next=<original> so the user lands back on the app after login. Body: %s", body)
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
	// exadmin holds a stale JWT minted while they were an admin, but their live
	// DB role is now developer (e.g. they have since been demoted). We model that
	// divergence directly: the row is developer, the token still claims admin.
	store.CreateUser(db.CreateUserParams{Username: "exadmin", PasswordHash: "h", Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")
	exadmin, _ := store.GetUserByUsername("exadmin")
	store.CreateApp(db.CreateAppParams{Slug: "priv", Name: "Private", OwnerID: owner.ID})

	// JWT was minted while exadmin was still an admin; the token is unchanged.
	token, _ := auth.IssueJWT(exadmin.ID, "exadmin", "admin", "test-secret")

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

// TestAccess_PrivateApp_GroupMemberAllowed verifies that a user whose only access
// comes from a group rule (no app_members row, no direct grant) passes the
// access middleware on a private app. UserCanAccessApp already covers the group
// path in its SQL (via app_group_access JOIN user_groups); this test pins that
// the middleware delegates to it correctly so group membership grants /app/* access.
func TestAccess_PrivateApp_GroupMemberAllowed(t *testing.T) {
	store := makeStore(t)
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "h", Role: "developer"})
	store.CreateUser(db.CreateUserParams{Username: "groupmember", PasswordHash: "h", Role: "developer"})
	store.CreateUser(db.CreateUserParams{Username: "outsider", PasswordHash: "h", Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")
	member, _ := store.GetUserByUsername("groupmember")
	outsider, _ := store.GetUserByUsername("outsider")

	store.CreateApp(db.CreateAppParams{Slug: "grp", Name: "Group App", OwnerID: owner.ID})
	// groupmember is in the "devs" group, which has a viewer grant on the app.
	store.ReplaceUserGroups(member.ID, []string{"devs"})
	store.GrantAppGroupAccess("grp", "devs", "viewer", "manual")
	// outsider has no group or direct membership.

	mw := access.Middleware(store, "test-secret", nil, nil)
	handler := mw(http.HandlerFunc(next))

	memberToken, _ := auth.IssueJWT(member.ID, "groupmember", "developer", "test-secret")
	req := httptest.NewRequest("GET", "/app/grp/", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: memberToken})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("group member: expected 200, got %d", rec.Code)
	}

	outsiderToken, _ := auth.IssueJWT(outsider.ID, "outsider", "developer", "test-secret")
	req2 := httptest.NewRequest("GET", "/app/grp/", nil)
	req2.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: outsiderToken})
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusForbidden {
		t.Errorf("outsider (no group): expected 403, got %d", rec2.Code)
	}
}

// TestAccess_PrivateApp_ForwardAuthContextUser verifies that a request whose
// context already carries a forward-auth ContextUser (set by ForwardAuthMiddleware
// upstream of access.Middleware) is authorised for a private app even when no
// session cookie is present. This is the /app/* forward-auth path: the top-level
// mux wraps the entire handler chain with ForwardAuthMiddleware, which attaches
// the user to the context; access.Middleware must honour that context user rather
// than demanding a fresh cookie.
func TestAccess_PrivateApp_ForwardAuthContextUser(t *testing.T) {
	store := makeStore(t)
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "h", Role: "admin"})
	owner, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "priv", Name: "Private", OwnerID: owner.ID})

	// Simulate what ForwardAuthMiddleware does: attach an admin ContextUser to the
	// request context. No session cookie is set on the request.
	mw := access.Middleware(store, "test-secret", nil, nil)
	handler := mw(http.HandlerFunc(next))

	req := httptest.NewRequest("GET", "/app/priv/", nil)
	// Attach via auth.WithUser the way ForwardAuthMiddleware does.
	ctx := auth.WithUser(req.Context(), &auth.ContextUser{ID: owner.ID, Username: "owner", Role: "admin"})
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("forward-auth context admin: expected 200, got %d — access middleware must honour context users set by ForwardAuthMiddleware", rec.Code)
	}
}

// TestMiddleware_PropagatesUserToContext_PrivateApp verifies that when an
// authenticated member accesses a private app, the resolved user is attached
// to the request context seen by the downstream handler.
func TestMiddleware_PropagatesUserToContext_PrivateApp(t *testing.T) {
	store := makeStore(t)
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "h", Role: "admin"})
	store.CreateUser(db.CreateUserParams{Username: "alice", PasswordHash: "h", Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")
	alice, _ := store.GetUserByUsername("alice")
	store.CreateApp(db.CreateAppParams{Slug: "priv", Name: "Private", OwnerID: owner.ID})
	store.GrantAppAccess("priv", alice.ID)

	token, _ := auth.IssueJWT(alice.ID, "alice", "developer", "test-secret")

	mw := access.Middleware(store, "test-secret", nil, nil)

	var gotUser *auth.ContextUser
	capture := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser = auth.UserFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	handler := mw(capture)

	req := httptest.NewRequest("GET", "/app/priv/", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if gotUser == nil {
		t.Fatal("auth.UserFromContext returned nil in next handler — user must be propagated into context")
	}
	if gotUser.Username != "alice" {
		t.Errorf("expected Username %q, got %q", "alice", gotUser.Username)
	}
}

// TestMiddleware_PropagatesUserToContext_SharedAndAdminBypass verifies that
// the admin/operator/shared bypass paths also propagate the user into context.
func TestMiddleware_PropagatesUserToContext_SharedAndAdminBypass(t *testing.T) {
	store := makeStore(t)
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "h", Role: "developer"})
	store.CreateUser(db.CreateUserParams{Username: "stranger", PasswordHash: "h", Role: "developer"})
	store.CreateUser(db.CreateUserParams{Username: "adminuser", PasswordHash: "h", Role: "admin"})
	owner, _ := store.GetUserByUsername("owner")
	stranger, _ := store.GetUserByUsername("stranger")
	adminuser, _ := store.GetUserByUsername("adminuser")
	store.CreateApp(db.CreateAppParams{Slug: "shared-app", Name: "Shared", OwnerID: owner.ID})
	store.SetAppAccess("shared-app", "shared")
	store.CreateApp(db.CreateAppParams{Slug: "priv", Name: "Private", OwnerID: owner.ID})

	t.Run("shared app plain user", func(t *testing.T) {
		token, _ := auth.IssueJWT(stranger.ID, "stranger", "developer", "test-secret")
		mw := access.Middleware(store, "test-secret", nil, nil)
		var gotUser *auth.ContextUser
		capture := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotUser = auth.UserFromContext(r.Context())
			w.WriteHeader(http.StatusOK)
		})
		handler := mw(capture)
		req := httptest.NewRequest("GET", "/app/shared-app/", nil)
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		if gotUser == nil {
			t.Fatal("shared app bypass: auth.UserFromContext returned nil — user must be in context")
		}
		if gotUser.Username != "stranger" {
			t.Errorf("expected Username %q, got %q", "stranger", gotUser.Username)
		}
	})

	t.Run("private app admin bypass", func(t *testing.T) {
		token, _ := auth.IssueJWT(adminuser.ID, "adminuser", "admin", "test-secret")
		mw := access.Middleware(store, "test-secret", nil, nil)
		var gotUser *auth.ContextUser
		capture := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotUser = auth.UserFromContext(r.Context())
			w.WriteHeader(http.StatusOK)
		})
		handler := mw(capture)
		req := httptest.NewRequest("GET", "/app/priv/", nil)
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		if gotUser == nil {
			t.Fatal("admin bypass: auth.UserFromContext returned nil — user must be in context")
		}
		if gotUser.Username != "adminuser" {
			t.Errorf("expected Username %q, got %q", "adminuser", gotUser.Username)
		}
	})
}

// TestMiddleware_PublicApp_AuthenticatedUserInContext verifies that a public app
// request with a valid session cookie results in the user being propagated into
// the context seen by the downstream handler.
func TestMiddleware_PublicApp_AuthenticatedUserInContext(t *testing.T) {
	store := makeStore(t)
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "h", Role: "admin"})
	store.CreateUser(db.CreateUserParams{Username: "viewer", PasswordHash: "h", Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")
	viewer, _ := store.GetUserByUsername("viewer")
	store.CreateApp(db.CreateAppParams{Slug: "pub", Name: "Public", OwnerID: owner.ID})
	store.SetAppAccess("pub", "public")

	token, _ := auth.IssueJWT(viewer.ID, "viewer", "developer", "test-secret")

	mw := access.Middleware(store, "test-secret", nil, nil)

	var gotUser *auth.ContextUser
	capture := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser = auth.UserFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	handler := mw(capture)

	req := httptest.NewRequest("GET", "/app/pub/", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if gotUser == nil {
		t.Fatal("public app + authenticated user: auth.UserFromContext returned nil — user must be propagated into context")
	}
	if gotUser.Username != "viewer" {
		t.Errorf("expected Username %q, got %q", "viewer", gotUser.Username)
	}
}

// TestMiddleware_PublicApp_AnonymousNilUser verifies that a public app request
// with no session cookie still passes through (200) and leaves the context user nil.
func TestMiddleware_PublicApp_AnonymousNilUser(t *testing.T) {
	store := makeStore(t)
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "h", Role: "admin"})
	owner, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "pub", Name: "Public", OwnerID: owner.ID})
	store.SetAppAccess("pub", "public")

	mw := access.Middleware(store, "test-secret", nil, nil)

	var gotUser *auth.ContextUser
	capture := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser = auth.UserFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	handler := mw(capture)

	req := httptest.NewRequest("GET", "/app/pub/", nil)
	// No cookie — anonymous request.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("anonymous public app: expected 200, got %d", rec.Code)
	}
	if gotUser != nil {
		t.Errorf("anonymous request: expected nil context user, got %+v", gotUser)
	}
}
