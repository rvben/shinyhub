package oauth

import (
	"context"
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
}

// OIDCUser holds the claims extracted from the ID token.
type OIDCUser struct {
	Sub   string
	Email string
	Name  string
}

// NewOIDCProvider performs OIDC discovery against issuerURL and returns a
// configured provider. Returns an error if discovery fails.
func NewOIDCProvider(ctx context.Context, issuerURL, clientID, clientSecret, callbackURL, displayName string) (*OIDCProvider, error) {
	provider, err := gooidc.NewProvider(ctx, issuerURL)
	if err != nil {
		return nil, fmt.Errorf("oidc discover %s: %w", issuerURL, err)
	}
	return &OIDCProvider{
		verifier: provider.Verifier(&gooidc.Config{ClientID: clientID}),
		oauth2Cfg: oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			RedirectURL:  callbackURL,
			Endpoint:     provider.Endpoint(),
			Scopes:       []string{gooidc.ScopeOpenID, "email", "profile"},
		},
		DisplayName: displayName,
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
	var claims struct {
		Sub   string `json:"sub"`
		Email string `json:"email"`
		Name  string `json:"name"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return nil, fmt.Errorf("oidc extract claims: %w", err)
	}
	if claims.Sub == "" {
		return nil, fmt.Errorf("oidc: id_token missing sub claim")
	}
	return &OIDCUser{Sub: claims.Sub, Email: claims.Email, Name: claims.Name}, nil
}
