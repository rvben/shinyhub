package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

type fakeAppLaunchStore struct {
	createdHash  string
	createdUser  int64
	createdSlug  string
	user         *auth.ContextUser
	consumed     bool
	activatedID  string
	activatedJTI string
	activateErr  error
	abortedID    string
}

func (f *fakeAppLaunchStore) CreateAppLaunchCode(hash string, userID int64, slug string) error {
	f.createdHash, f.createdUser, f.createdSlug = hash, userID, slug
	return nil
}

func (f *fakeAppLaunchStore) ConsumeAppLaunchCode(hash, slug string) (*auth.ContextUser, error) {
	if f.consumed || hash != f.createdHash || slug != f.createdSlug {
		return nil, errors.New("invalid launch")
	}
	f.consumed = true
	return f.user, nil
}

func (f *fakeAppLaunchStore) ActivateSupportSession(id, jti string, _ time.Time) error {
	f.activatedID, f.activatedJTI = id, jti
	return f.activateErr
}

func (f *fakeAppLaunchStore) AbortSupportSession(id, _ string) error {
	f.abortedID = id
	return nil
}

func TestAppOriginLaunchExchangesOneTimeCodeForHostOnlySession(t *testing.T) {
	appOrigin, _ := url.Parse("https://apps.example.com")
	user := &auth.ContextUser{ID: 42, Username: "alice", Role: "developer"}
	store := &fakeAppLaunchStore{user: user}
	control := appOriginRedirectHandler(store, appOrigin)
	proxyHits := 0
	app := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxyHits++
		w.WriteHeader(http.StatusNoContent)
	})
	handler := appOriginDispatch(appOrigin, nil, store, "01234567890123456789012345678901", control, app)

	controlReq := httptest.NewRequest(http.MethodGet, "https://hub.example.com/app/sales/?tab=one", nil)
	controlReq = controlReq.WithContext(auth.WithUser(controlReq.Context(), user))
	controlRec := httptest.NewRecorder()
	handler.ServeHTTP(controlRec, controlReq)
	if controlRec.Code != http.StatusSeeOther {
		t.Fatalf("control status = %d, want 303", controlRec.Code)
	}
	location, err := url.Parse(controlRec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	rawCode := location.Query().Get(appLaunchQueryParam)
	if location.Host != appOrigin.Host || rawCode == "" || location.Query().Get("tab") != "one" {
		t.Fatalf("unexpected launch redirect: %s", location)
	}
	sum := sha256.Sum256([]byte(rawCode))
	if store.createdHash != hex.EncodeToString(sum[:]) || store.createdUser != user.ID || store.createdSlug != "sales" {
		t.Fatalf("launch was not correctly hashed and bound: %#v", store)
	}

	appReq := httptest.NewRequest(http.MethodGet, location.String(), nil)
	appRec := httptest.NewRecorder()
	handler.ServeHTTP(appRec, appReq)
	if appRec.Code != http.StatusSeeOther {
		t.Fatalf("exchange status = %d, want 303", appRec.Code)
	}
	if got := appRec.Header().Get("Location"); got != "/app/sales/?tab=one" {
		t.Fatalf("clean redirect = %q", got)
	}
	var session *http.Cookie
	for _, cookie := range appRec.Result().Cookies() {
		if cookie.Name == auth.SessionCookieName {
			session = cookie
		}
	}
	if session == nil || !session.HttpOnly || !session.Secure || session.Domain != "" {
		t.Fatalf("unexpected app session cookie: %#v", session)
	}
	if proxyHits != 0 {
		t.Fatalf("launch exchange reached proxy %d times", proxyHits)
	}

	replayRec := httptest.NewRecorder()
	handler.ServeHTTP(replayRec, appReq)
	if replayRec.Code != http.StatusUnauthorized {
		t.Fatalf("replay status = %d, want 401", replayRec.Code)
	}
}

