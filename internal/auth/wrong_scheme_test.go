package auth_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/auth"
)

// wrongSchemeHandler is BearerMiddleware in front of a handler that must never
// run in any of these tests.
func wrongSchemeHandler(t *testing.T, secret string, keyLookup auth.APIKeyLookup) http.Handler {
	t.Helper()
	return auth.BearerMiddleware(secret, keyLookup, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("authentication should not have succeeded")
		w.WriteHeader(http.StatusOK)
	}))
}

func sendAuthorization(t *testing.T, h http.Handler, header string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/apps", nil)
	req.Header.Set("Authorization", header)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rec.Code, rec.Body.String())
	}
	return rec
}

// An API key sent under Bearer is the mistake a hand-written client makes,
// because Bearer is the convention everywhere else. ShinyHub accepts an API key
// only under Token, and the rejection used to be the same bare 401 as a wrong
// password: nothing in the response said which of the two things was wrong.
//
// The scheme is now named in WWW-Authenticate, the header RFC 7235 defines for
// it, so `curl -i` shows the answer with no change to the body.
func TestAPIKeyUnderBearerNamesTheTokenScheme(t *testing.T) {
	const rawKey = "shk_realkey123"
	keyLookup := func(hash string) (*auth.ContextUser, *auth.CredentialInfo, error) {
		if hash == auth.HashAPIKey(rawKey) {
			return &auth.ContextUser{ID: 9, Username: "bot", Role: "developer"}, &auth.CredentialInfo{Type: "api_key"}, nil
		}
		return nil, nil, fmt.Errorf("not found")
	}
	h := wrongSchemeHandler(t, "test-secret", keyLookup)

	rec := sendAuthorization(t, h, "Bearer "+rawKey)

	challenge := rec.Header().Get("WWW-Authenticate")
	if !strings.HasPrefix(challenge, "Token ") {
		t.Fatalf("expected the challenge to name the Token scheme, got %q", challenge)
	}
	if !strings.Contains(challenge, `error="invalid_request"`) {
		t.Errorf("expected an invalid_request challenge, got %q", challenge)
	}
	if rec.Body.String() != "unauthorized\n" {
		t.Errorf("the response body must not change, got %q", rec.Body.String())
	}
}

// The mirror mistake: a session JWT sent under Token. The CLI picks the scheme
// structurally so it never lands here, but a script that copies a JWT out of
// `POST /api/auth/login` and reaches for the Token scheme does.
func TestSessionJWTUnderTokenNamesTheBearerScheme(t *testing.T) {
	const secret = "test-secret"
	token, err := auth.IssueJWT(1, "alice", "admin", secret)
	if err != nil {
		t.Fatal(err)
	}
	h := wrongSchemeHandler(t, secret, nil)

	rec := sendAuthorization(t, h, "Token "+token)

	challenge := rec.Header().Get("WWW-Authenticate")
	if !strings.HasPrefix(challenge, "Bearer ") {
		t.Fatalf("expected the challenge to name the Bearer scheme, got %q", challenge)
	}
}

// The challenge must never become a validity oracle. It is decided from the
// SHAPE of the string the caller sent and nothing else, so a real API key and a
// string that was never a credential produce byte-identical answers under
// Bearer - the lookup is not even consulted.
func TestWrongSchemeChallengeSaysNothingAboutValidity(t *testing.T) {
	const rawKey = "shk_realkey123"
	var lookups int
	keyLookup := func(hash string) (*auth.ContextUser, *auth.CredentialInfo, error) {
		lookups++
		if hash == auth.HashAPIKey(rawKey) {
			return &auth.ContextUser{ID: 9, Username: "bot", Role: "developer"}, &auth.CredentialInfo{Type: "api_key"}, nil
		}
		return nil, nil, fmt.Errorf("not found")
	}
	h := wrongSchemeHandler(t, "test-secret", keyLookup)

	real := sendAuthorization(t, h, "Bearer "+rawKey)
	fake := sendAuthorization(t, h, "Bearer shk_neverissued999")

	if got, want := real.Header().Get("WWW-Authenticate"), fake.Header().Get("WWW-Authenticate"); got != want {
		t.Errorf("a real key and an unissued one must answer identically:\n real: %q\n fake: %q", got, want)
	}
	if real.Body.String() != fake.Body.String() {
		t.Errorf("bodies differ: %q vs %q", real.Body.String(), fake.Body.String())
	}
	if lookups != 0 {
		t.Errorf("the API key lookup ran %d times for a Bearer request; a wrong scheme must be refused on shape alone", lookups)
	}
}

// The negative control that keeps the header meaningful: a credential sent
// under the RIGHT scheme and rejected on its merits gets no challenge at all.
// Without this, the header would appear on every 401 and would tell a caller
// nothing.
func TestCorrectSchemeRejectionCarriesNoChallenge(t *testing.T) {
	const secret = "test-secret"
	foreign, err := auth.IssueJWT(1, "alice", "admin", "a-different-servers-secret")
	if err != nil {
		t.Fatal(err)
	}
	keyLookup := func(string) (*auth.ContextUser, *auth.CredentialInfo, error) {
		return nil, nil, fmt.Errorf("not found")
	}
	h := wrongSchemeHandler(t, secret, keyLookup)

	for _, tc := range []struct {
		name   string
		header string
	}{
		{"a JWT this server did not sign, under Bearer", "Bearer " + foreign},
		{"an API key that was never issued, under Token", "Token shk_neverissued999"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := sendAuthorization(t, h, tc.header)
			if challenge := rec.Header().Get("WWW-Authenticate"); challenge != "" {
				t.Fatalf("expected no challenge for a wrong credential under the right scheme, got %q", challenge)
			}
		})
	}
}

// LooksLikeJWT is what decides the scheme, so its edges are the fix's edges.
func TestLooksLikeJWT(t *testing.T) {
	valid, err := auth.IssueJWT(1, "alice", "admin", "test-secret")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		token string
		want  bool
	}{
		{valid, true},
		{"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.sig", true},
		{"shk_abc123", false},
		{"", false},
		// An opaque deploy token: `openssl rand -hex 32`, no prefix at all.
		{"4f8c1b0d9e7a6c5b4a3928170f6e5d4c3b2a19080706050403020100ffeeddcc", false},
		{"550e8400-e29b-41d4-a716-446655440000", false},
		// Three segments but not a JWT header.
		{"abc.def.ghi", false},
		// Empty segments never appear in a compact JWS.
		{"eyJhbGciOiJIUzI1NiJ9..sig", false},
		{"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.", false},
		// Two segments, and four.
		{"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0", false},
		{"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.sig.extra", false},
	} {
		if got := auth.LooksLikeJWT(tc.token); got != tc.want {
			t.Errorf("LooksLikeJWT(%q) = %v, want %v", tc.token, got, tc.want)
		}
	}
}
