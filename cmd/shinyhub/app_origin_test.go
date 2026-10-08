package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/access"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
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
	session      *auth.TokenInfo
}

func (f *fakeAppLaunchStore) CreateAppLaunchCodeWithSession(hash string, userID int64, slug string, session *auth.TokenInfo, _ int64) error {
	f.session = session
	return f.CreateAppLaunchCode(hash, userID, slug)
}

func (f *fakeAppLaunchStore) ConsumeAppLaunchCodeWithSession(hash, slug string) (*auth.ContextUser, *auth.TokenInfo, error) {
	u, err := f.ConsumeAppLaunchCode(hash, slug)
	return u, f.session, err
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

func TestAppOriginCanonicalizesKnownRootsOnBothHosts(t *testing.T) {
	store := dbtest.New(t)
	if err := store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "h", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	owner, err := store.GetUserByUsername("owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "Demo", OwnerID: owner.ID, Access: "public"}); err != nil {
		t.Fatal(err)
	}
	origin, _ := url.Parse("https://apps.example.com")
	app := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/app/demo/" {
			t.Errorf("app handler received noncanonical root %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	wrap := access.Middleware(store, "test-secret", nil, store.LookupContextUser)
	handler := appOriginDispatch(origin, nil, store, "test-secret",
		wrap(appOriginRedirectHandler(store, origin, nil)), wrap(app))
	for _, host := range []string{"hub.example.com", "apps.example.com"} {
		t.Run(host, func(t *testing.T) {
			for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost} {
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, httptest.NewRequest(method, "https://"+host+"/app/demo?_inputs_&x=%2f", nil))
				if rec.Code != http.StatusPermanentRedirect || rec.Header().Get("Location") != "/app/demo/?_inputs_&x=%2f" {
					t.Fatalf("%s bare root: status=%d Location=%q", method, rec.Code, rec.Header().Get("Location"))
				}
			}
		})
	}
	// Following canonicalization on the control host still switches origins,
	// and the canonical app-origin landing does not redirect again.
	control := httptest.NewRecorder()
	handler.ServeHTTP(control, httptest.NewRequest(http.MethodGet, "https://hub.example.com/app/demo/?tab=one", nil))
	if control.Code != http.StatusSeeOther || control.Header().Get("Location") != "https://apps.example.com/app/demo/?tab=one" {
		t.Fatalf("control landing: status=%d Location=%q", control.Code, control.Header().Get("Location"))
	}
	landing := httptest.NewRecorder()
	handler.ServeHTTP(landing, httptest.NewRequest(http.MethodGet, control.Header().Get("Location"), nil))
	if landing.Code != http.StatusNoContent || landing.Header().Get("Location") != "" {
		t.Fatalf("app landing: status=%d Location=%q", landing.Code, landing.Header().Get("Location"))
	}
}

func TestAppOriginLaunchExchangesOneTimeCodeForHostOnlySession(t *testing.T) {
	appOrigin, _ := url.Parse("https://apps.example.com")
	user := &auth.ContextUser{ID: 42, Username: "alice", Role: "developer"}
	store := &fakeAppLaunchStore{user: user}
	control := appOriginRedirectHandler(store, appOrigin, nil)
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

func TestAppOriginLaunchPreservesConfiguredBrowserLifetime(t *testing.T) {
	ttl, maxAge := 10*time.Minute, 2*time.Hour
	original := time.Now().Add(-110 * time.Minute).Truncate(time.Second)
	appOrigin, _ := url.Parse("https://apps.example.com")
	u := &auth.ContextUser{ID: 42, Username: "browser", Role: "viewer"}
	store := &fakeAppLaunchStore{user: u}
	handler := appOriginDispatch(appOrigin, nil, store, "secret", appOriginRedirectHandler(store, appOrigin, nil), http.NotFoundHandler(), config.AuthConfig{SessionTTL: &ttl, SessionMaxAge: &maxAge})
	req := httptest.NewRequest("GET", "https://hub.example.com/app/sales/", nil)
	ctx := auth.WithUser(req.Context(), u)
	ctx = auth.WithTokenInfo(ctx, &auth.TokenInfo{JTI: "original-family", AuthTime: original})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req.WithContext(ctx))
	if rec.Code != 303 {
		t.Fatalf("launch: %d %s", rec.Code, rec.Body.String())
	}
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, httptest.NewRequest("GET", rec.Header().Get("Location"), nil))
	if rec2.Code != 303 {
		t.Fatalf("exchange: %d %s", rec2.Code, rec2.Body.String())
	}
	for _, cookie := range rec2.Result().Cookies() {
		if cookie.Name != auth.SessionCookieName {
			continue
		}
		claims, err := auth.ValidateJWT(cookie.Value, "secret", nil)
		if err != nil {
			t.Fatal(err)
		}
		if claims.ID != "original-family" || !claims.AuthTime.Time.Equal(original) || !claims.ExpiresAt.Time.Equal(original.Add(maxAge)) || !cookie.Expires.Equal(claims.ExpiresAt.Time) {
			t.Fatalf("exchange reset login or expiry: %+v cookie=%+v", claims, cookie)
		}
		return
	}
	t.Fatal("missing browser session cookie")
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

