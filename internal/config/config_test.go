package config_test

import (
	"os"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
)

func writeYAML(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(content)
	f.Close()
	return f.Name()
}

func TestLifecycle_Defaults(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "test-secret")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Lifecycle.WatchInterval != 15*time.Second {
		t.Errorf("WatchInterval default: got %v, want 15s", cfg.Lifecycle.WatchInterval)
	}
	if cfg.Lifecycle.RestartMaxAttempts != 5 {
		t.Errorf("RestartMaxAttempts default: got %d, want 5", cfg.Lifecycle.RestartMaxAttempts)
	}
	if cfg.Lifecycle.HibernateTimeout != 30*time.Minute {
		t.Errorf("HibernateTimeout default: got %v, want 30m", cfg.Lifecycle.HibernateTimeout)
	}
}

func TestLifecycle_FromYAML(t *testing.T) {
	path := writeYAML(t, `
auth:
  secret: test-secret
lifecycle:
  watch_interval: 30s
  restart_max_attempts: 3
  hibernate_timeout: 10m
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Lifecycle.WatchInterval != 30*time.Second {
		t.Errorf("WatchInterval: got %v, want 30s", cfg.Lifecycle.WatchInterval)
	}
	if cfg.Lifecycle.RestartMaxAttempts != 3 {
		t.Errorf("RestartMaxAttempts: got %d, want 3", cfg.Lifecycle.RestartMaxAttempts)
	}
	if cfg.Lifecycle.HibernateTimeout != 10*time.Minute {
		t.Errorf("HibernateTimeout: got %v, want 10m", cfg.Lifecycle.HibernateTimeout)
	}
}

func TestLifecycle_HibernateDisabled(t *testing.T) {
	path := writeYAML(t, `
auth:
  secret: test-secret
lifecycle:
  hibernate_timeout: 0s
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Lifecycle.HibernateTimeout != 0 {
		t.Errorf("expected 0 (disabled globally), got %v", cfg.Lifecycle.HibernateTimeout)
	}
}

func TestLifecycle_InvalidDuration(t *testing.T) {
	path := writeYAML(t, `
auth:
  secret: test-secret
lifecycle:
  watch_interval: "not-a-duration"
`)
	_, err := config.Load(path)
	if err == nil {
		t.Error("expected error for invalid duration, got nil")
	}
}
