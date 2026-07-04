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
	// GroupsClaimMalformed is true when the groups claim was present and non-null
	// but could not be decoded as a JSON array of strings or a single string. When
	// malformed, GroupsClaimPresent is false so callers skip group reconciliation
	// (a claim we cannot parse must not be read as "no groups" and demote the
	// user); callers should log it so the IdP misconfiguration is visible.
	GroupsClaimMalformed bool
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

// AuthURL returns the authorization URL to redirect the browser to. nonce is
// a per-request value the caller must also pass to VerifyIDToken on callback;
// it defends against ID-token replay/injection by binding the token the IdP
// returns to this specific authorization request (RFC 6749 doesn't require a
// nonce, but the OIDC core spec does for exactly this reason).
func (p *OIDCProvider) AuthURL(state, nonce string) string {
	return p.oauth2Cfg.AuthCodeURL(state, oauth2.AccessTypeOnline, oauth2.SetAuthURLParam("nonce", nonce))
}

// Exchange trades an authorization code for tokens.
func (p *OIDCProvider) Exchange(ctx context.Context, code string) (*oauth2.Token, error) {
	tok, err := p.oauth2Cfg.Exchange(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("oidc token exchange: %w", err)
	}
	return tok, nil
}

// VerifyIDToken extracts and verifies the ID token, returning the user's
// claims. expectedNonce must equal the nonce previously passed to AuthURL for
// this authorization request; a missing or mismatched nonce claim is rejected
// (defense-in-depth against ID-token replay/injection - see AuthURL). Pass ""
// only when no nonce was requested, which production callers never do.
func (p *OIDCProvider) VerifyIDToken(ctx context.Context, tok *oauth2.Token, expectedNonce string) (*OIDCUser, error) {
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
	var sub, email, name, nonce string
	_ = json.Unmarshal(claims["sub"], &sub)
	_ = json.Unmarshal(claims["email"], &email)
	_ = json.Unmarshal(claims["name"], &name)
	_ = json.Unmarshal(claims["nonce"], &nonce)
	if sub == "" {
		return nil, fmt.Errorf("oidc: id_token missing sub claim")
	}
	if expectedNonce != "" && nonce != expectedNonce {
		return nil, fmt.Errorf("oidc: id_token nonce mismatch")
	}
	rawGroups := claims[p.groupsClaim]
	groups, decoded := decodeGroupsClaim(rawGroups)
	hasValue := len(rawGroups) > 0 && string(rawGroups) != "null"
	return &OIDCUser{
		Sub:                  sub,
		Email:                email,
		Name:                 name,
		Groups:               groups,
		GroupsClaimPresent:   hasValue && decoded,
		GroupsClaimMalformed: hasValue && !decoded,
	}, nil
}

// decodeGroupsClaim normalizes an OIDC groups claim that may be a JSON array of
// strings or a single string. The bool is false when the claim is present but
// cannot be decoded as either form (malformed); callers MUST treat that as
// "groups unknown" rather than "no groups", so a malformed claim never silently
// demotes a user. Absent, null, empty-array, and empty-string claims decode to
// an empty slice with ok=true (a definite "no groups").
func decodeGroupsClaim(raw json.RawMessage) ([]string, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return []string{}, true
	}
	var arr []string
	if err := json.Unmarshal(raw, &arr); err == nil {
		return arr, true
	}
	var one string
	if err := json.Unmarshal(raw, &one); err == nil {
		if one == "" {
			return []string{}, true
		}
		return []string{one}, true
	}
	return nil, false
}
