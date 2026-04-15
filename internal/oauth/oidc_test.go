package oauth_test

import (
	"context"
	"testing"

	"github.com/rvben/shinyhub/internal/oauth"
)

func TestNewOIDCProvider_InvalidIssuer(t *testing.T) {
	ctx := context.Background()
	_, err := oauth.NewOIDCProvider(
		ctx,
		"https://invalid.example.invalid/oidc",
		"client", "secret", "https://app.example.com/callback", "SSO",
	)
	if err == nil {
		t.Error("expected error for invalid issuer URL")
	}
}
