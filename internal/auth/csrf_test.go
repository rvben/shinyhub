package auth_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/auth"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func TestCSRF_GETAlwaysAllowed(t *testing.T) {
	h := auth.CSRFMiddleware(nil)(okHandler())
	req := httptest.NewRequest("GET", "/api/apps", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "any"})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
	if !strings.Contains(rr.Header().Get("Set-Cookie"), "csrf_token=") {
		t.Fatal("expected csrf_token cookie on GET response")
	}
}

func TestCSRF_POSTWithBearerBypasses(t *testing.T) {
	h := auth.CSRFMiddleware(nil)(okHandler())
	req := httptest.NewRequest("POST", "/api/apps", nil)
	req.Header.Set("Authorization", "Bearer abc")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200 for bearer, got %d", rr.Code)
	}
}

func TestCSRF_POSTMissingTokenRejected(t *testing.T) {
	h := auth.CSRFMiddleware(nil)(okHandler())
	req := httptest.NewRequest("POST", "/api/apps", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "any"})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", rr.Code)
	}
}

func TestCSRF_POSTMatchingTokenAllowed(t *testing.T) {
	h := auth.CSRFMiddleware(nil)(okHandler())
	req := httptest.NewRequest("POST", "/api/apps", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "any"})
	req.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: "matching-token"})
	req.Header.Set("X-CSRF-Token", "matching-token")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
}

func TestCSRF_POSTFromProxiedAppRefererRejected(t *testing.T) {
	// A malicious/compromised proxied app served same-origin at /app/<slug>/ could
	// read the JS-readable csrf_token cookie and ride the session. Reject any
	// mutating request whose Referer is under /app/ even when the token matches.
	h := auth.CSRFMiddleware(nil)(okHandler())
	req := httptest.NewRequest("POST", "/api/apps", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "any"})
	req.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: "matching-token"})
	req.Header.Set("X-CSRF-Token", "matching-token")
	req.Header.Set("Referer", "https://hub.example.com/app/evil/page")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("want 403 for /app/-refered mutation, got %d", rr.Code)
	}
}

func TestCSRF_POSTFromDashboardRefererAllowed(t *testing.T) {
	// The dashboard's own fetches carry a dashboard-route Referer, never /app/.
	h := auth.CSRFMiddleware(nil)(okHandler())
	req := httptest.NewRequest("POST", "/api/apps", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "any"})
	req.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: "matching-token"})
	req.Header.Set("X-CSRF-Token", "matching-token")
	req.Header.Set("Referer", "https://hub.example.com/apps/demo/overview")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200 for dashboard-refered mutation, got %d", rr.Code)
	}
}

func TestCSRF_POSTNoRefererAllowed(t *testing.T) {
	// A missing Referer (privacy tooling, no-referrer policy) must not break the
	// dashboard; the token check still applies.
	h := auth.CSRFMiddleware(nil)(okHandler())
	req := httptest.NewRequest("POST", "/api/apps", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "any"})
	req.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: "matching-token"})
	req.Header.Set("X-CSRF-Token", "matching-token")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200 for no-referer mutation, got %d", rr.Code)
	}
}

func TestCSRF_POSTMismatchedTokenRejected(t *testing.T) {
	h := auth.CSRFMiddleware(nil)(okHandler())
	req := httptest.NewRequest("POST", "/api/apps", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "any"})
	req.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: "cookie-token"})
	req.Header.Set("X-CSRF-Token", "different-token")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", rr.Code)
	}
}

func TestCSRF_GETForwardAuthUserMintsToken(t *testing.T) {
	h := auth.CSRFMiddleware(nil)(okHandler())
	req := httptest.NewRequest("GET", "/api/apps", nil)
	// Forward-auth user on context, NO session cookie.
	req = req.WithContext(auth.WithUser(req.Context(), &auth.ContextUser{ID: 7, Username: "fa-user", Role: "developer"}))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
	if !strings.Contains(rr.Header().Get("Set-Cookie"), "csrf_token=") {
		t.Fatal("expected csrf_token cookie minted for forward-auth user on GET")
	}
}

func TestCSRF_POSTForwardAuthUserMissingTokenRejected(t *testing.T) {
	// We mint a token rather than bypass: a forward-auth POST with no csrf cookie
	// is still rejected. This pins the secure choice (no blanket bypass).
	h := auth.CSRFMiddleware(nil)(okHandler())
	req := httptest.NewRequest("POST", "/api/apps", nil)
	req = req.WithContext(auth.WithUser(req.Context(), &auth.ContextUser{ID: 7, Username: "fa-user", Role: "developer"}))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("forward-auth POST without csrf token must be 403, got %d", rr.Code)
	}
}

func TestCSRF_POSTForwardAuthUserMatchingTokenAllowed(t *testing.T) {
	h := auth.CSRFMiddleware(nil)(okHandler())
	req := httptest.NewRequest("POST", "/api/apps", nil)
	req = req.WithContext(auth.WithUser(req.Context(), &auth.ContextUser{ID: 7, Username: "fa-user", Role: "developer"}))
	req.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: "matching-token"})
	req.Header.Set("X-CSRF-Token", "matching-token")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("forward-auth POST with matching double-submit token must be 200, got %d", rr.Code)
	}
}
