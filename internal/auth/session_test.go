package auth_test

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvben/shinyhub/internal/auth"
)

func cidrs(t *testing.T, in ...string) []*net.IPNet {
	t.Helper()
	out := make([]*net.IPNet, 0, len(in))
	for _, c := range in {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, n)
	}
	return out
}

func setCookie(t *testing.T, rec *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("cookie %q not set; got %+v", name, rec.Result().Cookies())
	return nil
}

// X-Forwarded-Proto: https from an UNTRUSTED direct peer must NOT cause the
// session cookie to be marked Secure. If we trusted the header from anyone,
// an attacker connecting directly over plain HTTP could spoof "https" and
// make us mint a Secure cookie that the browser then silently drops on the
// non-HTTPS origin — breaking session establishment for the legitimate user.
func TestSetSessionCookie_XForwardedProtoIgnoredFromUntrustedPeer(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "203.0.113.7:44444" // public peer, not trusted
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()

	auth.SetSessionCookie(rec, req, "tok", cidrs(t, "127.0.0.0/8"))

	c := setCookie(t, rec, auth.SessionCookieName)
	if c.Secure {
		t.Errorf("untrusted peer XFP=https must NOT mark cookie Secure on a plain-HTTP request")
	}
}

// Mirror image: the same header from a peer in the trusted CIDRs IS honoured
// — that's the production-default path where the reverse proxy terminates
// TLS and forwards over HTTP to the local socket.
func TestSetSessionCookie_XForwardedProtoHonoredFromTrustedProxy(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()

	auth.SetSessionCookie(rec, req, "tok", cidrs(t, "127.0.0.0/8"))

	c := setCookie(t, rec, auth.SessionCookieName)
	if !c.Secure {
		t.Errorf("trusted proxy XFP=https must mark cookie Secure")
	}
}

// Same trust gate must apply to the OAuth state cookie — it carries no
// secret, but a Secure flag set on a plain-HTTP origin would silently drop
// the cookie and break the OAuth flow.
func TestSetOAuthStateCookie_XForwardedProtoIgnoredFromUntrustedPeer(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/auth/github/login", nil)
	req.RemoteAddr = "203.0.113.7:44444"
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()

	auth.SetOAuthStateCookie(rec, req, "state-nonce", cidrs(t, "127.0.0.0/8"))

	c := setCookie(t, rec, auth.OAuthStateCookieName)
	if c.Secure {
		t.Errorf("untrusted peer XFP=https must NOT mark OAuth state cookie Secure")
	}
}

// Same trust gate for the CSRF cookie minted by CSRFMiddleware on safe
// methods. The middleware is constructed once with the trusted CIDRs, so
// the test assertion mirrors the SetSessionCookie pair.
func TestCSRFMiddleware_MintedCookie_HonorsTrustedProxyForSecureFlag(t *testing.T) {
	t.Run("untrusted peer XFP=https stays not Secure", func(t *testing.T) {
		mw := auth.CSRFMiddleware(cidrs(t, "127.0.0.0/8"))
		h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
		req := httptest.NewRequest("GET", "/api/apps", nil)
		req.RemoteAddr = "203.0.113.7:44444"
		req.Header.Set("X-Forwarded-Proto", "https")
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "tok"})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		c := setCookie(t, rec, auth.CSRFCookieName)
		if c.Secure {
			t.Errorf("untrusted peer XFP=https must NOT mark CSRF cookie Secure")
		}
	})

	t.Run("trusted peer XFP=https is honoured", func(t *testing.T) {
		mw := auth.CSRFMiddleware(cidrs(t, "127.0.0.0/8"))
		h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
		req := httptest.NewRequest("GET", "/api/apps", nil)
		req.RemoteAddr = "127.0.0.1:54321"
		req.Header.Set("X-Forwarded-Proto", "https")
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "tok"})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		c := setCookie(t, rec, auth.CSRFCookieName)
		if !c.Secure {
			t.Errorf("trusted peer XFP=https must mark CSRF cookie Secure")
		}
	})
}
