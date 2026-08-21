package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

// Unset means autodetect, and the accessors say so with 0 rather than with a
// made-up number, because 0 is what the callers read as "detect it".
func TestHostCapacityDefaultsToAutodetect(t *testing.T) {
	c := &Config{}
	if v := c.HostCapacityCores(); v != 0 {
		t.Fatalf("HostCapacityCores default = %v, want 0 (autodetect)", v)
	}
	if v := c.HostCapacityMemoryMB(); v != 0 {
		t.Fatalf("HostCapacityMemoryMB default = %v, want 0 (autodetect)", v)
	}
}

func TestHostCapacityExplicitValues(t *testing.T) {
	cores := 6.5
	memory := 12288
	c := &Config{Server: ServerConfig{
		HostCapacityCores:    &cores,
		HostCapacityMemoryMB: &memory,
	}}
	if v := c.HostCapacityCores(); v != 6.5 {
		t.Fatalf("HostCapacityCores = %v, want 6.5", v)
	}
	if v := c.HostCapacityMemoryMB(); v != 12288 {
		t.Fatalf("HostCapacityMemoryMB = %v, want 12288", v)
	}
}

// A zero or negative override is a typo or a leftover placeholder, never a
// claim that the host has no capacity. Both fall back to autodetection.
func TestHostCapacityNonPositiveFallsBackToAutodetect(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cores  float64
		memory int
	}{
		{"zero", 0, 0},
		{"negative", -4, -2048},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &Config{Server: ServerConfig{
				HostCapacityCores:    &tc.cores,
				HostCapacityMemoryMB: &tc.memory,
			}}
			if v := c.HostCapacityCores(); v != 0 {
				t.Fatalf("HostCapacityCores(%v) = %v, want 0", tc.cores, v)
			}
			if v := c.HostCapacityMemoryMB(); v != 0 {
				t.Fatalf("HostCapacityMemoryMB(%v) = %v, want 0", tc.memory, v)
			}
		})
	}
}

func TestHostCapacityYAMLKeys(t *testing.T) {
	var cfg Config
	src := "server:\n  host_capacity_cores: 3.5\n  host_capacity_memory_mb: 6144\n"
	if err := yaml.Unmarshal([]byte(src), &cfg); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	if v := cfg.HostCapacityCores(); v != 3.5 {
		t.Fatalf("HostCapacityCores from YAML = %v, want 3.5", v)
	}
	if v := cfg.HostCapacityMemoryMB(); v != 6144 {
		t.Fatalf("HostCapacityMemoryMB from YAML = %v, want 6144", v)
	}
}

// applyEnv reads an explicit allowlist of variable names; a name that is not on
// it is ignored in silence, so the override would look applied and do nothing.
func TestHostCapacityEnvOverrides(t *testing.T) {
	t.Setenv("SHINYHUB_HOST_CAPACITY_CORES", "2.5")
	t.Setenv("SHINYHUB_HOST_CAPACITY_MEMORY_MB", "4096")

	cfg := &Config{}
	if err := applyEnv(cfg); err != nil {
		t.Fatalf("applyEnv: %v", err)
	}
	if v := cfg.HostCapacityCores(); v != 2.5 {
		t.Fatalf("HostCapacityCores from env = %v, want 2.5", v)
	}
	if v := cfg.HostCapacityMemoryMB(); v != 4096 {
		t.Fatalf("HostCapacityMemoryMB from env = %v, want 4096", v)
	}
}

// A value that is not a number is an operator error worth refusing to start
// over, not something to silently drop back to autodetection.
func TestHostCapacityEnvRejectsNonNumeric(t *testing.T) {
	for _, tc := range []struct{ key, val string }{
		{"SHINYHUB_HOST_CAPACITY_CORES", "many"},
		{"SHINYHUB_HOST_CAPACITY_MEMORY_MB", "8gb"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			t.Setenv(tc.key, tc.val)
			cfg := &Config{}
			if err := applyEnv(cfg); err == nil {
				t.Fatalf("expected error for %s=%q, got nil", tc.key, tc.val)
			}
		})
	}
}
