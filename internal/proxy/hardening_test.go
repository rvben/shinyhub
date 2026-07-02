package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func cookieMap(raw string) map[string]string {
	out := map[string]string{}
	req := &http.Request{Header: http.Header{"Cookie": {raw}}}
	for _, c := range req.Cookies() {
		out[c.Name] = c.Value
	}
	return out
}

// TestStripInternalCookies verifies ShinyHub's own auth/session/sticky cookies
// are removed from the request before it is forwarded to the (developer-
// controlled) app backend, while unrelated app cookies pass through.
func TestStripInternalCookies(t *testing.T) {
	req := httptest.NewRequest("GET", "/app/demo/", nil)
	req.Header.Set("Cookie", "shiny_session=THE_JWT; csrf_token=abc; shiny_oauth_state=xyz; shinyhub_rep_demo=2; theme=dark; sid=keepme")

	stripInternalCookies(req)

	got := cookieMap(req.Header.Get("Cookie"))
	for _, internal := range []string{"shiny_session", "csrf_token", "shiny_oauth_state", "shinyhub_rep_demo"} {
		if _, present := got[internal]; present {
			t.Errorf("internal cookie %q must be stripped before forwarding, got %v", internal, got)
		}
	}
	if got["theme"] != "dark" || got["sid"] != "keepme" {
		t.Errorf("non-ShinyHub cookies must be preserved, got %v", got)
	}
}

// TestStripInternalCookies_StripsClientIDCookie verifies the per-app elastic
// client-id cookie (shinyhub_cid_<slug>) is stripped before the request reaches
// the app backend, so a backend cannot harvest another visitor's cid value and
// later pin that visitor to a worker it controls.
func TestStripInternalCookies_StripsClientIDCookie(t *testing.T) {
	req := httptest.NewRequest("GET", "/app/demo/", nil)
	req.Header.Set("Cookie", clientCookiePrefix+"demo=abcdef0123456789abcdef0123456789.deadbeefdeadbeef; theme=dark")

	stripInternalCookies(req)

	got := cookieMap(req.Header.Get("Cookie"))
	if _, present := got[clientCookiePrefix+"demo"]; present {
		t.Errorf("client-id cookie must be stripped before forwarding, got %v", got)
	}
	if got["theme"] != "dark" {
		t.Errorf("non-ShinyHub app cookie must be preserved, got %v", got)
	}
}

// TestFilterReservedSetCookies_StripsBackendReservedCookies verifies that a
// backend response cannot set any cookie in ShinyHub's reserved namespaces
// (session/CSRF/oauth-state, sticky-routing, or the elastic client-id cookie).
// Without this a compromised app backend could emit
// Set-Cookie: shinyhub_cid_<slug>=<victim's value> to hijack another visitor's
// dedicated worker. Legitimate app cookies must pass through untouched.
func TestFilterReservedSetCookies_StripsBackendReservedCookies(t *testing.T) {
	resp := &http.Response{Header: http.Header{}}
	resp.Header.Add("Set-Cookie", clientCookiePrefix+"demo=VICTIMVALUE.deadbeefdeadbeef; Path=/app/demo/")
	resp.Header.Add("Set-Cookie", cookiePrefix+"demo=9; Path=/app/demo/")
	resp.Header.Add("Set-Cookie", "shiny_session=FORGED_JWT; Path=/")
	resp.Header.Add("Set-Cookie", "app_pref=blue; Path=/") // legitimate app cookie

	if err := filterReservedSetCookies(resp); err != nil {
		t.Fatalf("filterReservedSetCookies: %v", err)
	}

	got := resp.Header["Set-Cookie"]
	if len(got) != 1 || !strings.HasPrefix(got[0], "app_pref=") {
		t.Errorf("reserved backend Set-Cookie headers must be stripped, kept: %v", got)
	}
}

// TestStripInternalCookies_AllInternalRemovesHeader verifies the Cookie header
// is removed entirely when nothing remains after stripping.
func TestStripInternalCookies_AllInternalRemovesHeader(t *testing.T) {
	req := httptest.NewRequest("GET", "/app/demo/", nil)
	req.Header.Set("Cookie", "shiny_session=j; shinyhub_rep_demo=0")
	stripInternalCookies(req)
	if v := req.Header.Get("Cookie"); v != "" {
		t.Errorf("Cookie header should be removed when only internal cookies were present, got %q", v)
	}
}

