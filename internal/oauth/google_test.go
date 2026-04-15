package oauth_test

import (
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/oauth"
)

func TestNewGoogle_AuthURL(t *testing.T) {
	g := oauth.NewGoogle("client-id", "client-secret", "http://localhost/callback")
	url := g.AuthURL("test-state-123")
	if url == "" {
		t.Fatal("AuthURL returned empty string")
	}
	// Must contain the state param.
	if !strings.Contains(url, "state=test-state-123") {
		t.Errorf("AuthURL missing state param: %s", url)
	}
	// Must point at Google's auth domain.
	if !strings.Contains(url, "accounts.google.com") {
		t.Errorf("AuthURL missing Google domain: %s", url)
	}
}

func TestNewGoogle_AuthURLContainsScopes(t *testing.T) {
	g := oauth.NewGoogle("client-id", "client-secret", "http://localhost/callback")
	url := g.AuthURL("state")
	for _, scope := range []string{"openid", "email", "profile"} {
		if !strings.Contains(url, scope) {
			t.Errorf("AuthURL missing scope %q: %s", scope, url)
		}
	}
}
