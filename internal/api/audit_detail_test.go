package api_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

// auditDetail decodes an audit event's Detail column as the JSON object the
// format contract promises. Asserting on decoded keys rather than on substrings
// is the point: a substring check for a username also passes when the username
// only happens to appear inside some other field, and passes on a row that is
// not parseable at all.
func auditDetail(t *testing.T, raw string) map[string]any {
	t.Helper()
	var fields map[string]any
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		t.Fatalf("audit Detail is not a JSON object: %v; got %q", err, raw)
	}
	return fields
}

// latestAuditDetail returns the decoded Detail of the newest event for action.
func latestAuditDetail(t *testing.T, store *db.Store, action string) map[string]any {
	t.Helper()
	events, err := store.ListAuditEvents(action, 10, 0)
	if err != nil {
		t.Fatalf("list %s audit events: %v", action, err)
	}
	if len(events) == 0 {
		t.Fatalf("no %s audit event recorded", action)
	}
	return auditDetail(t, events[0].Detail)
}

// auditDetailValues collects one string field across every event for an action,
// so an assertion does not depend on which order two events landed in.
func auditDetailValues(t *testing.T, store *db.Store, action, key string) []string {
	t.Helper()
	events, err := store.ListAuditEvents(action, 20, 0)
	if err != nil {
		t.Fatalf("list %s audit events: %v", action, err)
	}
	values := make([]string, 0, len(events))
	for _, e := range events {
		v, _ := auditDetail(t, e.Detail)[key].(string)
		values = append(values, v)
	}
	return values
}

func loginReq(t *testing.T, path, username, password string) *http.Request {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"username": username, "password": password})
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

// TestDeleteUser_AuditNamesTheDeletedUser pins the one detail a deletion trail
// cannot reconstruct afterwards. delete_user records the row id as its
// resource_id, and by the time anyone reads the trail that row is gone, so the
// username has to be captured at the moment of deletion or it is lost for good.
func TestDeleteUser_AuditNamesTheDeletedUser(t *testing.T) {
	srv, store := newTestServer(t)
	token, _ := seedUserAndJWT(t, store, "admin", "admin")
	if err := store.CreateUser(db.CreateUserParams{Username: "bob", PasswordHash: "x", Role: "developer"}); err != nil {
		t.Fatal(err)
	}
	bob, _ := store.GetUserByUsername("bob")

	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, adminReq(t, http.MethodDelete, fmt.Sprintf("/api/users/%d", bob.ID), nil, token))
	if rec.Code != http.StatusOK && rec.Code != http.StatusNoContent {
		t.Fatalf("delete user: want 200/204, got %d: %s", rec.Code, rec.Body.String())
	}

	// The users row is the only other place the id could be resolved to a name.
	// Proving it is gone is what makes the assertion below load-bearing rather
	// than a duplicate of information still available elsewhere.
	if _, err := store.GetUserByID(bob.ID); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("user row must be gone after delete; GetUserByID err = %v", err)
	}

	d := latestAuditDetail(t, store, "delete_user")
	if d["username"] != "bob" {
		t.Errorf("delete_user audit Detail must name the deleted user; username = %v, want bob (detail: %v)", d["username"], d)
	}
	if d["role"] != "developer" {
		t.Errorf("delete_user audit Detail must record the role that was removed; role = %v, want developer (detail: %v)", d["role"], d)
	}
}