func TestAppOriginBoundaryHidesControlPlaneRoutes(t *testing.T) {
	appOrigin, _ := url.Parse("https://apps.example.com")
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	handler := appOriginBoundary(next, appOrigin, nil)

	for _, path := range []string{"/api/users", "/static/app.js", "/internal/fargate-bundle/x", "/internal/runtime-bundle/x", "/"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "https://apps.example.com"+path, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s status = %d, want 404", path, rec.Code)
		}
	}
	for _, path := range []string{"/app/sales/", "/healthz", "/readyz", "/favicon.ico"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "https://apps.example.com"+path, nil))
		if rec.Code != http.StatusNoContent {
			t.Errorf("%s status = %d, want 204", path, rec.Code)
		}
	}
}

func TestAppOriginLaunchMintsAppScopedSupportCookie(t *testing.T) {
	raw := "single-use-support-code"
	sum := sha256.Sum256([]byte(raw))
	store := &fakeAppLaunchStore{
		createdHash: hex.EncodeToString(sum[:]), createdSlug: "sales",
		user: &auth.ContextUser{ID: 42, Username: "alice", Role: "viewer",
			SupportSession: &auth.SupportSessionContext{
				ID: "support-id", ActorID: 7, ActorUsername: "admin", AppID: 99, AppSlug: "sales",
				ExpiresAt: time.Now().Add(15 * time.Minute),
			}},
	}
	req := httptest.NewRequest(http.MethodGet, "https://apps.example.com/app/sales/?__shinyhub_launch="+raw, nil)
	rec := httptest.NewRecorder()
	consumeAppLaunch(rec, req, store, "01234567890123456789012345678901", nil, raw)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var support, guard *http.Cookie
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == auth.SessionCookieName {
			if cookie.MaxAge >= 0 {
				t.Fatal("support launch must clear, not mint, the ordinary app-origin session cookie")
			}
		}
		if cookie.Name == auth.SupportSessionCookieName {
			support = cookie
		}
		if cookie.Name == auth.SupportSessionGuardCookieName {
			guard = cookie
		}
	}
	if support == nil || support.Path != "/app/sales/" || !support.HttpOnly || store.activatedID != "support-id" || store.activatedJTI == "" {
		t.Fatalf("support cookie=%+v activation=%q/%q", support, store.activatedID, store.activatedJTI)
	}
	if guard == nil || guard.Path != "/" || !guard.HttpOnly || guard.Value != "support-id" {
		t.Fatalf("support guard=%+v", guard)
	}
}

func TestAppOriginLaunchAbortsAmbiguousActivationFailureBeforeCookies(t *testing.T) {
	raw := "ambiguous-activation-code"
	sum := sha256.Sum256([]byte(raw))
	store := &fakeAppLaunchStore{
		createdHash: hex.EncodeToString(sum[:]), createdSlug: "sales", activateErr: errors.New("connection reset after commit"),
		user: &auth.ContextUser{ID: 42, Username: "alice", Role: "viewer", SupportSession: &auth.SupportSessionContext{
			ID: "support-ambiguous", ActorID: 7, ActorUsername: "admin", AppID: 99, AppSlug: "sales", ExpiresAt: time.Now().Add(15 * time.Minute)}},
	}
	req := httptest.NewRequest(http.MethodGet, "https://apps.example.com/app/sales/?__shinyhub_launch="+raw, nil)
	rec := httptest.NewRecorder()
	consumeAppLaunch(rec, req, store, "01234567890123456789012345678901", nil, raw)
	if rec.Code != http.StatusInternalServerError || store.abortedID != "support-ambiguous" {
		t.Fatalf("status=%d aborted=%q", rec.Code, store.abortedID)
	}
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == auth.SupportSessionCookieName && cookie.Value != "" {
			t.Fatalf("activation failure emitted support cookie: %+v", cookie)
		}
	}
}

func TestSameHostUsesBrowserCanonicalization(t *testing.T) {
	if !sameHost("bücher.example", "xn--bcher-kva.example:443") {
		t.Fatal("Unicode and punycode spellings must identify the same virtual host")
	}
	if sameHost("127.1", "127.0.0.1") {
		t.Fatal("ambiguous numeric IPv4 spelling must never match a configured host")
	}
}

