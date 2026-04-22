package cli

import (
	"path/filepath"
	"strings"
	"testing"
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
