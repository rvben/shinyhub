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
		"  app_log_run_retention_count: 7\n" +
		"  fleet_run_retention_count: 10\n" +
		"  development_session_retention_days: 90\n" +
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
	if cfg.Maintenance.AppLogRunRetentionCount != 7 {
		t.Errorf("AppLogRunRetentionCount = %d, want 7", cfg.Maintenance.AppLogRunRetentionCount)
	}
	if cfg.Maintenance.FleetRunRetentionCount != 10 {
		t.Errorf("FleetRunRetentionCount = %d, want 10", cfg.Maintenance.FleetRunRetentionCount)
	}
	if cfg.Maintenance.DevelopmentSessionRetentionDays != 90 {
		t.Errorf("DevelopmentSessionRetentionDays = %d, want 90", cfg.Maintenance.DevelopmentSessionRetentionDays)
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
	if cfg.Maintenance.AppLogRunRetentionCount != config.DefaultAppLogRunRetentionCount {
		t.Errorf("AppLogRunRetentionCount = %d, want default %d", cfg.Maintenance.AppLogRunRetentionCount, config.DefaultAppLogRunRetentionCount)
	}
}

func TestLoad_MaintenanceAppLogRetentionForever(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	t.Setenv("SHINYHUB_APP_LOG_RUN_RETENTION_COUNT", "-1")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Maintenance.AppLogRunRetentionCount != -1 {
		t.Fatalf("AppLogRunRetentionCount = %d, want -1", cfg.Maintenance.AppLogRunRetentionCount)
	}
}

func TestLoad_MaintenanceRejectsInvalidAppLogRetention(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	t.Setenv("SHINYHUB_APP_LOG_RUN_RETENTION_COUNT", "-2")
	if _, err := config.Load(""); err == nil {
		t.Fatal("expected retention below -1 to be rejected")
	}
}

// TestLoad_MaintenanceFleetAndDevelopmentSessionEnv proves both new retention
// knobs are read from their environment variables, matching the other
// maintenance knobs.
func TestLoad_MaintenanceFleetAndDevelopmentSessionEnv(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	t.Setenv("SHINYHUB_FLEET_RUN_RETENTION_COUNT", "5")
	t.Setenv("SHINYHUB_DEVELOPMENT_SESSION_RETENTION_DAYS", "45")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Maintenance.FleetRunRetentionCount != 5 {
		t.Errorf("FleetRunRetentionCount = %d, want 5", cfg.Maintenance.FleetRunRetentionCount)
	}
	if cfg.Maintenance.DevelopmentSessionRetentionDays != 45 {
		t.Errorf("DevelopmentSessionRetentionDays = %d, want 45", cfg.Maintenance.DevelopmentSessionRetentionDays)
	}
}

// TestLoad_MaintenanceFleetAndDevelopmentSessionDefaultToZero proves both new
// knobs default to 0 ("keep forever"/"keep all") when unset.
func TestLoad_MaintenanceFleetAndDevelopmentSessionDefaultToZero(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Maintenance.FleetRunRetentionCount != 0 {
		t.Errorf("FleetRunRetentionCount = %d, want 0", cfg.Maintenance.FleetRunRetentionCount)
	}
	if cfg.Maintenance.DevelopmentSessionRetentionDays != 0 {
		t.Errorf("DevelopmentSessionRetentionDays = %d, want 0", cfg.Maintenance.DevelopmentSessionRetentionDays)
	}
}

// TestLoad_MaintenanceFleetAndDevelopmentSessionKeepAllExplicit proves -1 is
// accepted as an explicit "keep all", matching app_log_run_retention_count's
// -1 convention, and normalizes to the same value as the 0 default so an
// operator who copies the sibling setting's -1 gets what they expect.
func TestLoad_MaintenanceFleetAndDevelopmentSessionKeepAllExplicit(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	t.Setenv("SHINYHUB_FLEET_RUN_RETENTION_COUNT", "-1")
	t.Setenv("SHINYHUB_DEVELOPMENT_SESSION_RETENTION_DAYS", "-1")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Maintenance.FleetRunRetentionCount != 0 {
		t.Errorf("FleetRunRetentionCount = %d, want 0 (keep all)", cfg.Maintenance.FleetRunRetentionCount)
	}
	if cfg.Maintenance.DevelopmentSessionRetentionDays != 0 {
		t.Errorf("DevelopmentSessionRetentionDays = %d, want 0 (keep all)", cfg.Maintenance.DevelopmentSessionRetentionDays)
	}
}

// TestLoad_MaintenanceRejectsInvalidFleetAndDevelopmentSessionRetention proves
// a negative value other than -1 is rejected at load rather than silently
// clamped to 0: a typo like -3 must not be quietly turned into "keep
// forever", matching AppLogRunRetentionCount's own -2 rejection.
func TestLoad_MaintenanceRejectsInvalidFleetAndDevelopmentSessionRetention(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")

	t.Setenv("SHINYHUB_FLEET_RUN_RETENTION_COUNT", "-3")
	if _, err := config.Load(""); err == nil {
		t.Fatal("expected fleet_run_retention_count below -1 to be rejected")
	}

	t.Setenv("SHINYHUB_FLEET_RUN_RETENTION_COUNT", "")
	t.Setenv("SHINYHUB_DEVELOPMENT_SESSION_RETENTION_DAYS", "-2")
	if _, err := config.Load(""); err == nil {
		t.Fatal("expected development_session_retention_days below -1 to be rejected")
	}
}
