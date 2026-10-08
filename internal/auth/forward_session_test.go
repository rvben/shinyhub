package auth

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestForwardBrowserFamilyRevocationAndOptOut(t *testing.T) {
	store := newFakeStore()
	store.users["alice"] = &ContextUser{ID: 1, Username: "alice", Role: "viewer"}
	revoked := map[string]bool{}
	cfg := ForwardAuthConfig{Enabled: true, UserHeader: "Remote-User", SessionSecret: "secret", SessionMaxAge: 12 * time.Hour, Revoked: func(jti string) (bool, error) { return revoked[jti], nil }}
	trusted := []*net.IPNet{mustCIDR(t, "127.0.0.0/8")}
	var ti *TokenInfo
	var user *ContextUser
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ti = TokenInfoFromContext(r.Context())
		user = UserFromContext(r.Context())
		if r.Header.Get("Remote-User") != "" {
			t.Fatal("identity header leaked")
		}
		w.WriteHeader(200)
	})
	h := ForwardAuthMiddleware(store, cfg, trusted)(next)
	run := func(cookies ...*http.Cookie) *httptest.ResponseRecorder {
		ti = nil
		user = nil
		r := httptest.NewRequest("GET", "https://example.com/api/auth/me", nil)
		r.RemoteAddr = "127.0.0.1:4"
		r.Header.Set("Remote-User", "alice")
		r.Header.Set("Sec-Fetch-Dest", "document")
		for _, c := range cookies {
			r.AddCookie(c)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	first := run()
	tracking := first.Result().Cookies()[0]
	if tracking.Name != SecureForwardAuthSessionCookie || !tracking.HttpOnly || !tracking.Secure || tracking.Domain != "" || tracking.MaxAge != 0 || !tracking.Expires.IsZero() {
		t.Fatalf("unsafe tracking cookie: %+v", tracking)
	}
	claims, err := ValidateForwardAuthSession(tracking.Value, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateJWT(tracking.Value, "secret", nil); err == nil {
		t.Fatal("tracking JWT grants native auth")
	}
	if user == nil || ti == nil || ti.JTI != claims.ID {
		t.Fatal("forward principal has no revocation family")
	}
	second := run(tracking)
	if second.Header().Get("Set-Cookie") != "" || second.Header().Get("Cache-Control") != "" {
		t.Fatal("existing family changed cookie or cache headers")
	}
	if ti == nil || ti.JTI != claims.ID {
		t.Fatal("family changed on refresh")
	}
	revoked[claims.ID] = true
	run(tracking)
	if user != nil {
		t.Fatal("revoked family fell back to upstream identity")
	}
	run(&http.Cookie{Name: SecureForwardAuthSignedOutCookie, Value: "1"})
	if user != nil {
		t.Fatal("signed-out marker ignored")
	}
	// An unprotected sibling-domain plain cookie cannot poison the HTTPS marker.
	run(&http.Cookie{Name: ForwardAuthSignedOutCookie, Value: "1"})
	if user == nil {
		t.Fatal("plain cookie shadowed secure marker")
	}
	run()
	if user == nil || ti.JTI == claims.ID {
		t.Fatal("new browser session cannot reconnect independently")
	}
}

func TestForwardFamilySurvivesNativeTTL(t *testing.T) {
	u := &ContextUser{ID: 1, Username: "alice", Role: "viewer"}
	original := time.Now().Add(-2 * time.Hour)
	tracking, ti, err := IssueForwardAuthSession(u, "secret", original, 12*time.Hour, "original-family")
	if err != nil {
		t.Fatal(err)
	}
	cfg := ForwardAuthConfig{SessionSecret: "secret", SessionMaxAge: 12 * time.Hour}
	r := httptest.NewRequest("GET", "https://example.com/", nil)
	r.AddCookie(&http.Cookie{Name: SecureForwardAuthSessionCookie, Value: tracking})
	w := httptest.NewRecorder()
	actual, ok, err := forwardBrowserFamily(w, r, u, cfg, nil)
	if err != nil || !ok || actual.JTI != ti.JTI || !actual.AuthTime.Equal(ti.AuthTime.Truncate(time.Second)) {
		t.Fatalf("family lost after native TTL: %+v %v", actual, err)
	}
}

func TestResumeRequiresDashboardOriginAndReferer(t *testing.T) {
	for _, ref := range []string{"", "https://evil.example/", "https://example.com/app/x/", "http://example.com/"} {
		r := httptest.NewRequest("POST", "https://example.com"+ForwardAuthResumePath, nil)
		r.Header.Set("Origin", "https://example.com")
		r.Header.Set("Referer", ref)
		if IsDashboardPost(r, nil) {
			t.Errorf("accepted referer %q", ref)
		}
	}
	r := httptest.NewRequest("POST", "https://example.com"+ForwardAuthResumePath, nil)
	r.Header.Set("Referer", "https://example.com/login")
	if !IsDashboardPost(r, nil) {
		t.Fatal("dashboard reconnect refused")
	}
	r.Header.Set("Origin", "http://example.com")
	if IsDashboardPost(r, nil) {
		t.Fatal("cross-scheme origin accepted")
	}
}

func TestConcurrentAppReconnectsCannotMintFreshFamilies(t *testing.T) {
	u := &ContextUser{ID: 1, Username: "alice", Role: "viewer"}
	cfg := ForwardAuthConfig{SessionSecret: "secret", SessionMaxAge: 12 * time.Hour}
	for _, dest := range []string{"", "document"} {
		t.Run("dest="+dest, func(t *testing.T) {
			for range 16 {
				t.Run("tab", func(t *testing.T) {
					t.Parallel()
					r := httptest.NewRequest("GET", "https://example.com/app/demo/websocket/", nil)
					r.Header.Set("Sec-Fetch-Dest", dest)
					r.Header.Set("Upgrade", "websocket")
					w := httptest.NewRecorder()
					ti, accepted, err := forwardBrowserFamily(w, r, u, cfg, nil)
					if !errors.Is(err, errForwardFamilyMissing) || accepted || ti != nil || len(w.Result().Cookies()) != 0 {
						t.Fatalf("orphan family minted: %v %v %v", ti, accepted, err)
					}
				})
			}
		})
	}
}

func TestForwardFamilyAfterMaxAgeReduction(t *testing.T) {
	u := &ContextUser{ID: 1, Username: "alice", Role: "viewer"}
	native, _, err := IssueBrowserSession(u, "secret", time.Now().Add(-2*time.Hour), 4*time.Hour, 12*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "https://example.com/login", nil)
	r.Header.Set("Sec-Fetch-Dest", "document")
	r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: native})
	w := httptest.NewRecorder()
	ti, accepted, err := forwardBrowserFamily(w, r, u, ForwardAuthConfig{SessionSecret: "secret", SessionMaxAge: time.Hour}, nil)
	if err != nil || !accepted || ti == nil || time.Since(ti.AuthTime) > time.Minute {
		t.Fatalf("cannot start fresh after policy change: %v %v %v", ti, accepted, err)
	}
}