func TestAppOriginTrustWarningFiresOnlyWhenAppOriginUnset(t *testing.T) {
	if msg := appOriginTrustWarning(""); msg == "" {
		t.Fatal("an empty app_origin must produce a same-origin trust warning")
	} else if !strings.Contains(msg, "app_origin") || !strings.Contains(msg, "same-origin") {
		t.Errorf("warning must name app_origin and the same-origin risk: %q", msg)
	}
	if msg := appOriginTrustWarning("https://apps.example.com"); msg != "" {
		t.Errorf("a configured app_origin must not warn, got %q", msg)
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

func TestTrustedAppSupportRejectsOrdinaryLaunchWithoutChangingAdminCookie(t *testing.T) {
	raw := "stale-ordinary-launch"
	sum := sha256.Sum256([]byte(raw))
	store := &fakeAppLaunchStore{createdHash: hex.EncodeToString(sum[:]), createdSlug: "sales", user: &auth.ContextUser{ID: 42, Username: "viewer", Role: "viewer"}}
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("ordinary launch reached app") })
	handler := trustedAppSupportDispatch(next, store, "test-secret", nil)
	req := httptest.NewRequest("GET", "https://hub.example.com/app/sales/?__shinyhub_launch="+raw, nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "admin-session"})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || len(rec.Result().Cookies()) != 0 {
		t.Fatalf("ordinary launch status=%d cookies=%v", rec.Code, rec.Result().Cookies())
	}
}

func TestForwardLaunchPreservesFamilyAndAuthenticationMode(t *testing.T) {
	store := dbtest.New(t)
	if err := store.CreateUser(db.CreateUserParams{Username: "alice", PasswordHash: "unused", Role: "viewer"}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateUser(db.CreateUserParams{Username: "bob", PasswordHash: "unused", Role: "viewer"}); err != nil {
		t.Fatal(err)
	}
	alice, _ := store.GetUserByUsername("alice")
	bob, _ := store.GetUserByUsername("bob")
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "demo", OwnerID: alice.ID}); err != nil {
		t.Fatal(err)
	}
	_, network, _ := net.ParseCIDR("127.0.0.0/8")
	trusted := []*net.IPNet{network}
	origin, _ := url.Parse("https://apps.example")
	policy := config.AuthConfig{Secret: "secret", ForwardAuth: config.ForwardAuthConfig{Enabled: true}}
	for _, tc := range []struct {
		name               string
		family, suppressed bool
		upstream           string
		wantClear, wantSet bool
	}{{"native account switch", false, true, "alice", false, true}, {"explicit reconnect", true, false, "alice", true, false}, {"mismatched forwarded launch", true, false, "bob", false, false}} {
		t.Run(tc.name, func(t *testing.T) {
			code := tc.name
			sum := sha256.Sum256([]byte(code))
			hash := hex.EncodeToString(sum[:])
			ti := &auth.TokenInfo{JTI: tc.name, AuthTime: time.Now().Add(-2 * time.Hour).Truncate(time.Second), ForwardAuthFamily: tc.family, ForwardAuthSuppressed: tc.suppressed}
			user := alice
			if !tc.family {
				user = bob
			}
			if err := store.CreateAppLaunchCodeWithSession(hash, user.ID, "demo", ti, user.TokenEpoch); err != nil {
				t.Fatal(err)
			}
			hits := 0
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++; w.WriteHeader(200) })
			h := appOriginLaunchDispatch(next, origin, store, "secret", trusted, policy)
			h = auth.ForwardAuthMiddleware(store, auth.ForwardAuthConfig{Enabled: true, UserHeader: "Remote-User", DefaultRole: "viewer", SessionSecret: "secret", SessionMaxAge: 12 * time.Hour, Revoked: store.IsTokenRevoked, AppOriginHost: origin.Host, DashboardURL: "https://hub.example"}, trusted)(h)
			r := httptest.NewRequest("GET", origin.String()+"/app/demo/.shinyhub/nav.json?__shinyhub_launch="+url.QueryEscape(code), nil)
			r.RemoteAddr = "127.0.0.1:4"
			r.Header.Set("Remote-User", tc.upstream)
			r.AddCookie(&http.Cookie{Name: auth.SecureForwardAuthSignedOutCookie, Value: "1"})
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 303 || hits != 0 {
				t.Fatalf("launch bypassed consumption: %d hits=%d", w.Code, hits)
			}
			set, clear := false, false
			trackingCount := 0
			for _, c := range w.Result().Cookies() {
				switch c.Name {
				case auth.SecureForwardAuthSignedOutCookie:
					set = set || c.MaxAge >= 0
					clear = clear || c.MaxAge < 0
				case auth.SessionCookieName:
					claims, err := auth.ValidateJWT(c.Value, "secret", nil)
					if err != nil || claims.ID != ti.JTI || claims.UserID != user.ID {
						t.Fatalf("unbound app JWT %+v %v", claims, err)
					}
				case auth.SecureForwardAuthSessionCookie:
					trackingCount++
					claims, err := auth.ValidateForwardAuthSession(c.Value, "secret")
					if err != nil || claims.ID != ti.JTI {
						t.Fatalf("unbound tracking %+v %v", claims, err)
					}
				}
			}
			if set != tc.wantSet || clear != tc.wantClear {
				t.Fatalf("mode set=%v clear=%v", set, clear)
			}
			if tc.family && trackingCount != 1 {
				t.Fatalf("stray or missing forward family cookies: %d", trackingCount)
			}
		})
	}
}

