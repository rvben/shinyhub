package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rvben/shinyhub/internal/config"
)

// The pre-migration snapshot is on by default: an operator who never heard of
// the knob still gets a rollback point before a schema change.
func TestPreMigrationSnapshot_DefaultsEnabled(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.Database.PreMigrationSnapshot {
		t.Error("pre-migration snapshot must default to enabled when unset")
	}
}

// An operator with an external backup schedule, or a database too large to copy
// inside the start timeout, can turn it off.
func TestPreMigrationSnapshot_DisabledInYAML(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	path := filepath.Join(t.TempDir(), "shinyhub.yaml")
	if err := os.WriteFile(path, []byte("database:\n  pre_migration_snapshot: false\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Database.PreMigrationSnapshot {
		t.Error("pre_migration_snapshot: false in YAML must disable the snapshot")
	}
	// Negative control: an unrelated database key must not disable it, or the
	// test above would pass on a config that never parsed the key at all.
	other := filepath.Join(t.TempDir(), "shinyhub.yaml")
	if err := os.WriteFile(other, []byte("database:\n  driver: sqlite\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg2, err := config.Load(other)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg2.Database.PreMigrationSnapshot {
		t.Error("a database block without the key must leave the snapshot enabled")
	}
}

// The env override exists so a container operator can disable it without
// editing a mounted config file.
func TestPreMigrationSnapshot_DisabledByEnv(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	t.Setenv("SHINYHUB_DB_PRE_MIGRATION_SNAPSHOT", "false")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Database.PreMigrationSnapshot {
		t.Error("SHINYHUB_DB_PRE_MIGRATION_SNAPSHOT=false must disable the snapshot")
	}
}
