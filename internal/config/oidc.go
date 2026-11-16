package config

import (
	"fmt"
	"net/url"
	"strings"
)

// Validate rejects ambiguous identity and authorization policy before startup.
func (o OIDCConfig) Validate() error {
	switch o.MissingGroups {
	case "", "preserve", "reject", "empty":
	default:
		return fmt.Errorf("oauth.oidc.missing_groups must be preserve, reject, or empty")
	}
	switch o.LinkExisting {
	case "", "off", "username":
	default:
		return fmt.Errorf("oauth.oidc.link_existing must be off or username; use explicit admin linking for email migration")
	}
	if (o.UsernameRequired || o.UsernameStripDomain || o.LinkExisting == "username") && o.UsernameClaim == "" {
		return fmt.Errorf("oauth.oidc.username_claim is required for username policy and linking")
	}
	if o.EndSession {
		u, err := url.Parse(o.CallbackURL)
		if err != nil || u.Host == "" || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"))) || u.Path != "/api/auth/oidc/callback" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
			return fmt.Errorf("oauth.oidc.end_session requires a callback_url ending in /api/auth/oidc/callback without query or fragment")
		}
	}
	if o.LinkExisting == "username" && !o.UsernameRequired {
		return fmt.Errorf("oauth.oidc.link_existing: username requires username_required: true")
	}
	if o.LinkExisting == "username" {
		switch strings.ToLower(o.UsernameClaim) {
		case "email", "preferred_username", "upn", "unique_name", "name", "sub":
			return fmt.Errorf("oauth.oidc.link_existing requires a trusted corporate username claim; mutable identity claims and sub cannot be used for automatic linking")
		}
	}
	if o.GraphGroups && o.GroupsClaim != "" && o.GroupsClaim != "groups" {
		return fmt.Errorf("oauth.oidc.graph_groups requires groups_claim: groups")
	}
	if o.GraphGroups {
		u, err := url.Parse(o.IssuerURL)
		if err != nil || u.Scheme != "https" || u.Host != "login.microsoftonline.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !strings.HasSuffix(u.Path, "/v2.0") || strings.Count(u.Path, "/") != 2 || u.Path == "//v2.0" || u.Path == "/common/v2.0" || u.Path == "/organizations/v2.0" || u.Path == "/consumers/v2.0" {
			return fmt.Errorf("oauth.oidc.graph_groups requires a tenant-specific Microsoft Entra public-cloud issuer")
		}
	}
	return nil
}
