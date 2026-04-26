package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestConfigPath_UsesShinyhubDir(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	got := configPath()
	want := filepath.Join("/home/user", ".config", "shinyhub", "config.json")
	if got != want {
		t.Fatalf("configPath() = %q, want %q", got, want)
	}
}

func TestLoadConfig_ErrorMentionsShinyhub(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, err := loadConfig()
	if err == nil {
		t.Fatal("expected error when config missing")
	}
	if !strings.Contains(err.Error(), "shinyhub login") {
		t.Fatalf("error %q should mention `shinyhub login`", err.Error())
	}
}

func TestAddCommandsTo_RegistersAllSubcommands(t *testing.T) {
	parent := &cobra.Command{Use: "parent"}
	AddCommandsTo(parent)

	wantSubcommands := []string{"login", "deploy", "apps", "tokens", "env", "data", "schedule", "share"}
	have := make(map[string]bool)
	for _, sub := range parent.Commands() {
		have[sub.Name()] = true
	}
	for _, name := range wantSubcommands {
		if !have[name] {
			t.Errorf("AddCommandsTo did not register %q; registered = %v", name, parent.Commands())
		}
	}
}

func TestSetVersion_PropagatesToRootCmd(t *testing.T) {
	// SetVersion should update the internal version for commands that read it.
	SetVersion("v9.9.9-test")
	if version != "v9.9.9-test" {
		t.Fatalf("SetVersion did not update version, got %q", version)
	}
}

func TestConfigPath_HonorsOverride(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Setenv("SHINYHUB_CONFIG", "")
	configPathOverride = "/tmp/custom.json"
	t.Cleanup(func() { configPathOverride = "" })

	if got, want := configPath(), "/tmp/custom.json"; got != want {
		t.Fatalf("configPath() = %q, want %q (override)", got, want)
	}
}

func TestConfigPath_HonorsEnvVar(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Setenv("SHINYHUB_CONFIG", "/etc/ci/shinyhub.json")
	configPathOverride = ""

	if got, want := configPath(), "/etc/ci/shinyhub.json"; got != want {
		t.Fatalf("configPath() = %q, want %q (env)", got, want)
	}
}

func TestConfigPath_FlagOverridesEnv(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Setenv("SHINYHUB_CONFIG", "/etc/ci/shinyhub.json")
	configPathOverride = "/explicit/flag.json"
	t.Cleanup(func() { configPathOverride = "" })

	if got, want := configPath(), "/explicit/flag.json"; got != want {
		t.Fatalf("flag must beat env: configPath() = %q, want %q", got, want)
	}
}

// LoadConfig must accept SHINYHUB_HOST + SHINYHUB_TOKEN with no on-disk
// config file at all — this is the CI / one-off scripting path.
func TestLoadConfig_FromEnvOnly(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SHINYHUB_HOST", "https://ci.example.com")
	t.Setenv("SHINYHUB_TOKEN", "shk_ci")
	t.Setenv("SHINYHUB_CONFIG", "")
	configPathOverride = ""

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig from env: %v", err)
	}
	if cfg.Host != "https://ci.example.com" || cfg.Token != "shk_ci" {
		t.Errorf("got %+v, want host/token from env", cfg)
	}
}

// SHINYHUB_HOST should override the host saved in the config while still
// reusing the saved token. This is the "target a different server" case.
func TestLoadConfig_EnvOverridesFileHost(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SHINYHUB_HOST", "https://other.example.com")
	t.Setenv("SHINYHUB_TOKEN", "")
	t.Setenv("SHINYHUB_CONFIG", "")
	configPathOverride = ""

	if err := saveConfig(&cliConfig{Host: "https://saved.example.com", Token: "shk_saved"}); err != nil {
		t.Fatalf("save config: %v", err)
	}
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Host != "https://other.example.com" {
		t.Errorf("env should override host, got %q", cfg.Host)
	}
	if cfg.Token != "shk_saved" {
		t.Errorf("token should fall through from file, got %q", cfg.Token)
	}
}

// AddCommandsTo must register the --config persistent flag so every subcommand
// inherits it. Without that, only `login` could be retargeted at a different
// credentials file.
func TestAddCommandsTo_RegistersConfigFlag(t *testing.T) {
	parent := &cobra.Command{Use: "parent"}
	AddCommandsTo(parent)
	f := parent.PersistentFlags().Lookup("config")
	if f == nil {
		t.Fatalf("AddCommandsTo did not register --config persistent flag")
	}
}

// TestAddCommandsTo_RegistersLogout makes sure the logout command is wired
// onto the root.
func TestAddCommandsTo_RegistersLogout(t *testing.T) {
	parent := &cobra.Command{Use: "parent"}
	AddCommandsTo(parent)
	for _, sub := range parent.Commands() {
		if sub.Name() == "logout" {
			return
		}
	}
	t.Fatalf("AddCommandsTo did not register `logout` subcommand")
}
