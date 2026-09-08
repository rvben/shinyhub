// Package trustedpublish verifies CI workload identities against operator-owned
// policies. A caller can select a policy, but cannot supply trust conditions.
package trustedpublish

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/rvben/shinyhub/internal/slug"
)

const GitHubIssuer = "https://token.actions.githubusercontent.com"

type Policy struct {
	Name     string            `yaml:"name" json:"name"`
	Issuer   string            `yaml:"issuer" json:"issuer"`
	JWKSURL  string            `yaml:"jwks_url" json:"jwks_url"`
	Audience string            `yaml:"audience" json:"audience"`
	Subject  string            `yaml:"subject" json:"subject"`
	Claims   map[string]string `yaml:"claims" json:"claims"`
	Apps     []string          `yaml:"apps" json:"apps"`
}

func Validate(policies []Policy) error {
	if len(policies) > 100 {
		return fmt.Errorf("auth.trusted_publishers supports at most 100 policies")
	}
	seen := map[string]bool{}
	for _, p := range policies {
		if !slug.Valid(p.Name) || seen[p.Name] {
			return fmt.Errorf("trusted publisher names must be unique app-style slugs")
		}
		seen[p.Name] = true
		for _, raw := range []string{p.Issuer, p.JWKSURL} {
			u, err := url.Parse(raw)
			if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
				return fmt.Errorf("trusted publisher %s requires HTTPS issuer and jwks_url without credentials, query, or fragment", p.Name)
			}
		}
		if strings.TrimSpace(p.Audience) == "" || strings.TrimSpace(p.Subject) == "" {
			return fmt.Errorf("trusted publisher %s requires an exact audience and subject", p.Name)
		}
		if len(p.Apps) == 0 {
			return fmt.Errorf("trusted publisher %s requires an apps allowlist", p.Name)
		}
		apps := map[string]bool{}
		for _, app := range p.Apps {
			if !slug.Valid(app) || apps[app] {
				return fmt.Errorf("trusted publisher %s has an invalid or duplicate app slug", p.Name)
			}
			apps[app] = true
		}
		for key, value := range p.Claims {
			if key == "" || value == "" {
				return fmt.Errorf("trusted publisher %s has an empty claim condition", p.Name)
			}
			switch key {
			case "iss", "sub", "aud", "exp", "iat", "nbf", "jti":
				return fmt.Errorf("trusted publisher %s: use dedicated fields for standard claims", p.Name)
			}
		}
		if p.Issuer == GitHubIssuer {
			if p.JWKSURL != GitHubIssuer+"/.well-known/jwks" {
				return fmt.Errorf("GitHub trusted publishers must use GitHub's JWKS endpoint")
			}
			for _, claim := range []string{"repository_id", "repository_owner_id", "ref", "workflow_ref"} {
				if p.Claims[claim] == "" {
					return fmt.Errorf("GitHub trusted publisher %s requires the %s claim", p.Name, claim)
				}
			}
		}
	}
	return nil
}

// Fingerprint binds issued credentials to the complete policy. Removing or
// changing a policy invalidates its credentials when the new config is loaded.
func (p Policy) Fingerprint() string {
	b, _ := json.Marshal(p)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
