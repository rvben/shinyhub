package config

import (
	"fmt"
	"net/url"
	"path"
	"strings"
)

func validateForwardAuthLogout(c *ForwardAuthConfig) error {
	c.LogoutMethod = strings.ToUpper(c.LogoutMethod)
	if c.LogoutMethod == "" {
		c.LogoutMethod = "GET"
	}
	if c.LogoutMethod != "GET" && c.LogoutMethod != "POST" {
		return fmt.Errorf("auth.forward_auth.logout_method must be GET or POST")
	}
	if c.LogoutURL == "" {
		return nil
	}
	for _, ch := range c.LogoutURL {
		if ch <= 32 || ch == 127 || ch == '\\' {
			return fmt.Errorf("auth.forward_auth.logout_url contains an unsafe character")
		}
	}
	u, err := url.Parse(c.LogoutURL)
	if err != nil || u.User != nil || u.Fragment != "" || u.Opaque != "" || (u.IsAbs() && (u.Host == "" || (u.Scheme != "http" && u.Scheme != "https"))) || (!u.IsAbs() && (u.Host != "" || !strings.HasPrefix(c.LogoutURL, "/") || strings.HasPrefix(c.LogoutURL, "//"))) {
		return fmt.Errorf("auth.forward_auth.logout_url must be an HTTP(S) URL or a single-leading-slash path, without userinfo or fragment")
	}
	p := path.Clean(u.Path)
	if p == "/api/auth/logout" || p == "/api/auth/handoff" || p == "/api/app/logout" || p == "/api/auth/forward-auth" || strings.HasPrefix(p, "/api/auth/forward-auth/") {
		return fmt.Errorf("auth.forward_auth.logout_url must not point to a ShinyHub logout or reconnect endpoint")
	}
	return nil
}
