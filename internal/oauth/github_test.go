package oauth_test

import (
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/oauth"
)

func TestGitHub_AuthURLContainsState(t *testing.T) {
	p := oauth.NewGitHub("client-id", "client-secret", "http://localhost/callback")
	url := p.AuthURL("my-state-nonce")
	if !strings.Contains(url, "state=my-state-nonce") {
		t.Errorf("AuthURL missing state param: %s", url)
	}
	if !strings.Contains(url, "github.com") {
		t.Errorf("AuthURL should point to github.com: %s", url)
	}
}

func TestGitHub_AuthURLContainsScopes(t *testing.T) {
	p := oauth.NewGitHub("id", "secret", "http://localhost/cb")
	url := p.AuthURL("state")
	if !strings.Contains(url, "scope") {
		t.Errorf("AuthURL missing scope: %s", url)
	}
}