func TestForwardLaunchRecordsModeFromRealControlRequest(t *testing.T) {
	store := dbtest.New(t)
	for _, name := range []string{"alice", "bob"} {
		if err := store.CreateUser(db.CreateUserParams{Username: name, PasswordHash: "unused", Role: "viewer"}); err != nil {
			t.Fatal(err)
		}
	}
	alice, _ := store.GetUserByUsername("alice")
	bob, _ := store.GetUserByUsername("bob")
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "demo", OwnerID: alice.ID, Access: "public"}); err != nil {
		t.Fatal(err)
	}
	_, network, _ := net.ParseCIDR("127.0.0.0/8")
	trusted := []*net.IPNet{network}
	origin, _ := url.Parse("https://apps.example")
	cfg := auth.ForwardAuthConfig{Enabled: true, UserHeader: "Remote-User", SessionSecret: "secret", SessionMaxAge: 12 * time.Hour, Revoked: store.IsTokenRevoked, AppOriginHost: origin.Host, DashboardURL: "https://hub.example"}
	policy := config.AuthConfig{Secret: "secret", ForwardAuth: config.ForwardAuthConfig{Enabled: true}}
	for _, suppressed := range []bool{true, false} {
		t.Run(fmt.Sprint(suppressed), func(t *testing.T) {
			user := alice
			var cookie *http.Cookie
			var ti *auth.TokenInfo
			var err error
			if suppressed {
				user = bob
				token, info, issueErr := auth.IssueBrowserSession(user.ContextUser(), "secret", time.Now(), time.Hour, 12*time.Hour)
				ti, err = info, issueErr
				cookie = &http.Cookie{Name: auth.SessionCookieName, Value: token}
			} else {
				token, info, issueErr := auth.IssueForwardAuthSession(user.ContextUser(), "secret", time.Now(), 12*time.Hour, "")
				ti, err = info, issueErr
				cookie = &http.Cookie{Name: auth.SecureForwardAuthSessionCookie, Value: token}
			}
			if err != nil {
				t.Fatal(err)
			}
			control := access.Middleware(store, "secret", store.IsTokenRevoked, store.LookupContextUser)(appOriginRedirectHandler(store, origin, trusted))
			h := auth.ForwardAuthMiddleware(store, cfg, trusted)(control)
			r := httptest.NewRequest("GET", "https://hub.example/app/demo/", nil)
			r.RemoteAddr = "127.0.0.1:4"
			r.Header.Set("Remote-User", "alice")
			r.AddCookie(cookie)
			if suppressed {
				r.AddCookie(&http.Cookie{Name: auth.SecureForwardAuthSignedOutCookie, Value: "1"})
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 303 {
				t.Fatalf("control: %d %s", w.Code, w.Body.String())
			}
			target, _ := url.Parse(w.Header().Get("Location"))
			raw := target.Query().Get(appLaunchQueryParam)
			sum := sha256.Sum256([]byte(raw))
			hash := hex.EncodeToString(sum[:])
			var uid int64
			var family, suppress int
			var jti string
			if err := store.DB().QueryRow("SELECT user_id,forward_auth_family,forward_auth_suppressed,session_jti FROM app_launch_codes WHERE code_hash=?", hash).Scan(&uid, &family, &suppress, &jti); err != nil {
				t.Fatal(err)
			}
			wantFamily, wantSuppress := 1, 0
			if suppressed {
				wantFamily, wantSuppress = 0, 1
			}
			if uid != user.ID || family != wantFamily || suppress != wantSuppress || jti != ti.JTI {
				t.Fatalf("recorded uid=%d family=%d suppressed=%d jti=%s", uid, family, suppress, jti)
			}
			consume := auth.ForwardAuthMiddleware(store, cfg, trusted)(appOriginLaunchDispatch(http.NotFoundHandler(), origin, store, "secret", trusted, policy))
			launch := httptest.NewRequest("GET", target.String(), nil)
			launch.RemoteAddr = "127.0.0.1:4"
			launch.Header.Set("Remote-User", "alice")
			launch.AddCookie(&http.Cookie{Name: auth.SecureForwardAuthSignedOutCookie, Value: "1"})
			out := httptest.NewRecorder()
			consume.ServeHTTP(out, launch)
			if out.Code != 303 {
				t.Fatalf("consume: %d %s", out.Code, out.Body.String())
			}
			sawMarker := false
			for _, c := range out.Result().Cookies() {
				if c.Name == auth.SecureForwardAuthSignedOutCookie {
					sawMarker = true
					if (c.MaxAge >= 0) != suppressed {
						t.Fatalf("wrong marker %+v", c)
					}
				}
			}
			if !sawMarker {
				t.Fatal("missing mode change")
			}
		})
	}
}

