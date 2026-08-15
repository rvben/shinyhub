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

func TestForwardAuthRequiresProxySharedSecret(t *testing.T) {
	path := writeYAML(t, `
auth:
  secret: 01234567890123456789012345678901
  forward_auth:
    enabled: true
`)
	_, err := config.Load(path)
	if err == nil || !strings.Contains(err.Error(), "forward_auth.shared_secret") {
		t.Fatalf("Load error = %v, want missing shared-secret error", err)
	}
}
