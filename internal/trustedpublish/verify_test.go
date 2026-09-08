package trustedpublish

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/golang-jwt/jwt/v5"
)

func TestSignedWorkloadIdentity(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	issuer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "test", Algorithm: "RS256", Use: "sig"}}})
	}))
	defer issuer.Close()
	p := Policy{Name: "test", Issuer: issuer.URL, JWKSURL: issuer.URL + "/keys", Audience: "https://hub.example", Subject: "repo:example/app:ref:refs/heads/main", Claims: map[string]string{"repository_id": "123"}, Apps: []string{"app"}}
	v := NewVerifier(issuer.Client())
	base := func() jwt.MapClaims {
		return jwt.MapClaims{"iss": p.Issuer, "aud": p.Audience, "sub": p.Subject, "iat": time.Now().Unix(), "exp": time.Now().Add(time.Minute).Unix(), "jti": "one", "repository_id": "123"}
	}
	tests := []struct {
		name   string
		mutate func(jwt.MapClaims)
		valid  bool
	}{
		{"valid", func(jwt.MapClaims) {}, true},
		{"issuer", func(c jwt.MapClaims) { c["iss"] = "https://attacker.example" }, false},
		{"audience", func(c jwt.MapClaims) { c["aud"] = "elsewhere" }, false},
		{"subject", func(c jwt.MapClaims) { c["sub"] = "repo:example/app:pull_request" }, false},
		{"repository", func(c jwt.MapClaims) { c["repository_id"] = "124" }, false},
		{"typed claim", func(c jwt.MapClaims) { c["repository_id"] = 123 }, false},
		{"missing claim", func(c jwt.MapClaims) { delete(c, "repository_id") }, false},
		{"expired", func(c jwt.MapClaims) { c["exp"] = time.Now().Add(-time.Minute).Unix() }, false},
		{"no expiry", func(c jwt.MapClaims) { delete(c, "exp") }, false},
		{"no issued time", func(c jwt.MapClaims) { delete(c, "iat") }, false},
		{"future", func(c jwt.MapClaims) { c["iat"] = time.Now().Add(time.Minute).Unix() }, false},
		{"not yet valid", func(c jwt.MapClaims) { c["nbf"] = time.Now().Add(time.Minute).Unix() }, false},
		{"too old", func(c jwt.MapClaims) { c["iat"] = time.Now().Add(-11 * time.Minute).Unix() }, false},
		{"missing id", func(c jwt.MapClaims) { delete(c, "jti") }, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			claims := base()
			tc.mutate(claims)
			token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
			token.Header["kid"] = "test"
			raw, err := token.SignedString(key)
			if err != nil {
				t.Fatal(err)
			}
			identity, err := v.Verify(context.Background(), p, raw)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, err)
			}
			if tc.valid && len(identity.ReplayID) != 64 {
				t.Fatal("missing replay hash")
			}
		})
	}
	badKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, base())
	token.Header["kid"] = "test"
	raw, _ := token.SignedString(badKey)
	if _, err := v.Verify(context.Background(), p, raw); err == nil {
		t.Fatal("accepted invalid signature")
	}
	token = jwt.NewWithClaims(jwt.SigningMethodHS256, base())
	raw, _ = token.SignedString([]byte("test-secret"))
	if _, err := v.Verify(context.Background(), p, raw); err == nil {
		t.Fatal("accepted symmetric algorithm")
	}
}

func TestPolicyValidationAndFingerprint(t *testing.T) {
	p := Policy{Name: "production", Issuer: GitHubIssuer, JWKSURL: GitHubIssuer + "/.well-known/jwks", Audience: "https://hub.example", Subject: "repo:example/app:ref:refs/heads/main", Claims: map[string]string{"repository_id": "123", "repository_owner_id": "456", "ref": "refs/heads/main", "workflow_ref": "example/app/.github/workflows/deploy.yml@refs/heads/main"}, Apps: []string{"app"}}
	if err := Validate([]Policy{p}); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"repository_id", "repository_owner_id", "ref", "workflow_ref"} {
		copy := p
		copy.Claims = map[string]string{}
		for k, v := range p.Claims {
			copy.Claims[k] = v
		}
		delete(copy.Claims, field)
		if Validate([]Policy{copy}) == nil {
			t.Errorf("accepted missing %s", field)
		}
	}
	copy := p
	copy.Apps = nil
	if Validate([]Policy{copy}) == nil {
		t.Fatal("accepted unrestricted scope")
	}
	copy = p
	copy.JWKSURL = "http://example.com/keys"
	if Validate([]Policy{copy}) == nil {
		t.Fatal("accepted insecure keys")
	}
	copy = p
	copy.Apps = []string{"other"}
	if p.Fingerprint() == copy.Fingerprint() {
		t.Fatal("scope does not bind fingerprint")
	}
	if Validate([]Policy{p, p}) == nil {
		t.Fatal("accepted duplicate policy")
	}
}