// TestClearRoutingCookies_ExpiresRepAndCidCookies verifies logout expires both
// per-app proxy routing cookies (sticky replica + elastic client-id) at their
// original path, so a shared/kiosk browser does not route a subsequently
// logged-in user to the previous user's pinned replica or dedicated worker.
func TestClearRoutingCookies_ExpiresRepAndCidCookies(t *testing.T) {
	req := httptest.NewRequest("POST", "/api/auth/logout", nil)
	req.Header.Set("Cookie", clientCookiePrefix+"demo=abcdef0123456789abcdef0123456789.deadbeefdeadbeef; "+cookiePrefix+"demo=3; theme=dark")
	rec := httptest.NewRecorder()

	ClearRoutingCookies(rec, req)

	cleared := map[string]*http.Cookie{}
	for _, c := range rec.Result().Cookies() {
		cleared[c.Name] = c
	}
	for _, name := range []string{clientCookiePrefix + "demo", cookiePrefix + "demo"} {
		c, ok := cleared[name]
		if !ok {
			t.Errorf("routing cookie %q must be cleared on logout", name)
			continue
		}
		if c.MaxAge >= 0 {
			t.Errorf("cookie %q must be expired (MaxAge<0), got MaxAge=%d", name, c.MaxAge)
		}
		if c.Path != "/app/demo/" {
			t.Errorf("cleared cookie %q Path = %q, must match the original /app/demo/ so the browser clears it", name, c.Path)
		}
	}
	if _, ok := cleared["theme"]; ok {
		t.Error("non-routing cookie must not be touched")
	}
}

// TestApplyForwardingHeaders_UntrustedPeerOverwrites verifies a direct (untrusted)
// client cannot spoof the forwarding headers the app backend sees: client-supplied
// values are discarded and replaced with the proxy's own trusted view.
func TestApplyForwardingHeaders_UntrustedPeerOverwrites(t *testing.T) {
	req := httptest.NewRequest("GET", "/app/demo/", nil)
	req.Host = "shiny.example.com"
	req.RemoteAddr = "198.51.100.7:5000"
	req.Header.Set("X-Forwarded-Host", "evil.example.com")
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Real-IP", "10.0.0.1")
	req.Header.Set("X-Forwarded-For", "10.0.0.1")
	req.Header.Set("Forwarded", `for="10.0.0.1";host=evil.example.com;proto=https`)

	applyForwardingHeaders(req, "http", "198.51.100.7", false)

	if h := req.Header.Get("X-Forwarded-Host"); h != "shiny.example.com" {
		t.Errorf("X-Forwarded-Host = %q, want the proxy's host (spoof must be overwritten)", h)
	}
	if p := req.Header.Get("X-Forwarded-Proto"); p != "http" {
		t.Errorf("X-Forwarded-Proto = %q, want http (spoof must be overwritten)", p)
	}
	if ip := req.Header.Get("X-Real-IP"); ip != "198.51.100.7" {
		t.Errorf("X-Real-IP = %q, want the real peer IP", ip)
	}
	if xff := req.Header.Get("X-Forwarded-For"); strings.Contains(xff, "10.0.0.1") {
		t.Errorf("spoofed X-Forwarded-For must be cleared, got %q", xff)
	}
	if f := req.Header.Get("Forwarded"); strings.Contains(f, "evil.example.com") {
		t.Errorf("spoofed Forwarded must be overwritten, got %q", f)
	}
}

func TestStickyCookie_SignRoundTrips(t *testing.T) {
	key := []byte("sticky-test-key-0123456789abcdef")
	v := signStickyValue(key, "demo", 3, 42)
	if v == "3" {
		t.Fatalf("signed value should not be a bare index, got %q", v)
	}
	idx, depID, ok := verifyStickyValue(key, "demo", v)
	if !ok || idx != 3 || depID != 42 {
		t.Errorf("round-trip = (%d,%d,%v), want (3,42,true)", idx, depID, ok)
	}
}

// TestStickyCookie_RejectsForgery verifies a client cannot forge a sticky cookie
// to pin itself to a replica (and bypass the per-replica session cap): a bare
// integer, an old 2-part format, a wrong signature, and a valid cookie replayed
// for another app are all rejected.
func TestStickyCookie_RejectsForgery(t *testing.T) {
	key := []byte("sticky-test-key-0123456789abcdef")
	if _, _, ok := verifyStickyValue(key, "demo", "0"); ok {
		t.Error("bare integer (unsigned) sticky value must be rejected when signing is enabled")
	}
	// Old 2-part format "<idx>.<hmac>" is stale — must not verify.
	if _, _, ok := verifyStickyValue(key, "demo", "0.deadbeefdeadbeef"); ok {
		t.Error("old 2-part cookie must be rejected (stale format)")
	}
	// 3-part format with tampered signature must be rejected.
	if _, _, ok := verifyStickyValue(key, "demo", "0.0.deadbeefdeadbeef"); ok {
		t.Error("forged 3-part signature must be rejected")
	}
	v := signStickyValue(key, "demo", 0, 0)
	if _, _, ok := verifyStickyValue(key, "other", v); ok {
		t.Error("a cookie signed for one app must not verify for another (slug binding)")
	}
}

