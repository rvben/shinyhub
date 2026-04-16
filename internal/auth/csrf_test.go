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
	h := auth.CSRFMiddleware()(okHandler())
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
	h := auth.CSRFMiddleware()(okHandler())
	req := httptest.NewRequest("POST", "/api/apps", nil)
	req.Header.Set("Authorization", "Bearer abc")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200 for bearer, got %d", rr.Code)
	}
}

func TestCSRF_POSTMissingTokenRejected(t *testing.T) {
	h := auth.CSRFMiddleware()(okHandler())
	req := httptest.NewRequest("POST", "/api/apps", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "any"})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", rr.Code)
	}
}

func TestCSRF_POSTMatchingTokenAllowed(t *testing.T) {
	h := auth.CSRFMiddleware()(okHandler())
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

func TestCSRF_POSTMismatchedTokenRejected(t *testing.T) {
	h := auth.CSRFMiddleware()(okHandler())
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
