package config_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
)

// TestLoad_MaintenanceFromYAML proves the documented maintenance block is
// actually loaded from YAML (not only from environment variables).
func TestLoad_MaintenanceFromYAML(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	dir := t.TempDir()
	path := filepath.Join(dir, "shinyhub.yaml")
	body := "maintenance:\n" +
		"  audit_retention_days: 30\n" +
		"  schedule_run_retention_count: 25\n" +
		"  interval: 2h\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Maintenance.AuditRetentionDays != 30 {
		t.Errorf("AuditRetentionDays = %d, want 30", cfg.Maintenance.AuditRetentionDays)
	}
	if cfg.Maintenance.ScheduleRunRetentionCount != 25 {
		t.Errorf("ScheduleRunRetentionCount = %d, want 25", cfg.Maintenance.ScheduleRunRetentionCount)
	}
	if cfg.Maintenance.Interval != 2*time.Hour {
		t.Errorf("Interval = %v, want 2h", cfg.Maintenance.Interval)
	}
}

// TestLoad_MaintenanceDefaultInterval verifies the interval default applies when
// the YAML omits it but sets a retention value.
func TestLoad_MaintenanceDefaultInterval(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	dir := t.TempDir()
	path := filepath.Join(dir, "shinyhub.yaml")
	if err := os.WriteFile(path, []byte("maintenance:\n  audit_retention_days: 7\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Maintenance.Interval != time.Hour {
		t.Errorf("Interval = %v, want default 1h", cfg.Maintenance.Interval)
	}
}
