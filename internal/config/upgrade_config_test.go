package config_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
)

func TestLoad_UpgradeTimeoutDefault(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.UpgradeTimeout != 60*time.Second {
		t.Fatalf("UpgradeTimeout default = %v, want 60s", cfg.Server.UpgradeTimeout)
	}
	if cfg.Server.PIDFile != "" {
		t.Fatalf("PIDFile default = %q, want empty", cfg.Server.PIDFile)
	}
}

func TestLoad_UpgradeTimeoutExplicit(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("server:\n  upgrade_timeout: 90s\n  pid_file: /run/shinyhub/shinyhub.pid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.UpgradeTimeout != 90*time.Second {
		t.Fatalf("UpgradeTimeout = %v, want 90s", cfg.Server.UpgradeTimeout)
	}
	if cfg.Server.PIDFile != "/run/shinyhub/shinyhub.pid" {
		t.Fatalf("PIDFile = %q, want /run/shinyhub/shinyhub.pid", cfg.Server.PIDFile)
	}
}

func TestLoad_PIDFileEnvOverride(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	t.Setenv("SHINYHUB_PID_FILE", "/tmp/from-env.pid")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.PIDFile != "/tmp/from-env.pid" {
		t.Fatalf("PIDFile = %q, want /tmp/from-env.pid", cfg.Server.PIDFile)
	}
}

func TestLoad_UpgradeTimeoutEnvOverride(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	t.Setenv("SHINYHUB_UPGRADE_TIMEOUT", "2m")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.UpgradeTimeout != 2*time.Minute {
		t.Fatalf("UpgradeTimeout = %v, want 2m", cfg.Server.UpgradeTimeout)
	}
}

func TestLoad_UpgradeTimeoutEnvInvalid(t *testing.T) {
	// Unparseable and non-positive values must both fail (a non-positive value
	// would otherwise be silently replaced by the 60s default, since applyEnv
	// runs before the defaults block).
	for _, bad := range []string{"not-a-duration", "0s", "-5s"} {
		t.Run(bad, func(t *testing.T) {
			t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
			t.Setenv("SHINYHUB_UPGRADE_TIMEOUT", bad)
			if _, err := config.Load(""); err == nil {
				t.Fatalf("Load must reject SHINYHUB_UPGRADE_TIMEOUT=%q", bad)
			}
		})
	}
}
