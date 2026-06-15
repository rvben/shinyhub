package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rvben/shinyhub/internal/config"
)

func TestSnapshotConfig_Env(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	t.Setenv("SHINYHUB_RUNTIME_SNAPSHOT_ENABLED", "true")
	t.Setenv("SHINYHUB_RUNTIME_SNAPSHOT_MAX_SUSPENDED", "8")
	t.Setenv("SHINYHUB_RUNTIME_SNAPSHOT_RECLAIM_MIN_FRACTION", "0.75")

	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	s := cfg.Runtime.Snapshot
	if !s.Enabled || s.MaxSuspended != 8 || s.ReclaimMinFraction != 0.75 {
		t.Fatalf("snapshot config = %+v", s)
	}
}

// TestSnapshotConfig_YAML pins the YAML location: snapshot lives at
// runtime.snapshot (shared by both runtimes), not the old runtime.docker.snapshot.
func TestSnapshotConfig_YAML(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	yaml := `
auth:
  secret: "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
runtime:
  mode: native
  snapshot:
    enabled: true
    max_suspended: 4
    reclaim_min_fraction: 0.6
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	s := cfg.Runtime.Snapshot
	if !s.Enabled || s.MaxSuspended != 4 || s.ReclaimMinFraction != 0.6 {
		t.Fatalf("snapshot config = %+v", s)
	}
}

func TestSnapshotConfig_Defaults(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")

	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	s := cfg.Runtime.Snapshot
	if s.Enabled {
		t.Error("snapshot must default disabled")
	}
	if s.MaxSuspended != 16 {
		t.Errorf("MaxSuspended default = %d, want 16", s.MaxSuspended)
	}
	if s.ReclaimMinFraction != 0.8 {
		t.Errorf("ReclaimMinFraction default = %v, want 0.8", s.ReclaimMinFraction)
	}
}

func TestSnapshotConfig_RejectsBadFraction(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	t.Setenv("SHINYHUB_RUNTIME_SNAPSHOT_RECLAIM_MIN_FRACTION", "1.5")

	if _, err := config.Load(""); err == nil {
		t.Fatal("expected error for reclaim_min_fraction > 1")
	}
}
