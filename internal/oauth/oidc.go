package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// OIDCProvider wraps an OpenID Connect provider for use with ShinyHub's
// auth flow. It is compatible with any OIDC-compliant IdP.
type OIDCProvider struct {
	verifier                     *gooidc.IDTokenVerifier
	oauth2Cfg                    oauth2.Config
	FrontChannelSessionSupported bool
	DisplayName                  string
	groupsClaim                  string
	IssuerURL                    string
	ClientID                     string
	endSessionEndpoint           string
}

// OIDCUser holds the claims extracted from the ID token.
type OIDCUser struct {
	SID                string
	GroupsOverage      bool
	Claims             map[string]json.RawMessage
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
	var metadata struct {
		EndSessionEndpoint           string `json:"end_session_endpoint"`
		FrontChannelSessionSupported bool   `json:"frontchannel_logout_session_supported"`
	}
	if err := provider.Claims(&metadata); err != nil {
		return nil, err
	}
	return &OIDCProvider{
		IssuerURL: issuerURL, ClientID: clientID, endSessionEndpoint: metadata.EndSessionEndpoint, FrontChannelSessionSupported: metadata.FrontChannelSessionSupported,
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
	var sub, email, name, nonce, sid string
	_ = json.Unmarshal(claims["sid"], &sid)
	var claimNames map[string]string
	_ = json.Unmarshal(claims["_claim_names"], &claimNames)
	var hasGroups bool
	_ = json.Unmarshal(claims["hasgroups"], &hasGroups)
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
		Sub: sub,
		SID: sid, Claims: claims, GroupsOverage: claimNames["groups"] != "" || hasGroups,
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

// LogoutURL uses only provider discovery and the configured redirect URI.
func (p *OIDCProvider) LogoutURL(idToken, returnURL string) (string, error) {
	u, err := url.Parse(p.endSessionEndpoint)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"))) {
		return "", fmt.Errorf("OIDC provider has no secure end_session_endpoint")
	}
	q := u.Query()
	if idToken != "" {
		q.Set("id_token_hint", idToken)
	} else {
		q.Del("id_token_hint")
	}
	q.Set("post_logout_redirect_uri", returnURL)
	q.Set("client_id", p.ClientID)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// Username reads an explicitly configured claim without lossy sanitization.
// A missing claim may fall back for new users, but must never be used to link.
func (u *OIDCUser) Username(claim string, required, stripDomain bool) (string, error) {
	if claim == "" {
		return "", nil
	}
	raw := u.Claims[claim]
	var value string
	if len(raw) == 0 || string(raw) == "null" {
		if required {
			return "", fmt.Errorf("required OIDC username claim is absent")
		}
		return "", nil
	}
	if err := json.Unmarshal(raw, &value); err != nil || value == "" {
		return "", fmt.Errorf("OIDC username claim must be a non-empty string")
	}
	if stripDomain {
		if i := strings.IndexByte(value, '@'); i >= 0 {
			value = value[:i]
		}
	}
	if len(value) == 0 || len(value) > 64 || value == "." || value == ".." {
		return "", fmt.Errorf("OIDC username claim must contain 1 to 64 characters and cannot be . or ..")
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return "", fmt.Errorf("OIDC username claim contains unsupported characters")
		}
	}
	return strings.ToLower(value), nil
}
