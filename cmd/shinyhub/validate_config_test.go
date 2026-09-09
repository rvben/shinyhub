package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/cli"
)

func TestValidateConfigCommand(t *testing.T) {
	// Exercise the real command tree: the client also has a --config flag.
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "SHINYHUB_") {
			t.Setenv(name, "")
		}
	}
	const secret = "validation-test-secret-at-least-32-characters"
	t.Setenv("SHINYHUB_AUTH_SECRET", secret)
	dir := t.TempDir()
	t.Chdir(dir)
	valid := filepath.Join(dir, "valid.yaml")
	trusted := filepath.Join(dir, "trusted.yaml")
	invalid := filepath.Join(dir, "invalid.yaml")
	malformed := filepath.Join(dir, "malformed.yaml")
	for path, body := range map[string]string{
		trusted:   "auth:\n  support_sessions: true\n  support_sessions_trusted_apps: true\nserver:\n  base_url: https://hub.example.com\n",
		valid:     "auth:\n  support_sessions: true\nserver:\n  base_url: https://hub.example.com\n  app_origin: https://apps.example.com\n",
		invalid:   "auth:\n  support_sessions: true\nserver:\n  base_url: https://hub.example.com\n  app_origin: https://hub.example.com:8443\n",
		malformed: "server: [\n",
	} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name      string
		args      []string
		envPath   string
		origin    string
		wantError string
	}{
		{name: "trusted apps preflight", args: []string{"validate-config", "--config", trusted}},
		{name: "flag after command", args: []string{"validate-config", "--config", valid}},
		{name: "flag before command", args: []string{"--config", valid, "validate-config"}},
		{name: "environment path", args: []string{"validate-config"}, envPath: valid},
		{name: "flag overrides environment", args: []string{"validate-config", "--config", valid}, envPath: invalid},
		{name: "environment overrides YAML", args: []string{"validate-config", "--config", invalid}, origin: "https://apps.example.com"},
		{name: "same host", args: []string{"validate-config", "--config", invalid}, wantError: "cookies are shared across ports"},
		{name: "malformed YAML", args: []string{"validate-config", "--config", malformed}, wantError: "parse config"},
		{name: "missing explicit file", args: []string{"validate-config", "--config", "missing.yaml"}, wantError: "missing.yaml"},
		{name: "missing environment file", args: []string{"validate-config"}, envPath: "missing-env.yaml", wantError: "missing-env.yaml"},
		{name: "environment only", args: []string{"validate-config"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SHINYHUB_CONFIG", tc.envPath)
			t.Setenv("SHINYHUB_APP_ORIGIN", tc.origin)
			root := buildRoot()
			prev := configPath
			configPath = ""
			flag := validateConfigCmd.Flags().Lookup("config")
			changed := flag.Changed
			flag.Changed = false
			var output bytes.Buffer
			root.SetOut(&output)
			root.SetErr(&output)
			outputFlag := root.PersistentFlags().Lookup("output")
			previousOutput, outputChanged := outputFlag.Value.String(), outputFlag.Changed
			root.SetArgs(append(append([]string{}, tc.args...), "--output", "json"))
			t.Cleanup(func() {
				configPath = prev
				flag.Changed = changed
				root.SetArgs(nil)
				root.SetOut(nil)
				root.SetErr(nil)
				_ = outputFlag.Value.Set(previousOutput)
				outputFlag.Changed = outputChanged
			})
			err := root.Execute()
			if tc.wantError != "" {
				var exitErr *cli.ExitCodeError
				if !errors.As(err, &exitErr) || exitErr.Code != 1 || exitErr.Kind != cli.KindValidation || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("expected validation failure containing %q; got %v", tc.wantError, err)
				}
				if strings.Contains(output.String(), `"valid":true`) {
					t.Fatal("failed validation reported success")
				}
			} else {
				var result struct {
					Status string `json:"status"`
					Valid  bool   `json:"valid"`
				}
				if err != nil || json.Unmarshal(output.Bytes(), &result) != nil || result.Status != "valid" || !result.Valid {
					t.Fatalf("expected validation success; error=%v output=%s", err, output.String())
				}
			}
			if strings.Contains(output.String(), secret) || (err != nil && strings.Contains(err.Error(), secret)) {
				t.Fatal("validation exposed the auth secret")
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 4 {
				t.Fatalf("validation modified the working directory: entries=%v error=%v", entries, err)
			}
		})
	}
}
