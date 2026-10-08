package config_test

import (
	"github.com/rvben/shinyhub/internal/config"
	"strconv"
	"strings"
	"testing"
)

func TestForwardAuthLogoutConfig(t *testing.T) {
	for _, tc := range []struct {
		url, method string
		valid       bool
	}{
		{"https://auth.example/logout?return=/signed-out", "POST", true}, {"/sso/logout", "GET", true}, {"", "", true},
		{"/api/auth/handoff", "POST", false}, {"https://hub.example/api/auth/handoff", "POST", false},
		{"//evil.example/logout", "GET", false}, {"javascript:alert(1)", "GET", false}, {"https://user:password@auth.example/logout", "GET", false}, {"/logout#fragment", "GET", false}, {"/logout\n", "GET", false}, {"/\\evil", "GET", false}, {"/api/auth/forward-auth/logout", "GET", false}, {"https://hub.example/api/app/logout", "GET", false}, {"/logout", "DELETE", false},
	} {
		t.Run(strconv.Quote(tc.url)+tc.method, func(t *testing.T) {
			file := writeYAML(t, "auth:\n  secret: "+strings.Repeat("a", 32)+"\n  forward_auth:\n    logout_url: "+strconv.Quote(tc.url)+"\n    logout_method: "+strconv.Quote(tc.method)+"\n")
			cfg, err := config.Load(file)
			if (err == nil) != tc.valid {
				t.Fatalf("cfg=%+v err=%v", cfg, err)
			}
		})
	}
	t.Setenv("SHINYHUB_FORWARD_AUTH_LOGOUT_URL", "https://auth.example/logout")
	t.Setenv("SHINYHUB_FORWARD_AUTH_LOGOUT_METHOD", "post")
	cfg, err := config.Load(writeYAML(t, "auth:\n  secret: "+strings.Repeat("a", 32)+"\n"))
	if err != nil || cfg.Auth.ForwardAuth.LogoutMethod != "POST" || cfg.Auth.ForwardAuth.LogoutURL != "https://auth.example/logout" {
		t.Fatalf("env cfg=%+v err=%v", cfg, err)
	}
}