func TestForwardHTTPNavigationWithoutFetchMetadata(t *testing.T) {
	u := &ContextUser{ID: 1, Username: "alice", Role: "viewer"}
	r := httptest.NewRequest("GET", "http://hub.example/", nil)
	r.Header.Set("Accept", "text/html,application/xhtml+xml")
	w := httptest.NewRecorder()
	ti, accepted, err := forwardBrowserFamily(w, r, u, ForwardAuthConfig{SessionSecret: "secret"}, nil)
	if err != nil || !accepted || ti == nil {
		t.Fatalf("HTTP SSO rejected: %v %v %v", ti, accepted, err)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != ForwardAuthSessionCookie || cookies[0].Secure {
		t.Fatalf("HTTP cookie: %v", cookies)
	}
	r.Header.Set("Upgrade", "websocket")
	if forwardPageLoad(r) {
		t.Fatal("HTML accept authorized websocket minting")
	}
}

func TestForwardResumeCanonicalOriginPorts(t *testing.T) {
	for _, tc := range []struct {
		target, origin string
		allowed        bool
	}{
		{"https://hub.example:443/api/auth/forward-auth/resume", "https://hub.example", true},
		{"http://hub.example:80/api/auth/forward-auth/resume", "http://hub.example", true},
		{"http://hub.example:443/api/auth/forward-auth/resume", "http://hub.example", false},
	} {
		r := httptest.NewRequest("POST", tc.target, nil)
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("Referer", tc.origin+"/login")
		if IsDashboardPost(r, nil) != tc.allowed {
			t.Fatalf("origin match wrong: %+v", tc)
		}
	}
}

func TestForwardFrameNavigationAndTerminalFailures(t *testing.T) {
	store := newFakeStore()
	store.users["alice"] = &ContextUser{ID: 1, Username: "alice", Role: "viewer"}
	trusted := []*net.IPNet{mustCIDR(t, "127.0.0.0/8")}
	hits := 0
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if UserFromContext(r.Context()) == nil || TokenInfoFromContext(r.Context()) == nil {
			t.Fatal("frame has no family")
		}
		w.WriteHeader(200)
	})
	h := ForwardAuthMiddleware(store, ForwardAuthConfig{Enabled: true, UserHeader: "Remote-User", SessionSecret: "secret", AppOriginHost: "apps.example", DashboardURL: "https://hub.example"}, trusted)(next)
	for _, tc := range []struct {
		name, target, dest, site, ref string
		code                          int
		cookie                        bool
	}{
		{"same-site control", "https://hub.example/app/demo/", "iframe", "same-site", "", 200, true},
		{"same-site app", "https://apps.example/app/demo/", "frame", "same-site", "", 303, false},
		{"cross-site control", "https://hub.example/app/demo/", "iframe", "cross-site", "", 401, false},
		{"cross-site app", "https://apps.example/app/demo/", "iframe", "cross-site", "", 401, false},
		{"legacy cross-site", "https://apps.example/app/demo/", "", "", "https://external.test/", 303, false},
		{"legacy unknown blocked cookies", "https://apps.example/app/demo/?__shinyhub_cookie_check=1", "", "", "", 401, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", tc.target, nil)
			r.RemoteAddr = "127.0.0.1:4"
			r.Header.Set("Remote-User", "alice")
			r.Header.Set("Accept", "text/html")
			r.Header.Set("Sec-Fetch-Dest", tc.dest)
			r.Header.Set("Sec-Fetch-Site", tc.site)
			if tc.dest != "" {
				r.Header.Set("Sec-Fetch-Mode", "navigate")
			}
			r.Header.Set("Referer", tc.ref)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.code || (len(w.Result().Cookies()) > 0) != tc.cookie {
				t.Fatalf("frame response: %d cookies=%v", w.Code, w.Result().Cookies())
			}
			if tc.code == 401 && (w.Header().Get("Location") != "" || !strings.Contains(w.Body.String(), "new tab")) {
				t.Fatalf("nonterminal frame: %v %s", w.Header(), w.Body.String())
			}
			if tc.code == 303 && w.Header().Get("Location") != "https://hub.example/app/demo/" {
				t.Fatalf("bad frame handoff: %s", w.Header().Get("Location"))
			}
		})
	}
	if hits != 1 {
		t.Fatalf("unexpected backend access: %d", hits)
	}
}