func TestSupportStopNeverClearsRootGuardEarly(t *testing.T) {
	store := dbtest.New(t)
	for _, user := range []db.CreateUserParams{
		{Username: "admin", PasswordHash: "hash", Role: "admin"},
		{Username: "alice", PasswordHash: "hash", Role: "viewer"},
	} {
		if err := store.CreateUser(user); err != nil {
			t.Fatal(err)
		}
	}
	admin, _ := store.GetUserByUsername("admin")
	alice, _ := store.GetUserByUsername("alice")
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "sales", Name: "Sales", OwnerID: admin.ID, Access: "public"}); err != nil {
		t.Fatal(err)
	}
	app, _ := store.GetAppBySlug("sales")
	expires := time.Now().UTC().Add(15 * time.Minute)
	if err := store.CreateSupportSession(db.CreateSupportSessionParams{
		ID: "support-id", ActorUserID: admin.ID, ActorUsername: admin.Username, ActorTokenEpoch: admin.TokenEpoch,
		SubjectUserID: alice.ID, SubjectUsername: alice.Username, SubjectRole: alice.Role, SubjectTokenEpoch: alice.TokenEpoch,
		AppID: app.ID, AppSlug: app.Slug, Reason: "Investigating SUP-4001", LaunchCodeHash: "launch-hash", ExpiresAt: expires,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConsumeAppLaunchCode("launch-hash", "sales"); err != nil {
		t.Fatal(err)
	}
	identity := &auth.ContextUser{ID: alice.ID, Username: alice.Username, Role: alice.Role, TokenEpoch: alice.TokenEpoch,
		SupportSession: &auth.SupportSessionContext{ID: "support-id", ActorID: admin.ID, ActorUsername: admin.Username,
			ActorTokenEpoch: admin.TokenEpoch, AppID: app.ID, AppSlug: "sales", ExpiresAt: expires}}
	token, info, err := auth.IssueSessionTokenWithInfo(identity, "01234567890123456789012345678901")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ActivateSupportSession("support-id", info.JTI, info.ExpiresAt); err != nil {
		t.Fatal(err)
	}
	if err := store.BumpTokenEpoch(alice.ID); err != nil {
		t.Fatal(err)
	}
	handler := supportSessionStopHandler(store, "01234567890123456789012345678901", "https://hub.example.com/users", nil)

	for _, slug := range []string{"other", "sales"} {
		req := httptest.NewRequest(http.MethodPost, "https://apps.example.com/app/"+slug+"/.shinyhub/support-session/stop", nil)
		req.SetPathValue("slug", slug)
		req.AddCookie(&http.Cookie{Name: auth.SupportSessionCookieName, Value: token})
		req.AddCookie(&http.Cookie{Name: auth.SupportSessionGuardCookieName, Value: "support-id"})
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		for _, cookie := range rec.Result().Cookies() {
			if cookie.Name == auth.SupportSessionGuardCookieName && cookie.MaxAge < 0 {
				t.Fatalf("%s stop cleared the root guard before its deadline: %+v", slug, cookie)
			}
		}
	}
	refreshedAlice, _ := store.GetUserByUsername("alice")
	if err := store.CreateSupportSession(db.CreateSupportSessionParams{
		ID: "replacement", ActorUserID: admin.ID, ActorUsername: admin.Username, ActorTokenEpoch: admin.TokenEpoch,
		SubjectUserID: refreshedAlice.ID, SubjectUsername: refreshedAlice.Username, SubjectRole: refreshedAlice.Role,
		SubjectTokenEpoch: refreshedAlice.TokenEpoch, AppID: app.ID, AppSlug: app.Slug,
		Reason: "Investigating SUP-4002", LaunchCodeHash: "replacement-hash", ExpiresAt: time.Now().Add(15 * time.Minute),
	}); err != nil {
		t.Fatalf("identity drift made the prior support session impossible to replace: %v", err)
	}
}
