package auth_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvben/shinyhub/internal/auth"
)

// TestAuthenticateRequest_InvalidHeaderDoesNotFallThroughToCookie locks the
// invariant that makes the "CSRF skips when an Authorization header is present"
// behavior safe: when an Authorization header is present it is authenticated on
// its own and a failure is NOT retried against the session cookie. Without this,
// a cross-site request could attach a garbage Authorization header to skip CSRF
// and still be authenticated by the ambient session cookie.
func TestAuthenticateRequest_InvalidHeaderDoesNotFallThroughToCookie(t *testing.T) {
	const secret = "test-secret"
	tok, err := auth.IssueJWT(1, "alice", "developer", secret)
	if err != nil {
		t.Fatal(err)
	}
	userLookup := func(int64) (*auth.ContextUser, error) {
		return &auth.ContextUser{ID: 1, Username: "alice", Role: "developer"}, nil
	}
	revoked := func(string) (bool, error) { return false, nil }

	// Garbage Authorization header + a valid session cookie: must NOT authenticate.
	bad := httptest.NewRequest("POST", "/api/apps/x/stop", nil)
	bad.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: tok})
	bad.Header.Set("Authorization", "garbage-no-scheme")
	if u, _, err := auth.AuthenticateRequest(bad, secret, nil, userLookup, revoked); err == nil && u != nil {
		t.Fatal("present-but-invalid Authorization must not fall through to cookie auth (CSRF-bypass guard)")
	}

	// Sanity: the same cookie with no Authorization header authenticates.
	good := httptest.NewRequest("POST", "/api/apps/x/stop", nil)
	good.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: tok})
	if u, _, err := auth.AuthenticateRequest(good, secret, nil, userLookup, revoked); err != nil || u == nil {
		t.Fatalf("cookie-only request should authenticate: user=%v err=%v", u, err)
	}
}