func TestForwardAppOriginDirectLinkAndIdentitySwitch(t *testing.T) {
	store := dbtest.New(t)
	for _, name := range []string{"alice", "bob"} {
		if err := store.CreateUser(db.CreateUserParams{Username: name, PasswordHash: "unused", Role: "viewer"}); err != nil {
			t.Fatal(err)
		}
	}
	alice, _ := store.GetUserByUsername("alice")
	bob, _ := store.GetUserByUsername("bob")
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "demo", OwnerID: bob.ID, Access: "private"}); err != nil {
		t.Fatal(err)
	}
	_, network, _ := net.ParseCIDR("127.0.0.0/8")
	trusted := []*net.IPNet{network}
	origin, _ := url.Parse("https://apps.example.com")
	cfg := auth.ForwardAuthConfig{Enabled: true, UserHeader: "Remote-User", SessionSecret: "secret", Revoked: store.IsTokenRevoked, AppOriginHost: origin.Host, DashboardURL: "https://hub.example.com"}
	policy := config.AuthConfig{Secret: "secret", ForwardAuth: config.ForwardAuthConfig{Enabled: true}}
	hits := 0
	app := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if u := auth.UserFromContext(r.Context()); u != nil && u.ID == alice.ID {
			t.Fatal("edge Bob authenticated as Alice")
		}
		w.WriteHeader(200)
	})
	control := access.Middleware(store, "secret", store.IsTokenRevoked, store.LookupContextUser)(appOriginRedirectHandler(store, origin, trusted))
	h := auth.ForwardAuthMiddleware(store, cfg, trusted)(appOriginLaunchDispatch(appOriginDispatch(origin, trusted, store, "secret", control, access.Middleware(store, "secret", store.IsTokenRevoked, store.LookupContextUser)(app), policy), origin, store, "secret", trusted, policy))
	native, _, _ := auth.IssueBrowserSession(alice.ContextUser(), "secret", time.Now(), time.Hour, 12*time.Hour)
	tracking, _, _ := auth.IssueForwardAuthSession(alice.ContextUser(), "secret", time.Now(), 12*time.Hour, "")
	request := func(target string, page bool, cookies []*http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", target, nil)
		r.RemoteAddr = "127.0.0.1:4"
		r.Header.Set("Remote-User", "bob")
		if page {
			r.Header.Set("Accept", "text/html")
		}
		for _, c := range cookies {
			r.AddCookie(c)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	old := []*http.Cookie{{Name: auth.SessionCookieName, Value: native}, {Name: auth.SecureForwardAuthSessionCookie, Value: tracking}}
	blocked := request("https://apps.example.com/app/demo/.shinyhub/session.json", false, old)
	if blocked.Code != 401 || hits != 0 || len(blocked.Result().Cookies()) != 0 {
		t.Fatalf("foreign fallback: %d hits=%d", blocked.Code, hits)
	}
	direct := request("https://apps.example.com/app/demo/?tab=one", true, old)
	if direct.Code != 303 || direct.Header().Get("Location") != "https://hub.example.com/app/demo/?tab=one" {
		t.Fatalf("direct link dead end: %d %s", direct.Code, direct.Header().Get("Location"))
	}
	dashboard := request(direct.Header().Get("Location"), true, nil)
	if dashboard.Code != 303 {
		t.Fatalf("control auth: %d %s", dashboard.Code, dashboard.Body.String())
	}
	launch := request(dashboard.Header().Get("Location"), true, old)
	if launch.Code != 303 {
		t.Fatalf("launch: %d %s", launch.Code, launch.Body.String())
	}
	var claims *auth.Claims
	for _, c := range launch.Result().Cookies() {
		if c.Name == auth.SessionCookieName {
			claims, _ = auth.ValidateJWT(c.Value, "secret", nil)
		}
	}
	if claims == nil || claims.UserID != bob.ID {
		t.Fatalf("wrong launched identity: %+v", claims)
	}
	if err := store.RevokeToken(claims.ID, claims.UserID, claims.ExpiresAt.Time); err != nil {
		t.Fatal(err)
	}
	revoked := request("https://apps.example.com/app/demo/", true, launch.Result().Cookies())
	if revoked.Code != 303 || !strings.HasPrefix(revoked.Header().Get("Location"), "https://hub.example.com/login?next=") {
		t.Fatalf("revoked app dead end: %d %s", revoked.Code, revoked.Header().Get("Location"))
	}
	marker := false
	for _, cookie := range revoked.Result().Cookies() {
		if cookie.Name == auth.SecureForwardAuthSignedOutCookie && cookie.MaxAge >= 0 {
			marker = true
		}
	}
	if !marker {
		t.Fatal("revoked app family did not suppress upstream")
	}
	frame := func(target string, cookies []*http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", target, nil)
		r.RemoteAddr = "127.0.0.1:4"
		r.Header.Set("Remote-User", "bob")
		r.Header.Set("Accept", "text/html")
		r.Header.Set("Sec-Fetch-Dest", "iframe")
		r.Header.Set("Sec-Fetch-Mode", "navigate")
		r.Header.Set("Sec-Fetch-Site", "same-site")
		for _, cookie := range cookies {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	iframe := frame("https://apps.example.com/app/demo/", nil)
	if iframe.Code != 303 {
		t.Fatalf("iframe app redirect: %d", iframe.Code)
	}
	iframeControl := frame(iframe.Header().Get("Location"), nil)
	if iframeControl.Code != 303 {
		t.Fatalf("iframe control launch: %d", iframeControl.Code)
	}
	iframeLaunch := frame(iframeControl.Header().Get("Location"), nil)
	if iframeLaunch.Code != 303 {
		t.Fatalf("iframe bound consume: %d", iframeLaunch.Code)
	}
	checked := frame(origin.String()+iframeLaunch.Header().Get("Location"), iframeLaunch.Result().Cookies())
	if checked.Code != 303 || checked.Header().Get("Location") != "/app/demo/" {
		t.Fatalf("iframe cookie confirmation: %d %s", checked.Code, checked.Header().Get("Location"))
	}
	liveFrame := frame(origin.String()+checked.Header().Get("Location"), iframeLaunch.Result().Cookies())
	if liveFrame.Code != 200 || hits != 1 {
		t.Fatalf("iframe failed to load: %d hits=%d", liveFrame.Code, hits)
	}
	blockedCookies := frame(origin.String()+iframeLaunch.Header().Get("Location"), nil)
	if blockedCookies.Code != 401 || blockedCookies.Header().Get("Location") != "" || len(blockedCookies.Result().Cookies()) != 0 {
		t.Fatalf("cookie-blocked iframe loops: %d %v", blockedCookies.Code, blockedCookies.Header())
	}

}
