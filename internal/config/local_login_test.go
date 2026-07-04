package config_test

import (
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/config"
)

// Local username/password login is on by default.
func TestLocalLogin_DefaultsEnabled(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.Auth.LocalLoginEnabled() {
		t.Error("local login must default to enabled when unset")
	}
}

// The lockout guard: disabling local login with no SSO path configured would
// lock out every user, so it must fail fast at startup with a clear message.
func TestLocalLogin_DisabledWithoutSSOIsRejected(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	t.Setenv("SHINYHUB_AUTH_LOCAL_LOGIN", "false")
	_, err := config.Load("")
	if err == nil {
		t.Fatal("disabling local login with no SSO configured must fail (lockout guard)")
	}
	if !strings.Contains(err.Error(), "lock out") {
		t.Errorf("error should explain the lockout risk, got: %v", err)
	}
}

// SSO-only is allowed once a browser SSO path (here GitHub) is configured.
func TestLocalLogin_DisabledWithSSOIsAllowed(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	t.Setenv("SHINYHUB_AUTH_LOCAL_LOGIN", "false")
	t.Setenv("SHINYHUB_GITHUB_CLIENT_ID", "gh-client")
	t.Setenv("SHINYHUB_GITHUB_CLIENT_SECRET", "gh-secret")
	t.Setenv("SHINYHUB_GITHUB_CALLBACK_URL", "https://x.example/api/auth/github/callback")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("SSO-only with GitHub configured should load: %v", err)
	}
	if cfg.Auth.LocalLoginEnabled() {
		t.Error("local login should be disabled")
	}
	if !cfg.HasSSOLoginPath() {
		t.Error("HasSSOLoginPath should be true when GitHub is configured")
	}
}

// forward-auth counts as an SSO login path (users authenticate at the edge).
func TestLocalLogin_DisabledWithForwardAuthIsAllowed(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	t.Setenv("SHINYHUB_AUTH_LOCAL_LOGIN", "false")
	t.Setenv("SHINYHUB_FORWARD_AUTH_ENABLED", "true")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("SSO-only with forward-auth should load: %v", err)
	}
	if cfg.Auth.LocalLoginEnabled() {
		t.Error("local login should be disabled")
	}
}
