package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/config"
)

func TestAppOriginRequiresDedicatedBareHTTPSOrigin(t *testing.T) {
	tests := []struct {
		name      string
		origin    string
		base      string
		wantError string
	}{
		{name: "valid", origin: "https://apps.example.com/", base: "https://hub.example.com"},
		{name: "http", origin: "http://apps.example.com", base: "https://hub.example.com", wantError: "bare HTTPS origin"},
		{name: "path", origin: "https://apps.example.com/shiny", base: "https://hub.example.com", wantError: "bare HTTPS origin"},
		{name: "same host", origin: "https://hub.example.com", base: "https://hub.example.com", wantError: "different host"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "shinyhub.yaml")
			yaml := "auth:\n  secret: 01234567890123456789012345678901\nserver:\n  base_url: " + tt.base + "\n  app_origin: " + tt.origin + "\n"
			if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.Load(path)
			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("Load error = %v, want containing %q", err, tt.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Server.AppOrigin != "https://apps.example.com" {
				t.Fatalf("normalized app origin = %q", cfg.Server.AppOrigin)
			}
		})
	}
}

func TestSupportSessionsRequireAppOrigin(t *testing.T) {
	path := writeYAML(t, "auth:\n  secret: 01234567890123456789012345678901\n  support_sessions: true\n")
	_, err := config.Load(path)
	if err == nil || !strings.Contains(err.Error(), "auth.support_sessions requires server.app_origin") {
		t.Fatalf("Load error = %v, want app-origin requirement", err)
	}
}

func TestSupportSessionsLoadWhenAppOriginIsIsolated(t *testing.T) {
	path := writeYAML(t, "auth:\n  secret: 01234567890123456789012345678901\n  support_sessions: true\nserver:\n  base_url: https://hub.example.com\n  app_origin: https://apps.example.com\n")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Auth.SupportSessions {
		t.Fatal("support sessions should be enabled")
	}
}

func TestSupportSessionsRequireBoundedWebSocketRechecks(t *testing.T) {
	for _, interval := range []string{"0", "31s", "-1s"} {
		t.Run(interval, func(t *testing.T) {
			path := writeYAML(t, "auth:\n  secret: 01234567890123456789012345678901\n  support_sessions: true\nserver:\n  base_url: https://hub.example.com\n  app_origin: https://apps.example.com\n  session_recheck_interval: "+interval+"\n")
			_, err := config.Load(path)
			if err == nil || !strings.Contains(err.Error(), "session_recheck_interval") {
				t.Fatalf("Load error = %v, want bounded recheck requirement", err)
			}
		})
	}
}

func TestAppOriginRejectsSameCookieHostAcrossPortsAndSpelling(t *testing.T) {
	for _, appOrigin := range []string{
		"https://hub.example.com:443",
		"https://hub.example.com:8443",
		"https://HUB.EXAMPLE.COM.",
		"https://bücher.example",
	} {
		t.Run(appOrigin, func(t *testing.T) {
			base := "https://hub.example.com"
			if strings.Contains(appOrigin, "bücher") {
				base = "https://xn--bcher-kva.example"
			}
			path := writeYAML(t, "auth:\n  secret: 01234567890123456789012345678901\nserver:\n  base_url: "+base+"\n  app_origin: "+appOrigin+"\n")
			_, err := config.Load(path)
			if err == nil || !strings.Contains(err.Error(), "different host") {
				t.Fatalf("Load error = %v, want same cookie-host rejection", err)
			}
		})
	}
}

func TestAppOriginRejectsAmbiguousNumericHost(t *testing.T) {
	path := writeYAML(t, "auth:\n  secret: 01234567890123456789012345678901\nserver:\n  base_url: https://127.0.0.1\n  app_origin: https://127.1\n")
	if _, err := config.Load(path); err == nil || !strings.Contains(err.Error(), "canonicalizable") {
		t.Fatalf("Load error = %v, want ambiguous numeric host rejection", err)
	}
}

func TestForwardAuthRejectsInvalidProxySharedSecret(t *testing.T) {
	for _, tc := range []struct {
		name       string
		secretLine string
		want       string
	}{
		{name: "missing", want: "set auth.forward_auth.shared_secret or SHINYHUB_FORWARD_AUTH_SHARED_SECRET"},
		{name: "placeholder", secretLine: "    shared_secret: replace-with-a-random-32+-character-secret\n", want: "still contains a placeholder"},
		{name: "too short", secretLine: "    shared_secret: too-short\n", want: "must be at least 32 characters"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			yaml := "auth:\n" +
				"  secret: 01234567890123456789012345678901\n" +
				"  forward_auth:\n" +
				"    enabled: true\n" +
				tc.secretLine
			path := writeYAML(t, yaml)
			_, err := config.Load(path)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Load error = %v, want containing %q", err, tc.want)
			}
		})
	}
}
