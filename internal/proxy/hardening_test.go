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
