package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rvben/shinyhub/internal/config"
)

const buildTestSecret = "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"

// TestLoad_BuildFromYAML proves the documented build block is loaded from YAML,
// mirroring uv's three interpreter knobs.
func TestLoad_BuildFromYAML(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", buildTestSecret)
	dir := t.TempDir()
	path := filepath.Join(dir, "shinyhub.yaml")
	body := "build:\n" +
		"  python_preference: only-system\n" +
		"  python: \"3.12\"\n" +
		"  python_install_mirror: https://mirror.example.com/pbs\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Build.PythonPreference != "only-system" {
		t.Errorf("PythonPreference = %q, want only-system", cfg.Build.PythonPreference)
	}
	if cfg.Build.Python != "3.12" {
		t.Errorf("Python = %q, want 3.12", cfg.Build.Python)
	}
	if cfg.Build.PythonInstallMirror != "https://mirror.example.com/pbs" {
		t.Errorf("PythonInstallMirror = %q", cfg.Build.PythonInstallMirror)
	}
	if !cfg.Build.IsActive() {
		t.Error("IsActive should be true when any field is set")
	}
}

// TestBuild_UVBuildEnv checks the KEY=value projection includes only non-empty
// fields, in the fixed uv-variable order.
func TestBuild_UVBuildEnv(t *testing.T) {
	b := config.BuildConfig{PythonPreference: "managed", PythonInstallMirror: "https://m/x"}
	got := b.UVBuildEnv()
	want := []string{"UV_PYTHON_PREFERENCE=managed", "UV_PYTHON_INSTALL_MIRROR=https://m/x"}
	if len(got) != len(want) {
		t.Fatalf("UVBuildEnv = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("UVBuildEnv[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	var empty config.BuildConfig
	if empty.IsActive() {
		t.Error("empty BuildConfig must not be active")
	}
	if len(empty.UVBuildEnv()) != 0 {
		t.Error("empty BuildConfig must project to no env vars")
	}
}

// TestBuild_InvalidPreferenceRejected pins that a typo'd preference fails config
// load rather than being exported to uv verbatim and rejected per app at build
// time.
func TestBuild_InvalidPreferenceRejected(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", buildTestSecret)
	dir := t.TempDir()
	path := filepath.Join(dir, "shinyhub.yaml")
	if err := os.WriteFile(path, []byte("build:\n  python_preference: only_system\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(path); err == nil {
		t.Fatal("Load should reject an invalid build.python_preference")
	}
}

// TestBuild_EnvOverride proves the env vars override the YAML value, matching
// the applyEnv precedence used across the config.
func TestBuild_EnvOverride(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", buildTestSecret)
	t.Setenv("SHINYHUB_BUILD_PYTHON_PREFERENCE", "system")
	t.Setenv("SHINYHUB_BUILD_PYTHON", "3.11")
	t.Setenv("SHINYHUB_BUILD_PYTHON_INSTALL_MIRROR", "https://env-mirror.example.com")
	dir := t.TempDir()
	path := filepath.Join(dir, "shinyhub.yaml")
	if err := os.WriteFile(path, []byte("build:\n  python_preference: only-system\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Build.PythonPreference != "system" {
		t.Errorf("env should override YAML: PythonPreference = %q, want system", cfg.Build.PythonPreference)
	}
	if cfg.Build.Python != "3.11" {
		t.Errorf("Python = %q, want 3.11", cfg.Build.Python)
	}
	if cfg.Build.PythonInstallMirror != "https://env-mirror.example.com" {
		t.Errorf("PythonInstallMirror = %q", cfg.Build.PythonInstallMirror)
	}
}

// TestBuild_ApplyToEnv proves that a configured field overwrites the process
// environment (config authoritative) while an unset field leaves an existing
// service-env value untouched, so both mechanisms feed the same allow-list.
func TestBuild_ApplyToEnv(t *testing.T) {
	t.Setenv("UV_PYTHON_PREFERENCE", "managed") // inherited service env
	t.Setenv("UV_PYTHON", "3.9")                // inherited, left alone by empty config field
	t.Setenv("UV_PYTHON_INSTALL_MIRROR", "")    // unset

	b := config.BuildConfig{PythonPreference: "only-system"} // only the preference is configured
	if err := b.ApplyToEnv(); err != nil {
		t.Fatalf("ApplyToEnv: %v", err)
	}
	if got := os.Getenv("UV_PYTHON_PREFERENCE"); got != "only-system" {
		t.Errorf("configured field must overwrite env: UV_PYTHON_PREFERENCE = %q, want only-system", got)
	}
	if got := os.Getenv("UV_PYTHON"); got != "3.9" {
		t.Errorf("unset config field must not touch env: UV_PYTHON = %q, want 3.9", got)
	}
}
