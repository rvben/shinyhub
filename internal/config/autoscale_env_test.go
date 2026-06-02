package config

import (
	"testing"
	"time"
)

// Autoscale settings must be overridable from the environment, consistent with
// every other runtime block, so 12-factor deployments can configure the
// controller without a config file.
func TestAutoscaleEnvOverrides(t *testing.T) {
	t.Setenv("SHINYHUB_RUNTIME_AUTOSCALE_ENABLED", "true")
	t.Setenv("SHINYHUB_RUNTIME_AUTOSCALE_SCAN_INTERVAL", "45s")
	t.Setenv("SHINYHUB_RUNTIME_AUTOSCALE_COOLDOWN", "3m")
	t.Setenv("SHINYHUB_RUNTIME_AUTOSCALE_DEFAULT_TARGET", "0.75")

	cfg := &Config{}
	if err := applyEnv(cfg); err != nil {
		t.Fatalf("applyEnv: %v", err)
	}
	if !cfg.Runtime.Autoscale.Enabled {
		t.Error("Enabled = false, want true")
	}
	if cfg.Runtime.Autoscale.ScanInterval != 45*time.Second {
		t.Errorf("ScanInterval = %v, want 45s", cfg.Runtime.Autoscale.ScanInterval)
	}
	if cfg.Runtime.Autoscale.Cooldown != 3*time.Minute {
		t.Errorf("Cooldown = %v, want 3m", cfg.Runtime.Autoscale.Cooldown)
	}
	if cfg.Runtime.Autoscale.DefaultTarget != 0.75 {
		t.Errorf("DefaultTarget = %v, want 0.75", cfg.Runtime.Autoscale.DefaultTarget)
	}
}

// Invalid env values are rejected with a clear error, mirroring the YAML path
// (scan_interval/cooldown must be > 0; default_target must be in (0,1]).
func TestAutoscaleEnvOverrides_Invalid(t *testing.T) {
	cases := []struct {
		name, key, val string
	}{
		{"non-positive scan interval", "SHINYHUB_RUNTIME_AUTOSCALE_SCAN_INTERVAL", "0s"},
		{"non-positive cooldown", "SHINYHUB_RUNTIME_AUTOSCALE_COOLDOWN", "-1m"},
		{"bad duration", "SHINYHUB_RUNTIME_AUTOSCALE_SCAN_INTERVAL", "soon"},
		{"target above 1", "SHINYHUB_RUNTIME_AUTOSCALE_DEFAULT_TARGET", "1.5"},
		{"target zero", "SHINYHUB_RUNTIME_AUTOSCALE_DEFAULT_TARGET", "0"},
		{"target not a number", "SHINYHUB_RUNTIME_AUTOSCALE_DEFAULT_TARGET", "high"},
		{"bad bool", "SHINYHUB_RUNTIME_AUTOSCALE_ENABLED", "yes-please"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(tc.key, tc.val)
			cfg := &Config{}
			if err := applyEnv(cfg); err == nil {
				t.Fatalf("expected error for %s=%q, got nil", tc.key, tc.val)
			}
		})
	}
}
