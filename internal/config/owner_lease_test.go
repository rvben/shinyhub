package config_test

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
)

func TestLoad_OwnerLeaseDefaults(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load(\"\"): %v", err)
	}
	if cfg.Server.InstanceID == "" {
		t.Fatal("InstanceID must default to a non-empty value")
	}
	// Default form is "<hostname>-<pid>".
	if want := "-" + strconv.Itoa(os.Getpid()); !strings.HasSuffix(cfg.Server.InstanceID, want) {
		t.Fatalf("InstanceID = %q, want suffix %q", cfg.Server.InstanceID, want)
	}
	if cfg.Server.LeaseRenewEvery != 10*time.Second {
		t.Fatalf("LeaseRenewEvery = %v, want 10s", cfg.Server.LeaseRenewEvery)
	}
	if cfg.Server.LeaseTTL != 30*time.Second {
		t.Fatalf("LeaseTTL = %v, want 30s", cfg.Server.LeaseTTL)
	}
}

func TestLoad_OwnerLeaseExplicit(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	yaml := "" +
		"server:\n" +
		"  instance_id: cp-7\n" +
		"  lease_ttl: 1m\n" +
		"  lease_renew_every: 20s\n"
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.InstanceID != "cp-7" {
		t.Fatalf("InstanceID = %q, want cp-7", cfg.Server.InstanceID)
	}
	if cfg.Server.LeaseTTL != time.Minute {
		t.Fatalf("LeaseTTL = %v, want 1m", cfg.Server.LeaseTTL)
	}
	if cfg.Server.LeaseRenewEvery != 20*time.Second {
		t.Fatalf("LeaseRenewEvery = %v, want 20s", cfg.Server.LeaseRenewEvery)
	}
}
