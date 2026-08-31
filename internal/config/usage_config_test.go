package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rvben/shinyhub/internal/config"
)

func TestUsageConfigDefaults(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Usage.Enabled || cfg.Usage.IdentityMode != config.UsageIdentityUnattributed ||
		cfg.Usage.RawRetentionDays != 30 || cfg.Usage.AggregateRetentionDays != 365 {
		t.Fatalf("Usage = %+v, want enabled, unattributed, 30-day raw and 365-day aggregate retention", cfg.Usage)
	}
}

func TestUsageConfigYAMLAndEnvironment(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	dir := t.TempDir()
	path := filepath.Join(dir, "shinyhub.yaml")
	if err := os.WriteFile(path, []byte("usage:\n  enabled: false\n  identity_mode: pseudonymous\n  raw_retention_days: 14\n  aggregate_retention_days: 120\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Usage.Enabled || cfg.Usage.IdentityMode != config.UsageIdentityPseudonymous ||
		cfg.Usage.RawRetentionDays != 14 || cfg.Usage.AggregateRetentionDays != 120 {
		t.Fatalf("YAML Usage = %+v", cfg.Usage)
	}

	t.Setenv("SHINYHUB_USAGE_ENABLED", "true")
	t.Setenv("SHINYHUB_USAGE_IDENTITY_MODE", "identified")
	t.Setenv("SHINYHUB_USAGE_RAW_RETENTION_DAYS", "0")
	t.Setenv("SHINYHUB_USAGE_AGGREGATE_RETENTION_DAYS", "0")
	cfg, err = config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Usage.Enabled || cfg.Usage.IdentityMode != config.UsageIdentityIdentified ||
		cfg.Usage.RawRetentionDays != 0 || cfg.Usage.AggregateRetentionDays != 0 {
		t.Fatalf("environment Usage = %+v", cfg.Usage)
	}
}

func TestUsageConfigRejectsNegativeRetention(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	t.Setenv("SHINYHUB_USAGE_RAW_RETENTION_DAYS", "-1")
	if _, err := config.Load(""); err == nil {
		t.Fatal("expected negative usage retention to be rejected")
	}
}

func TestUsageConfigRejectsInvalidModeAndShortAggregateRetention(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	t.Setenv("SHINYHUB_USAGE_IDENTITY_MODE", "named-ish")
	if _, err := config.Load(""); err == nil {
		t.Fatal("expected invalid usage identity mode to be rejected")
	}
	t.Setenv("SHINYHUB_USAGE_IDENTITY_MODE", "unattributed")
	t.Setenv("SHINYHUB_USAGE_RAW_RETENTION_DAYS", "30")
	t.Setenv("SHINYHUB_USAGE_AGGREGATE_RETENTION_DAYS", "14")
	if _, err := config.Load(""); err == nil {
		t.Fatal("expected aggregate retention shorter than raw retention to be rejected")
	}
}
