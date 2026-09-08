package trustedpublish

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

const CredentialLifetime = 10 * time.Minute

var ErrIdentity = errors.New("workload identity does not satisfy the trusted publisher policy")

type Identity struct{ ReplayID string }

type Verifier struct {
	mu     sync.Mutex
	keys   map[string]oidc.KeySet
	client *http.Client
}

func NewVerifier(client *http.Client) *Verifier {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	return &Verifier{keys: map[string]oidc.KeySet{}, client: client}
}

func (v *Verifier) Verify(ctx context.Context, p Policy, raw string) (Identity, error) {
	if len(raw) == 0 || len(raw) > 32<<10 {
		return Identity{}, ErrIdentity
	}
	v.mu.Lock()
	keys := v.keys[p.JWKSURL]
	if keys == nil {
		keys = oidc.NewRemoteKeySet(oidc.ClientContext(context.Background(), v.client), p.JWKSURL)
		v.keys[p.JWKSURL] = keys
	}
	v.mu.Unlock()
	token, err := oidc.NewVerifier(p.Issuer, keys, &oidc.Config{ClientID: p.Audience, SupportedSigningAlgs: []string{"RS256", "ES256"}}).Verify(ctx, raw)
	if err != nil {
		return Identity{}, ErrIdentity
	}
	var claims map[string]json.RawMessage
	if err := token.Claims(&claims); err != nil {
		return Identity{}, ErrIdentity
	}
	var jti string
	var issuedAt, notBefore int64
	if json.Unmarshal(claims["jti"], &jti) != nil || jti == "" || json.Unmarshal(claims["iat"], &issuedAt) != nil {
		return Identity{}, ErrIdentity
	}
	now := time.Now()
	if issuedAt <= 0 || time.Unix(issuedAt, 0).After(now.Add(30*time.Second)) || now.Sub(time.Unix(issuedAt, 0)) > 10*time.Minute || !token.Expiry.After(now) {
		return Identity{}, ErrIdentity
	}
	if rawNBF, exists := claims["nbf"]; exists {
		if json.Unmarshal(rawNBF, &notBefore) != nil || time.Unix(notBefore, 0).After(now.Add(30*time.Second)) {
			return Identity{}, ErrIdentity
		}
	}
	if token.Subject != p.Subject {
		return Identity{}, ErrIdentity
	}
	// A pull_request_target job executes with base-repository identity. Never
	// accept pull-request events even if ref/workflow claims match production.
	if p.Issuer == GitHubIssuer {
		var event string
		if json.Unmarshal(claims["event_name"], &event) != nil || (event != "push" && event != "workflow_dispatch" && event != "schedule" && event != "workflow_call") {
			return Identity{}, ErrIdentity
		}
	}
	for name, want := range p.Claims {
		var got string
		if json.Unmarshal(claims[name], &got) != nil || got != want {
			return Identity{}, ErrIdentity
		}
	}
	h := sha256.Sum256([]byte(p.Issuer + "\x00" + jti))
	return Identity{ReplayID: hex.EncodeToString(h[:])}, nil
}
