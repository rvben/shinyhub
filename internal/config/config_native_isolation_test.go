package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/config"
)

func writeIsolationCfg(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	full := "auth:\n  secret: \"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx\"\n" + body
	if err := os.WriteFile(p, []byte(full), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// Isolation defaults to "off" (the historical native behavior) when unset.
func TestRuntimeNativeIsolation_DefaultsOff(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Runtime.Native.Isolation != "off" {
		t.Errorf("default isolation = %q, want off", cfg.Runtime.Native.Isolation)
	}
}

func TestRuntimeNativeIsolation_YAML(t *testing.T) {
	p := writeIsolationCfg(t, "runtime:\n  native:\n    isolation: standard\n")
	cfg, err := config.Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Runtime.Native.Isolation != "standard" {
		t.Errorf("isolation = %q, want standard", cfg.Runtime.Native.Isolation)
	}
}

func TestRuntimeNativeIsolation_Env(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	t.Setenv("SHINYHUB_RUNTIME_NATIVE_ISOLATION", "standard")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Runtime.Native.Isolation != "standard" {
		t.Errorf("env isolation = %q, want standard", cfg.Runtime.Native.Isolation)
	}
}

func TestRuntimeNativeIsolation_RejectsInvalid(t *testing.T) {
	p := writeIsolationCfg(t, "runtime:\n  native:\n    isolation: bogus\n")
	if _, err := config.Load(p); err == nil {
		t.Fatal("invalid isolation level must be rejected")
	}
}

// strict is reserved but not yet implemented; it must be rejected loudly, not
// silently treated as standard.
func TestRuntimeNativeIsolation_RejectsStrictForNow(t *testing.T) {
	p := writeIsolationCfg(t, "runtime:\n  native:\n    isolation: strict\n")
	_, err := config.Load(p)
	if err == nil || !strings.Contains(err.Error(), "strict") {
		t.Fatalf("strict must be rejected as not-yet-implemented, got %v", err)
	}
}