// TestDeleteToken_AuditNamesTheTokenAndForeignOwner pins both halves of the
// delete_token detail: the token's name always, and the owner only when the
// caller is revoking someone else's credential. Recording the owner
// unconditionally would be noise; omitting it on the admin path would leave the
// trail saying who acted and not whose access ended.
func TestDeleteToken_AuditNamesTheTokenAndForeignOwner(t *testing.T) {
	srv, store := newTestServer(t)
	adminToken, adminID := seedUserAndJWT(t, store, "admin", "admin")
	bobToken, bobID := seedUserAndJWT(t, store, "bob", "developer")

	mkToken := func(owner int64, jwt, name string) int64 {
		t.Helper()
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, adminReq(t, http.MethodPost, "/api/tokens", map[string]string{"name": name}, jwt))
		if rec.Code != http.StatusCreated {
			t.Fatalf("create token %s: want 201, got %d: %s", name, rec.Code, rec.Body.String())
		}
		keys, err := store.ListAPIKeys(owner)
		if err != nil {
			t.Fatalf("list keys: %v", err)
		}
		for _, k := range keys {
			if k.Name == name {
				return k.ID
			}
		}
		t.Fatalf("token %q not found for user %d", name, owner)
		return 0
	}

	bobsTokenID := mkToken(bobID, bobToken, "bob-ci")
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, adminReq(t, http.MethodDelete, fmt.Sprintf("/api/tokens/%d", bobsTokenID), nil, adminToken))
	if rec.Code != http.StatusOK && rec.Code != http.StatusNoContent {
		t.Fatalf("admin delete of bob's token: want 200/204, got %d: %s", rec.Code, rec.Body.String())
	}
	d := latestAuditDetail(t, store, "delete_token")
	if d["token_name"] != "bob-ci" {
		t.Errorf("delete_token audit Detail must name the token; token_name = %v, want bob-ci (detail: %v)", d["token_name"], d)
	}
	if owner, ok := d["owner_user_id"].(float64); !ok || int64(owner) != bobID {
		t.Errorf("revoking another user's token must record its owner; owner_user_id = %v, want %d (detail: %v)", d["owner_user_id"], bobID, d)
	}

	// Second bound: on a self-revocation the owner adds nothing the row does not
	// already carry, so it must be absent rather than always present.
	ownTokenID := mkToken(adminID, adminToken, "admin-ci")
	rec = httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, adminReq(t, http.MethodDelete, fmt.Sprintf("/api/tokens/%d", ownTokenID), nil, adminToken))
	if rec.Code != http.StatusOK && rec.Code != http.StatusNoContent {
		t.Fatalf("admin delete of own token: want 200/204, got %d: %s", rec.Code, rec.Body.String())
	}
	d = latestAuditDetail(t, store, "delete_token")
	if d["token_name"] != "admin-ci" {
		t.Errorf("self-revocation must still name the token; token_name = %v, want admin-ci (detail: %v)", d["token_name"], d)
	}
	if _, present := d["owner_user_id"]; present {
		t.Errorf("self-revocation must not record owner_user_id; detail: %v", d)
	}
}

// TestLoginFailed_AuditDistinguishesReasons pins that the audit trail separates
// a login attempt against a username that does not exist from one against a
// real account with the wrong password, while the HTTP responses stay
// byte-identical so the endpoint still refuses to confirm which usernames are
// real.
func TestLoginFailed_AuditDistinguishesReasons(t *testing.T) {
	srv, store := newTestServer(t)
	seedUserAndJWT(t, store, "admin", "admin")

	ghost := httptest.NewRecorder()
	srv.Router().ServeHTTP(ghost, loginReq(t, "/api/auth/login", "ghost", "seed-password"))
	badPass := httptest.NewRecorder()
	srv.Router().ServeHTTP(badPass, loginReq(t, "/api/auth/login", "admin", "not-the-password"))

	if ghost.Code != http.StatusUnauthorized || badPass.Code != http.StatusUnauthorized {
		t.Fatalf("both attempts must fail: unknown user %d, bad password %d", ghost.Code, badPass.Code)
	}
	if ghost.Body.String() != badPass.Body.String() {
		t.Errorf("responses must not distinguish an unknown user from a bad password; got %q vs %q",
			ghost.Body.String(), badPass.Body.String())
	}

	reasons := auditDetailValues(t, store, "login_failed", "reason")
	if len(reasons) != 2 {
		t.Fatalf("want 2 login_failed events, got %d (%v)", len(reasons), reasons)
	}
	seen := map[string]bool{reasons[0]: true, reasons[1]: true}
	if !seen["unknown_user"] {
		t.Errorf("a login for a username that does not exist must record reason unknown_user; got %v", reasons)
	}
	if !seen["bad_password"] {
		t.Errorf("a login with the wrong password for a real account must record reason bad_password; got %v", reasons)
	}
}

// TestGitHubCallback_AuditRecordsProvider pins that an SSO sign-in says which
// identity provider vouched for the user, on both the account it creates and
// the session it issues. Every provider records the same "login" action against
// a username, so without this an operator cannot tell a GitHub sign-in from a
// local password sign-in, and cannot tell a JIT-provisioned account from one an
// administrator created deliberately.
func TestGitHubCallback_AuditRecordsProvider(t *testing.T) {
	gh := newFakeGitHub(t, nil,
		`{"id":701,"login":"octocat","name":"Octo Cat","email":"octocat@corp.example"}`, "")
	srv, store := e2eTestServer(t)
	srv.SetGitHubProvider(gh)

	rec := driveCallback(t, srv, store, "/api/auth/github/callback", "gh-state-audit")
	if rec.Code != http.StatusFound {
		t.Fatalf("callback: want 302, got %d (%s)", rec.Code, rec.Body.String())
	}

	created := latestAuditDetail(t, store, "create_user")
	if created["provider"] != "github" {
		t.Errorf("a JIT-provisioned account must record its provider; provider = %v, want github (detail: %v)", created["provider"], created)
	}
	if created["role"] == nil || created["role"] == "" {
		t.Errorf("create_user must record the role the account was given; detail: %v", created)
	}

	login := latestAuditDetail(t, store, "login")
	if login["provider"] != "github" {
		t.Errorf("an SSO login must record its provider; provider = %v, want github (detail: %v)", login["provider"], login)
	}
	if login["grant"] != "session_cookie" {
		t.Errorf("an SSO login issues a browser session; grant = %v, want session_cookie (detail: %v)", login["grant"], login)
	}
}

