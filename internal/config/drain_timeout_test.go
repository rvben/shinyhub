package config_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
)

func TestLoad_DrainTimeoutDefault(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.DrainTimeout != 60*time.Second {
		t.Fatalf("DrainTimeout default = %v, want 60s", cfg.Server.DrainTimeout)
	}
}

func TestLoad_DrainTimeoutExplicit(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("server:\n  drain_timeout: 5m\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.DrainTimeout != 5*time.Minute {
		t.Fatalf("DrainTimeout = %v, want 5m", cfg.Server.DrainTimeout)
	}
}
