package oauth

import (
	"context"
	"encoding/json"
	"fmt"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// OIDCProvider wraps an OpenID Connect provider for use with ShinyHub's
// auth flow. It is compatible with any OIDC-compliant IdP.
type OIDCProvider struct {
	verifier    *gooidc.IDTokenVerifier
	oauth2Cfg   oauth2.Config
	DisplayName string
	groupsClaim string
}

// OIDCUser holds the claims extracted from the ID token.
type OIDCUser struct {
	Sub                string
	Email              string
	Name               string
	Groups             []string
	GroupsClaimPresent bool
}

// NewOIDCProvider performs OIDC discovery against issuerURL and returns a
// configured provider. groupsClaim names the ID-token claim that carries group
// memberships (defaults to "groups" when empty). groupsScope is an optional
// additional OAuth2 scope to request (e.g. "groups"). Returns an error if
// discovery fails.
func NewOIDCProvider(ctx context.Context, issuerURL, clientID, clientSecret, callbackURL, displayName, groupsClaim, groupsScope string) (*OIDCProvider, error) {
	provider, err := gooidc.NewProvider(ctx, issuerURL)
	if err != nil {
		return nil, fmt.Errorf("oidc discover %s: %w", issuerURL, err)
	}
	scopes := []string{gooidc.ScopeOpenID, "email", "profile"}
	if groupsScope != "" {
		scopes = append(scopes, groupsScope)
	}
	if groupsClaim == "" {
		groupsClaim = "groups"
	}
	return &OIDCProvider{
		verifier: provider.Verifier(&gooidc.Config{ClientID: clientID}),
		oauth2Cfg: oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			RedirectURL:  callbackURL,
			Endpoint:     provider.Endpoint(),
			Scopes:       scopes,
		},
		DisplayName: displayName,
		groupsClaim: groupsClaim,
	}, nil
}

// AuthURL returns the authorization URL to redirect the browser to.
func (p *OIDCProvider) AuthURL(state string) string {
	return p.oauth2Cfg.AuthCodeURL(state, oauth2.AccessTypeOnline)
}

// Exchange trades an authorization code for tokens.
func (p *OIDCProvider) Exchange(ctx context.Context, code string) (*oauth2.Token, error) {
	tok, err := p.oauth2Cfg.Exchange(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("oidc token exchange: %w", err)
	}
	return tok, nil
}

// VerifyIDToken extracts and verifies the ID token, returning the user's claims.
func (p *OIDCProvider) VerifyIDToken(ctx context.Context, tok *oauth2.Token) (*OIDCUser, error) {
	rawIDToken, ok := tok.Extra("id_token").(string)
	if !ok {
		return nil, fmt.Errorf("oidc: no id_token in token response")
	}
	idToken, err := p.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return nil, fmt.Errorf("oidc verify id_token: %w", err)
	}
	var claims map[string]json.RawMessage
	if err := idToken.Claims(&claims); err != nil {
		return nil, fmt.Errorf("oidc extract claims: %w", err)
	}
	var sub, email, name string
	_ = json.Unmarshal(claims["sub"], &sub)
	_ = json.Unmarshal(claims["email"], &email)
	_ = json.Unmarshal(claims["name"], &name)
	if sub == "" {
		return nil, fmt.Errorf("oidc: id_token missing sub claim")
	}
	rawGroups := claims[p.groupsClaim]
	present := len(rawGroups) > 0 && string(rawGroups) != "null"
	return &OIDCUser{
		Sub:                sub,
		Email:              email,
		Name:               name,
		Groups:             decodeGroupsClaim(rawGroups),
		GroupsClaimPresent: present,
	}, nil
}

// decodeGroupsClaim normalizes an OIDC groups claim that may be a JSON array
// of strings or a single string. Returns an empty slice when absent or null.
func decodeGroupsClaim(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return []string{}
	}
	var arr []string
	if err := json.Unmarshal(raw, &arr); err == nil {
		return arr
	}
	var one string
	if err := json.Unmarshal(raw, &one); err == nil && one != "" {
		return []string{one}
	}
	return []string{}
}