// TestGoogleCallback_AuditRecordsProvider is the Google half of the same
// contract. Two providers are what make the field worth having: one alone
// cannot show that the value actually varies with the path taken.
func TestGoogleCallback_AuditRecordsProvider(t *testing.T) {
	g := newFakeGoogle(t, nil, `{"id":"9101","email":"dana@corp.example","name":"Dana Scully"}`)
	srv, store := e2eTestServer(t)
	srv.SetGoogleProvider(g)

	rec := driveCallback(t, srv, store, "/api/auth/google/callback", "goog-state-audit")
	if rec.Code != http.StatusFound {
		t.Fatalf("callback: want 302, got %d (%s)", rec.Code, rec.Body.String())
	}

	if created := latestAuditDetail(t, store, "create_user"); created["provider"] != "google" {
		t.Errorf("a JIT-provisioned account must record its provider; provider = %v, want google (detail: %v)", created["provider"], created)
	}
	if login := latestAuditDetail(t, store, "login"); login["provider"] != "google" {
		t.Errorf("an SSO login must record its provider; provider = %v, want google (detail: %v)", login["provider"], login)
	}
}

// TestLogin_AuditRecordsGrantType pins that a successful login says which
// credential it handed out. The two endpoints issue different things - a bearer
// token a client keeps, and a browser session cookie - and an operator reading
// the trail cannot otherwise tell an API client's login from a dashboard login.
func TestLogin_AuditRecordsGrantType(t *testing.T) {
	srv, store := newTestServer(t)
	seedUserAndJWT(t, store, "admin", "admin")

	for _, path := range []string{"/api/auth/login", "/api/auth/session"} {
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, loginReq(t, path, "admin", "seed-password"))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: want 200, got %d: %s", path, rec.Code, rec.Body.String())
		}
	}

	grants := auditDetailValues(t, store, "login", "grant")
	if len(grants) != 2 {
		t.Fatalf("want 2 login events, got %d (%v)", len(grants), grants)
	}
	seen := map[string]bool{grants[0]: true, grants[1]: true}
	if !seen["bearer_token"] {
		t.Errorf("/api/auth/login must record grant bearer_token; got %v", grants)
	}
	if !seen["session_cookie"] {
		t.Errorf("/api/auth/session must record grant session_cookie; got %v", grants)
	}
}

func TestPatchUser_AuditRecordsOldAndNewRole(t *testing.T) {
	srv, store := newTestServer(t)
	token, _ := seedUserAndJWT(t, store, "admin", "admin")
	if err := store.CreateUser(db.CreateUserParams{Username: "bob", PasswordHash: "x", Role: "developer"}); err != nil {
		t.Fatal(err)
	}
	bob, _ := store.GetUserByUsername("bob")

	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, adminReq(t, http.MethodPatch, fmt.Sprintf("/api/users/%d", bob.ID), map[string]string{"role": "operator"}, token))
	if rec.Code != http.StatusOK {
		t.Fatalf("patch role: want 200, got %d: %s", rec.Code, rec.Body.String())
	}

	events, err := store.ListAuditEvents("update_user", 10, 0)
	if err != nil || len(events) == 0 {
		t.Fatalf("no update_user audit event: %v (n=%d)", err, len(events))
	}
	d := events[0].Detail
	if !strings.Contains(d, "developer") || !strings.Contains(d, "operator") {
		t.Errorf("update_user audit Detail must record old (developer) and new (operator) role; got %q", d)
	}
}

func TestSetAppAccess_AuditRecordsFromAndTo(t *testing.T) {
	srv, store := newTestServer(t)
	token, adminID := seedUserAndJWT(t, store, "admin", "admin")
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "dash", Name: "Dash", OwnerID: adminID}); err != nil {
		t.Fatal(err)
	}
	// Establish a known starting visibility (the create-handler defaults to this
	// in production; a direct store.CreateApp leaves it empty).
	if err := store.SetAppAccess("dash", "private"); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, adminReq(t, http.MethodPatch, "/api/apps/dash/access", map[string]string{"access": "public"}, token))
	if rec.Code != http.StatusOK {
		t.Fatalf("set access: want 200, got %d: %s", rec.Code, rec.Body.String())
	}

	events, err := store.ListAuditEvents("set_access", 10, 0)
	if err != nil || len(events) == 0 {
		t.Fatalf("no set_access audit event: %v (n=%d)", err, len(events))
	}
	d := events[0].Detail
	if !strings.Contains(d, "private") || !strings.Contains(d, "public") {
		t.Errorf("set_access audit Detail must record from (private) and to (public); got %q", d)
	}
}
