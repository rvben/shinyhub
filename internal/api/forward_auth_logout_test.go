package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

type logoutFixture struct {
	cfg     *config.Config
	store   *db.Store
	server  *Server
	handler http.Handler
	users   map[string]*auth.ContextUser
}

func newLogoutFixture(t *testing.T, isolated bool) *logoutFixture {
	t.Helper()
	store := dbtest.New(t)
	users := map[string]*auth.ContextUser{}
	for _, name := range []string{"alice", "bob", "carol", "victim"} {
		if err := store.CreateUser(db.CreateUserParams{Username: name, PasswordHash: "unused", Role: "viewer"}); err != nil {
			t.Fatal(err)
		}
		u, _ := store.GetUserByUsername(name)
		users[name] = u.ContextUser()
	}
	_, network, _ := net.ParseCIDR("127.0.0.0/8")
	cfg := &config.Config{Auth: config.AuthConfig{Secret: "test-secret", ForwardAuth: config.ForwardAuthConfig{Enabled: true}}, Server: config.ServerConfig{BaseURL: "https://hub.example"}, Storage: config.StorageConfig{AppsDir: t.TempDir()}, TrustedProxyNets: []*net.IPNet{network}}
	if isolated {
		cfg.Server.AppOrigin = "https://apps.example"
	}
	server := New(cfg, store, nil, nil)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == auth.AppLogoutPath {
			server.HandleAppLogout(w, r)
		} else {
			server.Router().ServeHTTP(w, r)
		}
	})
	handler := auth.ForwardAuthMiddleware(store, auth.ForwardAuthConfig{Enabled: true, UserHeader: "Remote-User", DefaultRole: "viewer", SessionSecret: cfg.Auth.Secret, SessionMaxAge: cfg.Auth.BrowserSessionMaxAge(), Revoked: store.IsTokenRevoked, AppOriginHost: "apps.example", DashboardURL: cfg.Server.BaseURL}, cfg.TrustedProxyNets)(next)
	return &logoutFixture{cfg, store, server, handler, users}
}
func (f *logoutFixture) request(method, target, upstream string, cookies []*http.Cookie, bearer string) *httptest.ResponseRecorder {
	if strings.HasPrefix(target, "/") {
		target = f.cfg.Server.BaseURL + target
	}
	r := httptest.NewRequest(method, target, nil)
	r.RemoteAddr = "127.0.0.1:4"
	if upstream != "" {
		r.Header.Set("Remote-User", upstream)
	}
	if method == http.MethodGet {
		r.Header.Set("Sec-Fetch-Dest", "document")
	}
	for _, c := range cookies {
		if c.MaxAge >= 0 {
			r.AddCookie(c)
		}
	}
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	if method == "POST" {
		u, _ := url.Parse(target)
		origin := u.Scheme + "://" + u.Host
		r.Header.Set("Origin", origin)
		r.Header.Set("Referer", origin+"/login")
		r.Header.Set(auth.CSRFHeaderName, "csrf")
		r.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: "csrf"})
	}
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	return w
}
func mergeLogoutCookies(before []*http.Cookie, response *httptest.ResponseRecorder) []*http.Cookie {
	values := map[string]*http.Cookie{}
	for _, c := range before {
		values[c.Name] = c
	}
	for _, c := range response.Result().Cookies() {
		if c.MaxAge < 0 {
			delete(values, c.Name)
		} else {
			values[c.Name] = c
		}
	}
	out := make([]*http.Cookie, 0, len(values))
	for _, c := range values {
		if c.Name != auth.CSRFCookieName {
			out = append(out, c)
		}
	}
	return out
}
func fixtureToken(t *testing.T, f *logoutFixture, user string) (string, *auth.TokenInfo) {
	t.Helper()
	token, ti, err := auth.IssueBrowserSession(f.users[user], f.cfg.Auth.Secret, time.Time{}, time.Hour, 12*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return token, ti
}
func cookieNamed(cookies []*http.Cookie, name string) *http.Cookie {
	for _, c := range cookies {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func TestForwardLogoutRevokesEveryPresentedFamilyAndBlocksRefresh(t *testing.T) {
	f := newLogoutFixture(t, false)
	first := f.request("GET", "/api/auth/me", "alice", nil, "")
	cookies := mergeLogoutCookies(nil, first)
	tracking := cookieNamed(cookies, auth.SecureForwardAuthSessionCookie)
	if tracking == nil {
		t.Fatal("no browser family")
	}
	claims, _ := auth.ValidateForwardAuthSession(tracking.Value, f.cfg.Auth.Secret)
	var payload map[string]any
	_ = json.Unmarshal(first.Body.Bytes(), &payload)
	if payload["session"] != nil || cookieNamed(cookies, auth.SessionCookieName) != nil {
		t.Fatal("forward identity minted a native dashboard credential")
	}
	native, nativeInfo := fixtureToken(t, f, "bob")
	bearer, bearerInfo := fixtureToken(t, f, "carol")
	cookies = append(cookies, &http.Cookie{Name: auth.SessionCookieName, Value: native})
	logout := f.request("POST", "/api/auth/logout", "alice", cookies, bearer)
	if logout.Code != 204 {
		t.Fatalf("logout %d %s", logout.Code, logout.Body.String())
	}
	cookies = mergeLogoutCookies(cookies, logout)
	for _, jti := range []string{claims.ID, nativeInfo.JTI, bearerInfo.JTI} {
		revoked, err := f.store.IsTokenRevoked(jti)
		if err != nil || !revoked {
			t.Fatalf("family %s remains valid: %v", jti, err)
		}
	}
	if response := f.request("GET", "/api/auth/me", "alice", cookies, ""); response.Code != 401 {
		t.Fatalf("refresh reauthenticated: %d %s", response.Code, response.Body.String())
	}
	if response := f.request("GET", "/api/auth/me", "", nil, native); response.Code != 401 {
		t.Fatal("native cookie JWT replay succeeded")
	}
	resumed := f.request("POST", auth.ForwardAuthResumePath, "alice", cookies, "")
	if resumed.Code != 204 {
		t.Fatalf("resume %d %s", resumed.Code, resumed.Body.String())
	}
	cookies = mergeLogoutCookies(cookies, resumed)
	if response := f.request("GET", "/api/auth/me", "alice", cookies, ""); response.Code != 200 {
		t.Fatal("explicit reconnect failed")
	}
	fresh, _ := auth.ValidateForwardAuthSession(cookieNamed(cookies, auth.SecureForwardAuthSessionCookie).Value, f.cfg.Auth.Secret)
	if fresh.ID == claims.ID {
		t.Fatal("resume reused revoked family")
	}
}

func TestForwardLogoutSecondWriteFailureRollsBackAndKeepsCookies(t *testing.T) {
	f := newLogoutFixture(t, true)
	a, ai := fixtureToken(t, f, "alice")
	b, bi := fixtureToken(t, f, "bob")
	drop := installDBFailureTrigger(t, f.store, dbFailureTrigger{name: "fail_logout_second", table: "revoked_tokens", event: "INSERT", condition: fmt.Sprintf("NEW.jti='%s'", bi.JTI)})
	cookies := []*http.Cookie{{Name: auth.SessionCookieName, Value: a}}
	response := f.request("POST", "/api/auth/logout", "alice", cookies, b)
	if response.Code != 503 {
		t.Fatalf("failure %d %s", response.Code, response.Body.String())
	}
	for _, c := range response.Result().Cookies() {
		if c.Name == auth.SessionCookieName || c.Name == auth.SecureForwardAuthSignedOutCookie || c.Name == auth.SecureLogoutHandoffCookie {
			t.Fatalf("failed logout changed cookie %s", c.Name)
		}
	}
	for _, id := range []string{ai.JTI, bi.JTI} {
		revoked, _ := f.store.IsTokenRevoked(id)
		if revoked {
			t.Fatal("transaction left a half-revoked browser")
		}
	}
	drop()
	if response = f.request("POST", "/api/auth/logout", "alice", cookies, b); response.Code != 204 {
		t.Fatalf("retry %d", response.Code)
	}
}

func TestScopedAppOriginLogoutAndCompletion(t *testing.T) {
	f := newLogoutFixture(t, true)
	f.cfg.Auth.ForwardAuth.LogoutURL = "https://auth.example/logout"
	first := f.request("GET", "/api/auth/me", "alice", nil, "")
	dashboard := mergeLogoutCookies(nil, first)
	loggedOut := f.request("POST", "/api/auth/logout", "alice", dashboard, "")
	dashboard = mergeLogoutCookies(dashboard, loggedOut)
	bridge := f.request("GET", auth.ForwardAuthLogoutPath, "alice", dashboard, "")
	target := bridge.Header().Get("Location")
	if bridge.Code != 303 || !strings.HasPrefix(target, "https://apps.example/api/app/logout?code=") {
		t.Fatalf("bridge %d %s", bridge.Code, target)
	}
	legacy, legacyInfo := fixtureToken(t, f, "alice")
	app := f.request("GET", target, "alice", []*http.Cookie{{Name: auth.SessionCookieName, Value: legacy}}, "")
	if app.Code != 303 {
		t.Fatalf("app cleanup %d %s", app.Code, app.Body.String())
	}
	revoked, _ := f.store.IsTokenRevoked(legacyInfo.JTI)
	if !revoked {
		t.Fatal("legacy unrelated app JWT survived")
	}
	appCookies := mergeLogoutCookies(nil, app)
	if cookieNamed(appCookies, auth.SecureForwardAuthSignedOutCookie) == nil {
		t.Fatal("app origin can fall back to upstream identity")
	}
	if repeat := f.request("GET", target, "alice", nil, ""); repeat.Code != 401 {
		t.Fatal("app code replay accepted")
	}
	completion := app.Header().Get("Location")
	forged := f.request("GET", auth.ForwardAuthLogoutPath+"?complete=forged", "alice", dashboard, "")
	if forged.Code != 400 {
		t.Fatal("forged completion accepted")
	}
	finished := f.request("GET", completion, "alice", dashboard, "")
	if finished.Code != 303 || finished.Header().Get("Location") != f.cfg.Auth.ForwardAuth.LogoutURL {
		t.Fatalf("completion %d %s", finished.Code, finished.Header().Get("Location"))
	}
	if repeat := f.request("GET", completion, "alice", dashboard, ""); repeat.Code != 400 {
		t.Fatal("completion replay accepted")
	}
	dashboard = mergeLogoutCookies(dashboard, finished)
	if cookieNamed(dashboard, auth.SecureLogoutHandoffCookie) != nil {
		t.Fatal("handoff capability retained")
	}
	if response := f.request("GET", "https://apps.example/api/auth/me", "alice", appCookies, ""); response.Code != 401 {
		t.Fatal("app marker failed after native cookie disappeared")
	}
}

func TestAppLogoutCannotAffectAnotherUserOrMintFromMarker(t *testing.T) {
	f := newLogoutFixture(t, true)
	marker := []*http.Cookie{{Name: auth.SecureForwardAuthSignedOutCookie, Value: "1"}}
	response := f.request("GET", auth.ForwardAuthLogoutPath, "alice", marker, "")
	if strings.Contains(response.Header().Get("Location"), "/api/app/logout") {
		t.Fatal("unsigned marker minted a capability")
	}
	first := f.request("GET", "/api/auth/me", "alice", nil, "")
	cookies := mergeLogoutCookies(nil, first)
	logout := f.request("POST", "/api/auth/logout", "alice", cookies, "")
	cookies = mergeLogoutCookies(cookies, logout)
	bridge := f.request("GET", auth.ForwardAuthLogoutPath, "alice", cookies, "")
	victim, ti := fixtureToken(t, f, "victim")
	app := f.request("GET", bridge.Header().Get("Location"), "victim", []*http.Cookie{{Name: auth.SessionCookieName, Value: victim}}, "")
	if app.Code != 303 {
		t.Fatal(app.Body.String())
	}
	revoked, _ := f.store.IsTokenRevoked(ti.JTI)
	if revoked {
		t.Fatal("another user's token revoked")
	}
	for _, c := range app.Result().Cookies() {
		if c.Name == auth.SessionCookieName || c.Name == auth.SecureForwardAuthSignedOutCookie {
			t.Fatal("another user's browser changed")
		}
	}
}

func TestForwardResumeFailureHasNoCookieSideEffects(t *testing.T) {
	f := newLogoutFixture(t, false)
	for _, tc := range []struct {
		upstream, origin, referer string
		status                    int
	}{{"", "https://hub.example", "https://hub.example/login", 401}, {"alice", "https://evil.example", "https://hub.example/login", 403}, {"alice", "https://hub.example", "https://hub.example/app/x/", 403}, {"alice", "https://hub.example", "", 403}} {
		r := httptest.NewRequest("POST", f.cfg.Server.BaseURL+auth.ForwardAuthResumePath, nil)
		r.RemoteAddr = "127.0.0.1:4"
		r.Header.Set("Remote-User", tc.upstream)
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("Referer", tc.referer)
		r.AddCookie(&http.Cookie{Name: auth.SecureForwardAuthSignedOutCookie, Value: "1"})
		w := httptest.NewRecorder()
		f.handler.ServeHTTP(w, r)
		if w.Code != tc.status || len(w.Result().Cookies()) != 0 {
			t.Fatalf("resume %+v: %d cookies=%v", tc, w.Code, w.Result().Cookies())
		}
	}
}

func TestUpstreamPostLogoutBridgeCSPAndEscaping(t *testing.T) {
	f := newLogoutFixture(t, false)
	f.cfg.Auth.ForwardAuth.LogoutURL = "https://auth.example/logout?q=\"&x=<tag>"
	f.cfg.Auth.ForwardAuth.LogoutMethod = "POST"
	response := f.request("GET", auth.ForwardAuthLogoutPath, "alice", []*http.Cookie{{Name: auth.SecureForwardAuthSignedOutCookie, Value: "1"}}, "")
	if response.Code != 200 || strings.Contains(response.Body.String(), `x=<tag>`) || !strings.Contains(response.Body.String(), `method="post"`) || !strings.Contains(response.Header().Get("Content-Security-Policy"), "sha256-") || strings.Contains(response.Header().Get("Content-Security-Policy"), "form-action") || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("bridge %d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
}

func TestForwardLogoutDoesNotRevokeOtherBrowser(t *testing.T) {
	f := newLogoutFixture(t, false)
	a := mergeLogoutCookies(nil, f.request("GET", "/api/auth/me", "alice", nil, ""))
	b := mergeLogoutCookies(nil, f.request("GET", "/api/auth/me", "alice", nil, ""))
	if f.request("POST", "/api/auth/logout", "alice", a, "").Code != 204 {
		t.Fatal("logout failed")
	}
	if f.request("GET", "/api/auth/me", "alice", b, "").Code != 200 {
		t.Fatal("other browser was signed out")
	}
}

func TestAppLogoutFailureKeepsCapabilityRetryable(t *testing.T) {
	f := newLogoutFixture(t, true)
	dashboard := mergeLogoutCookies(nil, f.request("GET", "/api/auth/me", "alice", nil, ""))
	dashboard = mergeLogoutCookies(dashboard, f.request("POST", "/api/auth/logout", "alice", dashboard, ""))
	target := f.request("GET", auth.ForwardAuthLogoutPath, "alice", dashboard, "").Header().Get("Location")
	legacy, ti := fixtureToken(t, f, "alice")
	cookies := []*http.Cookie{{Name: auth.SessionCookieName, Value: legacy}}
	drop := installDBFailureTrigger(t, f.store, dbFailureTrigger{name: "fail_app_logout", table: "revoked_tokens", event: "INSERT", condition: fmt.Sprintf("NEW.jti='%s'", ti.JTI)})
	failed := f.request("GET", target, "alice", cookies, "")
	if failed.Code != 503 || len(failed.Result().Cookies()) != 0 {
		t.Fatalf("failed cleanup %d cookies=%v", failed.Code, failed.Result().Cookies())
	}
	drop()
	retry := f.request("GET", target, "alice", cookies, "")
	if retry.Code != 303 {
		t.Fatalf("retry %d %s", retry.Code, retry.Body.String())
	}
}

func TestNativeSessionUnderOptOutIsNotOverridden(t *testing.T) {
	f := newLogoutFixture(t, false)
	native, _ := fixtureToken(t, f, "bob")
	cookies := []*http.Cookie{{Name: auth.SessionCookieName, Value: native}, {Name: auth.SecureForwardAuthSignedOutCookie, Value: "1"}}
	response := f.request("GET", "/api/auth/me", "alice", cookies, "")
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"username":"bob"`) {
		t.Fatalf("native B was replaced by upstream A: %d %s", response.Code, response.Body.String())
	}
	for _, c := range response.Result().Cookies() {
		if c.Name == auth.SecureForwardAuthSignedOutCookie && c.MaxAge < 0 {
			t.Fatal("native session renewal cleared opt-out")
		}
	}
	handoff := f.request("POST", "/api/auth/handoff", "alice", cookies, "")
	if handoff.Code != 303 {
		t.Fatalf("native handoff %d", handoff.Code)
	}
}

func TestFirstForwardLogoutRevokesNewlyIssuedFamily(t *testing.T) {
	f := newLogoutFixture(t, false)
	response := f.request("POST", "/api/auth/logout", "alice", nil, "")
	if response.Code != 204 {
		t.Fatalf("logout %d %s", response.Code, response.Body.String())
	}
	var issued string
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == auth.SecureForwardAuthSessionCookie && cookie.Value != "" {
			issued = cookie.Value
		}
	}
	claims, err := auth.ValidateForwardAuthSession(issued, f.cfg.Auth.Secret)
	if err != nil {
		t.Fatal(err)
	}
	revoked, err := f.store.IsTokenRevoked(claims.ID)
	if err != nil || !revoked {
		t.Fatal("newly minted family survived logout")
	}
}

func TestForwardLogoutDeduplicatesAndSkipsDeletedUser(t *testing.T) {
	f := newLogoutFixture(t, false)
	native, ti := fixtureToken(t, f, "alice")
	response := f.request("POST", "/api/auth/logout", "alice", []*http.Cookie{{Name: auth.SessionCookieName, Value: native}}, native)
	if response.Code != 204 {
		t.Fatal(response.Body.String())
	}
	var count int
	if err := f.store.DB().QueryRow("SELECT COUNT(*) FROM revoked_tokens").Scan(&count); err != nil || count != 1 {
		t.Fatalf("same JTI duplicated: count=%d err=%v", count, err)
	}
	revoked, _ := f.store.IsTokenRevoked(ti.JTI)
	if !revoked {
		t.Fatal("deduplicated token not revoked")
	}
	dead, _ := fixtureToken(t, f, "bob")
	if err := f.store.DeleteUser(f.users["bob"].ID); err != nil {
		t.Fatal(err)
	}
	response = f.request("POST", "/api/auth/logout", "alice", []*http.Cookie{{Name: auth.SessionCookieName, Value: dead}}, "")
	if response.Code != 204 {
		t.Fatalf("deleted user prevented cleanup: %d %s", response.Code, response.Body.String())
	}
}

func TestLogoutCompletionFailureIsRetryable(t *testing.T) {
	f := newLogoutFixture(t, true)
	cookies := mergeLogoutCookies(nil, f.request("GET", "/api/auth/me", "alice", nil, ""))
	cookies = mergeLogoutCookies(cookies, f.request("POST", "/api/auth/logout", "alice", cookies, ""))
	bridge := f.request("GET", auth.ForwardAuthLogoutPath, "alice", cookies, "")
	app := f.request("GET", bridge.Header().Get("Location"), "alice", nil, "")
	completion := app.Header().Get("Location")
	drop := installDBFailureTrigger(t, f.store, dbFailureTrigger{name: "fail_completion", table: "browser_logout_codes", event: "DELETE", condition: "OLD.kind='complete'"})
	failed := f.request("GET", completion, "alice", cookies, "")
	if failed.Code != 503 || len(failed.Result().Cookies()) != 0 {
		t.Fatalf("completion failure %d cookies=%v", failed.Code, failed.Result().Cookies())
	}
	drop()
	if response := f.request("GET", completion, "alice", cookies, ""); response.Code != 303 {
		t.Fatalf("completion retry %d", response.Code)
	}
}

func TestForwardAdminSupportSessionDelegatesFreshnessToGateway(t *testing.T) {
	f := newLogoutFixture(t, true)
	f.cfg.Auth.SupportSessions = true
	if err := f.store.CreateUser(db.CreateUserParams{Username: "support-admin", PasswordHash: "unused", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	admin, err := f.store.GetUserByUsername("support-admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.CreateApp(db.CreateAppParams{Slug: "sales", Name: "Sales", ProjectSlug: "default", OwnerID: admin.ID, Access: "public"}); err != nil {
		t.Fatal(err)
	}
	tracking, _, err := auth.IssueForwardAuthSession(admin.ContextUser(), f.cfg.Auth.Secret, time.Now().Add(-20*time.Minute), 12*time.Hour, "old-admin-family")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"user_id": f.users["bob"].ID, "app_slug": "sales", "reason": "Investigating ticket SUP-1042"})
	r := httptest.NewRequest("POST", "https://hub.example/api/support-sessions", strings.NewReader(string(body)))
	r.RemoteAddr = "127.0.0.1:4"
	r.Header.Set("Remote-User", "support-admin")
	r.Header.Set("Origin", "https://hub.example")
	r.Header.Set("Referer", "https://hub.example/")
	r.Header.Set(auth.CSRFHeaderName, "csrf")
	r.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: "csrf"})
	r.AddCookie(&http.Cookie{Name: auth.SecureForwardAuthSessionCookie, Value: tracking})
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("support creation: %d %s", w.Code, w.Body.String())
	}
	if w.Header().Get("Set-Cookie") != "" {
		t.Fatal("support creation renewed the forward family")
	}
}

func TestForwardAccountHandoffPreservesScopedLocalReturn(t *testing.T) {
	for _, next := range []string{"/app/sales/?tab=one", "https://evil.example/"} {
		t.Run(next, func(t *testing.T) {
			f := newLogoutFixture(t, true)
			r := httptest.NewRequest("POST", "https://hub.example/api/auth/handoff", strings.NewReader(url.Values{"next": {next}}.Encode()))
			r.RemoteAddr = "127.0.0.1:4"
			r.Header.Set("Remote-User", "alice")
			r.Header.Set("Origin", "https://hub.example")
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			w := httptest.NewRecorder()
			f.handler.ServeHTTP(w, r)
			if w.Code != 303 {
				t.Fatalf("handoff %d %s", w.Code, w.Body.String())
			}
			cookies := mergeLogoutCookies(nil, w)
			appURL := f.request("GET", w.Header().Get("Location"), "alice", cookies, "").Header().Get("Location")
			app := f.request("GET", appURL, "alice", nil, "")
			if app.Code != 303 {
				t.Fatalf("app cleanup: %d %s", app.Code, app.Body.String())
			}
			completion := app.Header().Get("Location") + "&next=https://attacker.example/"
			finished := f.request("GET", completion, "alice", cookies, "")
			want := "/login"
			if safe := safeNextPath(next); safe != "" {
				want += "?" + url.Values{"next": {safe}}.Encode()
			}
			if finished.Code != 303 || finished.Header().Get("Location") != want {
				t.Fatalf("lost or injected destination: %d %s want %s", finished.Code, finished.Header().Get("Location"), want)
			}
		})
	}
}

func TestForwardEdgeAuthorizationHeaderStillLogsBrowserOut(t *testing.T) {
	f := newLogoutFixture(t, false)
	first := f.request("GET", "/api/auth/me", "alice", nil, "edge-owned-jwt")
	if first.Code != 200 {
		t.Fatalf("edge browser auth: %d", first.Code)
	}
	cookies := mergeLogoutCookies(nil, first)
	if cookieNamed(cookies, auth.SecureForwardAuthSessionCookie) == nil {
		t.Fatal("edge Authorization disabled tracking")
	}
	logout := f.request("POST", "/api/auth/logout", "alice", cookies, "edge-owned-jwt")
	if logout.Code != 204 {
		t.Fatalf("logout: %d %s", logout.Code, logout.Body.String())
	}
	cookies = mergeLogoutCookies(cookies, logout)
	if cookieNamed(cookies, auth.SecureForwardAuthSignedOutCookie) == nil {
		t.Fatal("no optout marker")
	}
	if f.request("GET", "/api/auth/me", "alice", cookies, "edge-owned-jwt").Code != 401 {
		t.Fatal("edge JWT reauthenticated browser")
	}
}

func TestAnonymousForwardHandoffKeepsLocalNext(t *testing.T) {
	f := newLogoutFixture(t, true)
	r := httptest.NewRequest("POST", "https://hub.example/api/auth/handoff", strings.NewReader("next=%2Fapp%2Fsales%2F"))
	r.Header.Set("Origin", "https://hub.example")
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	if w.Code != 303 || w.Header().Get("Location") != "/login?next=%2Fapp%2Fsales%2F" {
		t.Fatalf("return path lost: %d %s", w.Code, w.Header().Get("Location"))
	}
}

func TestInvalidForwardFamilyLeavesShellReachableAndResumeWorks(t *testing.T) {
	for _, cause := range []string{"epoch", "revoked"} {
		t.Run(cause, func(t *testing.T) {
			f := newLogoutFixture(t, false)
			first := f.request("GET", "/api/auth/me", "alice", nil, "")
			cookies := mergeLogoutCookies(nil, first)
			tracker := cookieNamed(cookies, auth.SecureForwardAuthSessionCookie)
			claims, err := auth.ValidateForwardAuthSession(tracker.Value, f.cfg.Auth.Secret)
			if err != nil {
				t.Fatal(err)
			}
			if cause == "epoch" {
				err = f.store.BumpTokenEpoch(f.users["alice"].ID)
			} else {
				err = f.store.RevokeToken(claims.ID, claims.UserID, claims.ExpiresAt.Time)
			}
			if err != nil {
				t.Fatal(err)
			}
			foreign, _ := fixtureToken(t, f, "bob")
			cookies = append(cookies, &http.Cookie{Name: auth.SessionCookieName, Value: foreign})
			original := f.handler
			shellHits := 0
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/" || r.URL.Path == "/login" {
					shellHits++
					if auth.UserFromContext(r.Context()) != nil || auth.TokenInfoFromContext(r.Context()) != nil || auth.ForwardAuthFromContext(r.Context()) || auth.CredentialInfoFromContext(r.Context()) != nil || r.Header.Get("Authorization") != "" {
						t.Fatal("invalid family retained authentication")
					}
					if _, err := r.Cookie(auth.SessionCookieName); err == nil {
						t.Fatal("foreign native cookie retained")
					}
					_, _ = w.Write([]byte("login shell"))
					return
				}
				original.ServeHTTP(w, r)
			})
			f.handler = auth.ForwardAuthMiddleware(f.store, auth.ForwardAuthConfig{Enabled: true, UserHeader: "Remote-User", SessionSecret: f.cfg.Auth.Secret, Revoked: f.store.IsTokenRevoked}, f.cfg.TrustedProxyNets)(next)
			root := f.request("GET", "/", "alice", cookies, foreign)
			if root.Code != 200 || root.Body.String() != "login shell" || shellHits != 1 {
				t.Fatalf("shell locked out: %d %s", root.Code, root.Body.String())
			}
			cookies = mergeLogoutCookies(cookies, root)
			if cookieNamed(cookies, auth.SessionCookieName) != nil || cookieNamed(cookies, auth.SecureForwardAuthSessionCookie) != nil || cookieNamed(cookies, auth.SecureForwardAuthSignedOutCookie) == nil {
				t.Fatal("invalid family did not become signed-out")
			}
			providers := f.request("GET", "/api/auth/providers", "alice", cookies, "")
			if providers.Code != 200 {
				t.Fatalf("providers blocked: %d", providers.Code)
			}
			resumed := f.request("POST", auth.ForwardAuthResumePath, "alice", cookies, "")
			if resumed.Code != 204 {
				t.Fatalf("cannot resume: %d %s", resumed.Code, resumed.Body.String())
			}
			cookies = mergeLogoutCookies(cookies, resumed)
			fresh := cookieNamed(cookies, auth.SecureForwardAuthSessionCookie)
			live, err := auth.ValidateForwardAuthSession(fresh.Value, f.cfg.Auth.Secret)
			if err != nil || live.ID == claims.ID {
				t.Fatal("resume did not create fresh family")
			}
			if f.request("GET", "/api/auth/me", "alice", cookies, "").Code != 200 {
				t.Fatal("resumed family rejected")
			}
		})
	}
}

func TestSelfRevokeForwardFamilyImmediatelyMarksSignedOut(t *testing.T) {
	f := newLogoutFixture(t, false)
	cookies := mergeLogoutCookies(nil, f.request("GET", "/api/auth/me", "alice", nil, ""))
	w := f.request("POST", "/api/auth/revoke-sessions", "alice", cookies, "")
	if w.Code != 204 {
		t.Fatalf("self revoke: %d %s", w.Code, w.Body.String())
	}
	cookies = mergeLogoutCookies(cookies, w)
	if cookieNamed(cookies, auth.SecureForwardAuthSessionCookie) != nil || cookieNamed(cookies, auth.SecureForwardAuthSignedOutCookie) == nil {
		t.Fatal("revoked family left active in browser")
	}
}

func TestForwardServiceClientRetainsAPIWithoutBrowserCookies(t *testing.T) {
	f := newLogoutFixture(t, false)
	r := httptest.NewRequest("GET", "https://hub.example/api/auth/me", nil)
	r.RemoteAddr = "127.0.0.1:4"
	r.Header.Set("Remote-User", "alice")
	r.Header.Set("Accept", "application/json")
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("service client regression: %d", w.Code)
	}
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name != auth.CSRFCookieName {
			t.Fatalf("service client got auth tracking: %+v", cookie)
		}
	}
}

func TestBearerOnlyPublicHandoffRecordsRevocationAudit(t *testing.T) {
	f := newLogoutFixture(t, false)
	token, ti := fixtureToken(t, f, "bob")
	w := f.request("POST", "/api/auth/handoff", "", nil, token)
	if w.Code != 303 {
		t.Fatalf("handoff: %d %s", w.Code, w.Body.String())
	}
	revoked, _ := f.store.IsTokenRevoked(ti.JTI)
	if !revoked {
		t.Fatal("bearer not revoked")
	}
	events, err := f.store.ListAuditEvents("logout_handoff", 10, 0)
	if err != nil || len(events) != 1 || events[0].UserID == nil || *events[0].UserID != f.users["bob"].ID {
		t.Fatalf("missing bearer audit: %+v %v", events, err)
	}
}

func TestCompletionConsumeSweepsExpiredCapabilities(t *testing.T) {
	f := newLogoutFixture(t, false)
	if _, err := f.store.DB().Exec("INSERT INTO browser_logout_codes(code_hash,kind,user_ids,created_at) VALUES(?,'complete','[]',?)", "expired", "2000-01-01 00:00:00"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ConsumeBrowserLogoutCompletion("missing", "parent"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("missing consume: %v", err)
	}
	var count int
	if err := f.store.DB().QueryRow("SELECT COUNT(*) FROM browser_logout_codes").Scan(&count); err != nil || count != 0 {
		t.Fatalf("expired codes retained: %d %v", count, err)
	}
}