// TestServeHTTP_UntrustedClientSanitized is the end-to-end check that a direct
// (untrusted) client cannot spoof forwarding headers to the backend and that
// ShinyHub's session cookie never reaches the backend, exercised through the
// real ServeHTTP/Director path rather than the helpers in isolation.
func TestServeHTTP_UntrustedClientSanitized(t *testing.T) {
	var gotFwdHost, gotRealIP, gotCookie string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotFwdHost = r.Header.Get("X-Forwarded-Host")
		gotRealIP = r.Header.Get("X-Real-IP")
		gotCookie = r.Header.Get("Cookie")
	}))
	defer backend.Close()

	p := New()
	if err := p.Register("app", backend.URL); err != nil {
		t.Fatal(err)
	}
	// No trusted proxies configured: the client is a direct, untrusted peer.

	req := httptest.NewRequest("GET", "/app/app/", nil)
	req.RemoteAddr = "203.0.113.7:40000"
	req.Host = "shiny.example.com"
	req.Header.Set("X-Forwarded-Host", "evil.example.com")
	req.Header.Set("X-Real-IP", "192.0.2.99")
	req.Header.Set("Cookie", "shiny_session=THE_JWT; theme=dark")
	p.ServeHTTP(httptest.NewRecorder(), req)

	if gotFwdHost != "shiny.example.com" {
		t.Errorf("backend X-Forwarded-Host = %q, want shiny.example.com (spoof must be overwritten)", gotFwdHost)
	}
	if gotRealIP != "203.0.113.7" {
		t.Errorf("backend X-Real-IP = %q, want the real peer 203.0.113.7", gotRealIP)
	}
	if strings.Contains(gotCookie, "shiny_session") || strings.Contains(gotCookie, "THE_JWT") {
		t.Errorf("session cookie leaked to backend: %q", gotCookie)
	}
	if !strings.Contains(gotCookie, "theme=dark") {
		t.Errorf("non-ShinyHub cookie should be forwarded, got %q", gotCookie)
	}
}

// TestApplyForwardingHeaders_TrustedPeerPreserves verifies that when ShinyHub
// sits behind a trusted edge proxy (nginx/Caddy), the forwarding headers that
// proxy set are preserved (the edge proxy keeps authority).
func TestApplyForwardingHeaders_TrustedPeerPreserves(t *testing.T) {
	req := httptest.NewRequest("GET", "/app/demo/", nil)
	req.Host = "internal"
	req.RemoteAddr = "127.0.0.1:9000"
	req.Header.Set("X-Forwarded-Host", "shiny.example.com")
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Real-IP", "203.0.113.9")
	req.Header.Set("X-Forwarded-For", "203.0.113.9")

	applyForwardingHeaders(req, "http", "127.0.0.1", true)

	if h := req.Header.Get("X-Forwarded-Host"); h != "shiny.example.com" {
		t.Errorf("trusted edge proxy's X-Forwarded-Host must be preserved, got %q", h)
	}
	if p := req.Header.Get("X-Forwarded-Proto"); p != "https" {
		t.Errorf("trusted edge proxy's X-Forwarded-Proto must be preserved, got %q", p)
	}
	if ip := req.Header.Get("X-Real-IP"); ip != "203.0.113.9" {
		t.Errorf("trusted edge proxy's X-Real-IP must be preserved, got %q", ip)
	}
}

func TestNewBackendTransport_HasResponseHeaderTimeout(t *testing.T) {
	tr := newBackendTransport()
	if tr.ResponseHeaderTimeout <= 0 {
		t.Errorf("ResponseHeaderTimeout = %v, want > 0 (a hung app must not block the forwarding goroutine forever)", tr.ResponseHeaderTimeout)
	}
	// A single app is one host that may carry many concurrent Shiny sessions;
	// the net/http default of 2 idle conns per host causes connection churn.
	if tr.MaxIdleConnsPerHost <= 2 {
		t.Errorf("MaxIdleConnsPerHost = %d, want > 2", tr.MaxIdleConnsPerHost)
	}
	// Must be a distinct instance so we never mutate the process-wide default.
	if tr == http.DefaultTransport {
		t.Error("newBackendTransport returned the shared http.DefaultTransport; must be a clone")
	}
}
