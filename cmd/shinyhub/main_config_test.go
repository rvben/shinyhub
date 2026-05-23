package main

import "testing"

// TestServerConfigPath_FlagWins asserts the --config flag value takes
// precedence over the SHINYHUB_CONFIG env var and the default.
func TestServerConfigPath_FlagWins(t *testing.T) {
	t.Setenv("SHINYHUB_CONFIG", "/from/env.yaml")
	prev := configPath
	configPath = "/from/flag.yaml"
	t.Cleanup(func() { configPath = prev })

	if got := serverConfigPath(); got != "/from/flag.yaml" {
		t.Fatalf("serverConfigPath() = %q, want the --config flag value", got)
	}
}

// TestServerConfigPath_EnvWhenNoFlag asserts SHINYHUB_CONFIG is used when the
// flag is unset.
func TestServerConfigPath_EnvWhenNoFlag(t *testing.T) {
	t.Setenv("SHINYHUB_CONFIG", "/from/env.yaml")
	prev := configPath
	configPath = ""
	t.Cleanup(func() { configPath = prev })

	if got := serverConfigPath(); got != "/from/env.yaml" {
		t.Fatalf("serverConfigPath() = %q, want the SHINYHUB_CONFIG value", got)
	}
}

// TestServerConfigPath_DefaultWhenNothingSet asserts the ./shinyhub.yaml
// fallback when neither flag nor env is set.
func TestServerConfigPath_DefaultWhenNothingSet(t *testing.T) {
	t.Setenv("SHINYHUB_CONFIG", "")
	prev := configPath
	configPath = ""
	t.Cleanup(func() { configPath = prev })

	if got := serverConfigPath(); got != "shinyhub.yaml" {
		t.Fatalf("serverConfigPath() = %q, want the default shinyhub.yaml", got)
	}
}

// TestServeCmd_HasConfigFlag asserts the serve command exposes --config so the
// flag is discoverable and the documented ergonomic actually works.
func TestServeCmd_HasConfigFlag(t *testing.T) {
	if serveCmd.Flags().Lookup("config") == nil {
		t.Fatal("serve command must expose a --config flag")
	}
}
