package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

// TestServeCmd_ConfigFlagPositionIndependent guards against the latent
// footgun of two `--config` flags sharing a name: the root persistent client
// credentials flag (from cli.AddCommandsTo) and serve's own local server-config
// flag. The local flag must win for `serve` in BOTH `serve --config X` and
// `shinyhub --config X serve`, so the flag's position can never silently start
// the server against the wrong config. The test drives the real command tree
// end to end and asserts the exact file is loaded by giving it a sentinel
// runtime.mode that config validation rejects by name.
func TestServeCmd_ConfigFlagPositionIndependent(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "server.yaml")
	const sentinel = "SENTINEL_MODE_FROM_FLAG_FILE"
	content := "auth:\n  secret: " + strings.Repeat("a", 32) + "\nruntime:\n  mode: " + sentinel + "\n"
	if err := os.WriteFile(cfgFile, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	// A missing/unspecified config must NOT resolve to this file, so the
	// sentinel error proves the flag value (not a default) was loaded.
	t.Setenv("SHINYHUB_CONFIG", "")
	// config.Load applies SHINYHUB_RUNTIME_MODE after reading the YAML. If the
	// host environment has it set to a valid mode, it would overwrite the
	// sentinel and let runServe start the real server (hang). Clear it so the
	// sentinel runtime.mode in the file is what config validation rejects.
	t.Setenv("SHINYHUB_RUNTIME_MODE", "")

	cases := []struct {
		name string
		args []string
	}{
		{"flag after subcommand", []string{"serve", "--config", cfgFile}},
		{"flag before subcommand", []string{"--config", cfgFile, "serve"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prev := configPath
			configPath = ""
			t.Cleanup(func() {
				configPath = prev
				buildRoot().SetArgs(nil)
			})
			buildRoot().SetArgs(tc.args)
			err := buildRoot().Execute()
			if err == nil || !strings.Contains(err.Error(), sentinel) {
				t.Fatalf("[%s] expected the flag's config file to be loaded "+
					"(error naming %q), got: %v", tc.name, sentinel, err)
			}
		})
	}
}
