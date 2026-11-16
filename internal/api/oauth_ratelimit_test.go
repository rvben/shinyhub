package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestOAuthCallbacksRateLimited guards that the OAuth/OIDC callback endpoints
// are rate-limited per IP (the login-start endpoints already are; the callbacks
// were not, leaving them open to flooding with bogus state/code).
func TestOAuthCallbacksRateLimited(t *testing.T) {
	for _, path := range []string{
		"/api/auth/github/callback?code=x&state=y",
		"/api/auth/google/callback?code=x&state=y",
		"/api/auth/oidc/callback?code=x&state=y",
	} {
		srv, _ := newTestServer(t) // fresh limiter state per endpoint
		limit := 20
		if strings.HasPrefix(path, "/api/auth/oidc/") {
			limit = 600
		}
		var got429 bool
		for i := 0; i < limit+1; i++ {
			rec := httptest.NewRecorder()
			srv.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			if rec.Code == http.StatusTooManyRequests {
				got429 = true
				break
			}
		}
		if !got429 {
			t.Errorf("%s: expected a 429 after %d requests (callback must be rate-limited)", path, limit)
		}
	}
}
