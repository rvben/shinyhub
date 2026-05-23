package config_test

import (
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/config"
)

const metricsSecret = "auth:\n  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx\n"

// Metrics must be opt-in: an unconfigured server exposes no scrape endpoint.
func TestMetrics_DisabledByDefault(t *testing.T) {
	path := writeYAML(t, metricsSecret)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Metrics.Enabled {
		t.Error("metrics must be disabled by default")
	}
}

// When enabled without an explicit address, the scrape listener defaults to
// loopback so server internals are never exposed on a routable interface by
// accident.
func TestMetrics_DefaultsToLoopbackWhenEnabled(t *testing.T) {
	path := writeYAML(t, metricsSecret+`
metrics:
  enabled: true
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Metrics.Addr != "127.0.0.1:9090" {
		t.Errorf("default metrics.addr = %q, want 127.0.0.1:9090", cfg.Metrics.Addr)
	}
}

func TestMetrics_AddrFromYAML(t *testing.T) {
	path := writeYAML(t, metricsSecret+`
metrics:
  enabled: true
  addr: 10.0.0.5:9123
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Metrics.Addr != "10.0.0.5:9123" {
		t.Errorf("metrics.addr = %q, want 10.0.0.5:9123", cfg.Metrics.Addr)
	}
}

func TestMetrics_EnvOverride(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	t.Setenv("SHINYHUB_METRICS_ENABLED", "true")
	t.Setenv("SHINYHUB_METRICS_ADDR", "0.0.0.0:7000")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Metrics.Enabled {
		t.Error("SHINYHUB_METRICS_ENABLED=true should enable metrics")
	}
	if cfg.Metrics.Addr != "0.0.0.0:7000" {
		t.Errorf("metrics.addr = %q, want 0.0.0.0:7000", cfg.Metrics.Addr)
	}
}

// A malformed listen address is a misconfiguration that must fail loudly at
// startup rather than silently failing to bind later.
func TestMetrics_InvalidAddrRejected(t *testing.T) {
	path := writeYAML(t, metricsSecret+`
metrics:
  enabled: true
  addr: not-a-host-port
`)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error for malformed metrics.addr")
	}
	if !strings.Contains(err.Error(), "metrics.addr") {
		t.Errorf("error should mention metrics.addr: %v", err)
	}
}

// A disabled-but-malformed address must not block startup: validation only
// applies when the listener will actually be created.
func TestMetrics_InvalidAddrIgnoredWhenDisabled(t *testing.T) {
	path := writeYAML(t, metricsSecret+`
metrics:
  enabled: false
  addr: garbage
`)
	if _, err := config.Load(path); err != nil {
		t.Fatalf("a malformed addr must be ignored while metrics are disabled: %v", err)
	}
}

func TestMetrics_EnabledEnvRejectsGarbage(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	t.Setenv("SHINYHUB_METRICS_ENABLED", "maybe")
	_, err := config.Load("")
	if err == nil {
		t.Fatal("expected error for SHINYHUB_METRICS_ENABLED=maybe")
	}
	if !strings.Contains(err.Error(), "SHINYHUB_METRICS_ENABLED") {
		t.Errorf("error should name the offending env var: %v", err)
	}
}
