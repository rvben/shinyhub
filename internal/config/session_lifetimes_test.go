package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
)

func TestBrowserSessionLifetimeConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, yaml string
		ttl, max   time.Duration
		invalid    bool
	}{
		{"defaults", "{}\n", time.Hour, 12 * time.Hour, false},
		{"custom", "auth:\n  session_ttl: 15m\n  session_max_age: 8h\n", 15 * time.Minute, 8 * time.Hour, false},
		{"zero", "auth:\n  session_ttl: 0s\n", 0, 0, true},
		{"negative", "auth:\n  session_max_age: -1h\n", 0, 0, true},
		{"too short", "auth:\n  session_ttl: 30s\n", 0, 0, true},
		{"too long", "auth:\n  session_max_age: 721h\n", 0, 0, true},
		{"ttl beyond cap", "auth:\n  session_ttl: 13h\n", 0, 0, true},
		{"unitless", "auth:\n  session_ttl: 600\n", 0, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SHINYHUB_AUTH_SECRET", strings.Repeat("x", 32))
			t.Setenv("SHINYHUB_AUTH_SESSION_TTL", "")
			t.Setenv("SHINYHUB_AUTH_SESSION_MAX_AGE", "")
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(tc.yaml), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.Load(path)
			if tc.invalid {
				if err == nil {
					t.Fatal("invalid duration accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Auth.BrowserSessionTTL() != tc.ttl || cfg.Auth.BrowserSessionMaxAge() != tc.max {
				t.Fatalf("unexpected lifetimes: %+v", cfg.Auth)
			}
		})
	}
}

func TestBrowserSessionLifetimeEnvironmentOverrides(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SESSION_TTL", "10m")
	t.Setenv("SHINYHUB_AUTH_SESSION_MAX_AGE", "4h")
	cfg := loadWithYAML(t, "auth:\n  session_ttl: 20m\n  session_max_age: 8h\n")
	if cfg.Auth.BrowserSessionTTL() != 10*time.Minute || cfg.Auth.BrowserSessionMaxAge() != 4*time.Hour {
		t.Fatal("environment did not override YAML")
	}
	for _, value := range []string{"0", "-1h", "invalid", "30s"} {
		t.Setenv("SHINYHUB_AUTH_SESSION_TTL", value)
		_, err := config.Load("")
		if err == nil {
			t.Fatalf("accepted environment value %q", value)
		}
	}
}
