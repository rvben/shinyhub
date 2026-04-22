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
