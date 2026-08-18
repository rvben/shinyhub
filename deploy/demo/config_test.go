package demo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/fleet"
)

func TestPublicDemoConfigurationIsBounded(t *testing.T) {
	dataRoot := t.TempDir()
	t.Setenv("SHINYHUB_AUTH_SECRET", strings.Repeat("a", 64))
	t.Setenv("SHINYHUB_DEPLOY_TOKEN", "shk_"+strings.Repeat("b", 64))
	t.Setenv("SHINYHUB_DEPLOY_TOKEN_ROLE", "admin")
	t.Setenv("SHINYHUB_GITHUB_CLIENT_ID", "test-client")
	t.Setenv("SHINYHUB_GITHUB_CLIENT_SECRET", "test-secret")
	t.Setenv("SHINYHUB_GITHUB_CALLBACK_URL", "https://demo.shinyhub.dev/api/auth/github/callback")
	t.Setenv("SHINYHUB_APPS_DIR", filepath.Join(dataRoot, "apps"))
	t.Setenv("SHINYHUB_APP_DATA_DIR", filepath.Join(dataRoot, "app-data"))

	cfg, err := config.Load("shinyhub.yaml")
	if err != nil {
		t.Fatalf("load demo config: %v", err)
	}

	if cfg.Server.BaseURL != "https://demo.shinyhub.dev" {
		t.Fatalf("base URL = %q", cfg.Server.BaseURL)
	}
	if cfg.Server.AppOrigin != "https://apps.demo.shinyhub.dev" {
		t.Fatalf("app origin = %q", cfg.Server.AppOrigin)
	}
	if cfg.Auth.LocalLoginEnabled() {
		t.Fatal("local login must be disabled on the public demo")
	}
	if cfg.Auth.OAuthDefaultRole != "viewer" {
		t.Fatalf("OAuth default role = %q, want viewer", cfg.Auth.OAuthDefaultRole)
	}
	if cfg.Runtime.MaxReplicas != 1 || cfg.Runtime.DefaultReplicas != 1 {
		t.Fatalf("replica bounds = default %d, max %d", cfg.Runtime.DefaultReplicas, cfg.Runtime.MaxReplicas)
	}
	if cfg.Runtime.DefaultMaxSessionsPerReplica > 6 {
		t.Fatalf("default session cap = %d, want <= 6", cfg.Runtime.DefaultMaxSessionsPerReplica)
	}
	if cfg.Storage.MaxBundleMB > 64 || cfg.Storage.AppQuotaMB > 512 {
		t.Fatalf("storage bounds = bundle %d MiB, app %d MiB", cfg.Storage.MaxBundleMB, cfg.Storage.AppQuotaMB)
	}
}

func TestPublicFleetIsCuratedAndBounded(t *testing.T) {
	data, err := os.ReadFile("fleet.toml")
	if err != nil {
		t.Fatal(err)
	}
	manifest, problems := fleet.ParseManifest(data, "fleet.toml")
	if len(problems) != 0 {
		t.Fatalf("fleet manifest problems: %v", problems)
	}
	if len(manifest.Apps) < 4 {
		t.Fatalf("demo fleet has %d apps, want at least 4", len(manifest.Apps))
	}
	for _, app := range manifest.Apps {
		if app.Visibility != "public" {
			t.Errorf("app %q visibility = %q", app.Slug, app.Visibility)
		}
		if app.Config.Replicas == nil || *app.Config.Replicas != 1 {
			t.Errorf("app %q must declare exactly one replica", app.Slug)
		}
		if app.Config.MaxSessionsPerReplica == nil || *app.Config.MaxSessionsPerReplica > 6 {
			t.Errorf("app %q must declare a session cap <= 6", app.Slug)
		}
		if app.Config.HibernateTimeoutMinutes == nil || *app.Config.HibernateTimeoutMinutes > 10 {
			t.Errorf("app %q must hibernate within 10 minutes", app.Slug)
		}
	}
}
