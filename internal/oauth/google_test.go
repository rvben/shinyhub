package oauth_test

import (
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
	if !containsString(url, "test-state-123") {
		t.Errorf("AuthURL missing state param: %s", url)
	}
	// Must point at Google's auth domain.
	if !containsString(url, "accounts.google.com") {
		t.Errorf("AuthURL missing Google domain: %s", url)
	}
}

func containsString(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsStringHelper(s, sub))
}

func containsStringHelper(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
