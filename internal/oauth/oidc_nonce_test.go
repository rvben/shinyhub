package oauth_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/rvben/shinyhub/internal/oauth"
)

// newMockIdPForNonceTest starts a minimal OIDC IdP (discovery + JWKS + token
// endpoint) that signs an RS256 ID token carrying idClaims. idClaims is held by
// reference: mutate it (e.g. to set "nonce") between generating an auth request
// and calling Exchange, mirroring how a real IdP echoes back the nonce it
// received on the authorization request.
func newMockIdPForNonceTest(t *testing.T, clientID string, idClaims map[string]any) *httptest.Server {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	const kid = "test-key-1"
	b64 := base64.RawURLEncoding.EncodeToString

	mux := http.NewServeMux()
	var srv *httptest.Server

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                srv.URL,
			"authorization_endpoint":                srv.URL + "/authorize",
			"token_endpoint":                        srv.URL + "/token",
			"jwks_uri":                              srv.URL + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		pub := key.Public().(*rsa.PublicKey)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]any{{
			"kty": "RSA", "alg": "RS256", "use": "sig", "kid": kid,
			"n": b64(pub.N.Bytes()),
			"e": b64(big.NewInt(int64(pub.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		claims := jwt.MapClaims{
			"iss": srv.URL,
			"aud": clientID,
			"iat": time.Now().Unix(),
			"exp": time.Now().Add(time.Hour).Unix(),
		}
		for k, v := range idClaims {
			claims[k] = v
		}
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
		tok.Header["kid"] = kid
		signed, err := tok.SignedString(key)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "mock-access-token",
			"token_type":   "Bearer",
			"id_token":     signed,
		})
	})

	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestOIDCProvider_AuthURL_IncludesNonce proves the authorization URL carries a
// nonce query parameter distinct from state, so a real IdP has something to
// echo back into the ID token.
func TestOIDCProvider_AuthURL_IncludesNonce(t *testing.T) {
	idp := newMockIdPForNonceTest(t, "client", map[string]any{})
	p, err := oauth.NewOIDCProvider(context.Background(), idp.URL, "client", "secret",
		"http://app.example.test/callback", "SSO", "", "")
	if err != nil {
		t.Fatalf("NewOIDCProvider: %v", err)
	}

	raw := p.AuthURL("the-state", "the-nonce")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse AuthURL: %v", err)
	}
	if got := u.Query().Get("state"); got != "the-state" {
		t.Errorf("state = %q, want %q", got, "the-state")
	}
	if got := u.Query().Get("nonce"); got != "the-nonce" {
		t.Errorf("nonce = %q, want %q (AuthURL must forward the nonce to the IdP)", got, "the-nonce")
	}
}

// TestOIDCProvider_VerifyIDToken_NonceMismatchRejected is the core SEC-M1
// regression guard: an ID token whose nonce claim doesn't match what the
// caller expected must be rejected. Without this check, an attacker who can
// obtain a validly-signed ID token from the IdP for a DIFFERENT authorization
// request could inject it into a victim's callback (ID-token replay /
// substitution) since only signature + aud/iss were checked before.
func TestOIDCProvider_VerifyIDToken_NonceMismatchRejected(t *testing.T) {
	const clientID = "client"
	idClaims := map[string]any{
		"sub":   "user-1",
		"email": "user1@example.com",
		"nonce": "nonce-for-request-A",
	}
	idp := newMockIdPForNonceTest(t, clientID, idClaims)
	p, err := oauth.NewOIDCProvider(context.Background(), idp.URL, clientID, "secret",
		"http://app.example.test/callback", "SSO", "", "")
	if err != nil {
		t.Fatalf("NewOIDCProvider: %v", err)
	}

	tok, err := p.Exchange(context.Background(), "mock-code")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}

	// The token's nonce ("nonce-for-request-A") does not match what this
	// caller expected for its own request ("nonce-for-request-B") - as would
	// be the case if an attacker replayed a token minted for a different
	// authorization request.
	if _, err := p.VerifyIDToken(context.Background(), tok, "nonce-for-request-B"); err == nil {
		t.Fatal("VerifyIDToken accepted an ID token with a mismatched nonce")
	}

	// The matching nonce must still be accepted.
	if _, err := p.VerifyIDToken(context.Background(), tok, "nonce-for-request-A"); err != nil {
		t.Errorf("VerifyIDToken rejected an ID token with the correct nonce: %v", err)
	}
}

// TestOIDCProvider_VerifyIDToken_MissingNonceRejectedWhenExpected proves that
// an IdP response with NO nonce claim at all (e.g. a token minted before the
// nonce was requested, or one stripped by an attacker) is rejected when the
// caller expects a specific nonce - a missing claim must not be treated as "no
// check needed".
func TestOIDCProvider_VerifyIDToken_MissingNonceRejectedWhenExpected(t *testing.T) {
	const clientID = "client"
	idClaims := map[string]any{
		"sub":   "user-1",
		"email": "user1@example.com",
		// no "nonce" claim
	}
	idp := newMockIdPForNonceTest(t, clientID, idClaims)
	p, err := oauth.NewOIDCProvider(context.Background(), idp.URL, clientID, "secret",
		"http://app.example.test/callback", "SSO", "", "")
	if err != nil {
		t.Fatalf("NewOIDCProvider: %v", err)
	}
	tok, err := p.Exchange(context.Background(), "mock-code")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if _, err := p.VerifyIDToken(context.Background(), tok, "expected-nonce"); err == nil {
		t.Fatal("VerifyIDToken accepted an ID token with no nonce claim while a nonce was expected")
	}
}
