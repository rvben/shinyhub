package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rvben/shinyhub/internal/config"
)

// TestLoad_ShippedExampleParses guards against the shipped example drifting out
// of sync with the config struct (e.g. a new key documented with an invalid
// value). The example's branding block references local asset files under an
// assets_dir, so the test points assets_dir at a temp dir containing those files
// (via the env override) and loads the example through the production Load path.
func TestLoad_ShippedExampleParses(t *testing.T) {
	assets := t.TempDir()
	for _, name := range []string{"logo.svg", "favicon.ico", "landing.html"} {
		if err := os.WriteFile(filepath.Join(assets, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	t.Setenv("SHINYHUB_BRANDING_ASSETS_DIR", assets)

	cfg, err := config.Load("../../shinyhub.yaml.example")
	if err != nil {
		t.Fatalf("Load(shinyhub.yaml.example): %v", err)
	}
	if cfg.Server.UpgradeTimeout <= 0 {
		t.Fatalf("example produced non-positive UpgradeTimeout %v", cfg.Server.UpgradeTimeout)
	}
	if cfg.Server.PIDFile != "" {
		t.Fatalf("example pid_file should default to empty, got %q", cfg.Server.PIDFile)
	}
}