func TestForwardLegacyCrossSiteLinkKeepsValidFamily(t *testing.T) {
	store := newFakeStore()
	u := &ContextUser{ID: 1, Username: "alice", Role: "viewer"}
	store.users["alice"] = u
	token, _, err := IssueForwardAuthSession(u, "secret", time.Now(), 12*time.Hour, "")
	if err != nil {
		t.Fatal(err)
	}
	trusted := []*net.IPNet{mustCIDR(t, "127.0.0.0/8")}
	hits := 0
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if UserFromContext(r.Context()) == nil || TokenInfoFromContext(r.Context()) == nil {
			t.Fatal("family lost on ordinary link")
		}
		w.WriteHeader(200)
	})
	h := ForwardAuthMiddleware(store, ForwardAuthConfig{Enabled: true, UserHeader: "Remote-User", SessionSecret: "secret"}, trusted)(next)
	r := httptest.NewRequest("GET", "http://hub.lan/app/demo/", nil)
	r.RemoteAddr = "127.0.0.1:4"
	r.Header.Set("Remote-User", "alice")
	r.Header.Set("Accept", "text/html")
	r.Header.Set("Referer", "http://wiki.lan/page")
	r.AddCookie(&http.Cookie{Name: ForwardAuthSessionCookie, Value: token})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || hits != 1 || len(w.Result().Cookies()) != 0 {
		t.Fatalf("legacy link rejected: %d hits=%d cookies=%v", w.Code, hits, w.Result().Cookies())
	}
}

func TestForwardFrameTerminalLinkStaysLocal(t *testing.T) {
	r := httptest.NewRequest("GET", "https://hub.example//external.test/", nil)
	w := httptest.NewRecorder()
	forwardFrameUnavailable(w, r)
	if strings.Contains(w.Body.String(), `href="//external.test/"`) || !strings.Contains(w.Body.String(), `href="/"`) {
		t.Fatalf("unsafe terminal link: %s", w.Body.String())
	}
}
