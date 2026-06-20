package proxy_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvben/shinyhub/internal/proxy"
)

// TestStickyCookie_SecureFlag proves the routing cookie carries Secure over
// HTTPS (so it cannot be intercepted in cleartext) but not over plain HTTP
// (where a Secure cookie would be silently dropped by the browser, breaking
// sticky routing). It mirrors the session cookie's scheme-aware policy.
func TestStickyCookie_SecureFlag(t *testing.T) {
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}))
	defer b.Close()

	p := proxy.New()
	p.SetPoolSize("demo", 1)
	if err := p.RegisterReplica("demo", 0, b.URL, nil, 0); err != nil {
		t.Fatalf("register: %v", err)
	}

	stickyCookie := func(rec *httptest.ResponseRecorder) *http.Cookie {
		for _, c := range rec.Result().Cookies() {
			if c.Name == "shinyhub_rep_demo" {
				return c
			}
		}
		return nil
	}

	// Direct HTTPS connection: cookie must be Secure + HttpOnly.
	httpsReq := httptest.NewRequest(http.MethodGet, "https://host/app/demo/", nil)
	httpsRec := httptest.NewRecorder()
	p.ServeHTTP(httpsRec, httpsReq)
	c := stickyCookie(httpsRec)
	if c == nil {
		t.Fatal("expected a sticky cookie on the first HTTPS request")
	}
	if !c.Secure {
		t.Error("sticky cookie missing Secure over HTTPS; it could be intercepted in cleartext")
	}
	if !c.HttpOnly {
		t.Error("sticky cookie missing HttpOnly")
	}

	// Plain HTTP: Secure must be off or the browser drops the cookie.
	httpReq := httptest.NewRequest(http.MethodGet, "http://host/app/demo/", nil)
	httpRec := httptest.NewRecorder()
	p.ServeHTTP(httpRec, httpReq)
	if c := stickyCookie(httpRec); c != nil && c.Secure {
		t.Error("sticky cookie set Secure over plain HTTP; the browser would drop it, breaking sticky routing")
	}
}
