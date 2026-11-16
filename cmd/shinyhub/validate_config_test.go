package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/rvben/shinyhub/internal/cli"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/spf13/cobra"
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
	blocked := filepath.Join(dir, "blocked.yaml")
	trusted := filepath.Join(dir, "trusted.yaml")
	invalid := filepath.Join(dir, "invalid.yaml")
	malformed := filepath.Join(dir, "malformed.yaml")
	for path, body := range map[string]string{
		blocked:   "server:\n  host_capacity_cores: 1\n  update_capacity:\n    worker_cpu_request_cores: 1\n",
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
		name        string
		args        []string
		envPath     string
		origin      string
		wantError   string
		wantWarning bool
	}{
		{name: "impossible surge budget", args: []string{"validate-config", "--config", blocked}, wantWarning: true},
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
					Status   string   `json:"status"`
					Valid    bool     `json:"valid"`
					Warnings []string `json:"warnings"`
				}
				if err != nil || json.Unmarshal(output.Bytes(), &result) != nil || result.Status != "valid" || !result.Valid {
					t.Fatalf("expected validation success; error=%v output=%s", err, output.String())
				}
			}
			if tc.wantWarning && !strings.Contains(output.String(), "cannot admit even one surge worker") {
				t.Fatalf("missing capacity warning: %s", output.String())
			}
			if strings.Contains(output.String(), secret) || (err != nil && strings.Contains(err.Error(), secret)) {
				t.Fatal("validation exposed the auth secret")
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 5 {
				t.Fatalf("validation modified the working directory: entries=%v error=%v", entries, err)
			}
		})
	}
}

func TestValidateConfigOIDCDiscoveryOptIn(t *testing.T) {
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "SHINYHUB_") {
			t.Setenv(name, "")
		}
	}
	t.Setenv("SHINYHUB_AUTH_SECRET", "validation-test-secret-at-least-32-characters")
	var requests atomic.Int32
	var issuer atomic.Value
	issuer.Store("")
	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		value := issuer.Load().(string)
		fmt.Fprintf(w, `{"issuer":%q,"authorization_endpoint":%q,"token_endpoint":%q,"jwks_uri":%q}`, value, value+"/authorize", value+"/token", value+"/keys")
	}))
	defer idp.Close()
	issuer.Store(idp.URL)
	path := filepath.Join(t.TempDir(), "oidc.yaml")
	if err := os.WriteFile(path, []byte(fmt.Sprintf("oauth:\n  oidc:\n    issuer_url: %s\n    client_id: client\n    client_secret: secret\n    callback_url: https://hub.example/api/auth/oidc/callback\n", issuer.Load().(string))), 0600); err != nil {
		t.Fatal(err)
	}
	previousPath, previousCheck := configPath, checkOIDC
	defer func() { configPath = previousPath; checkOIDC = previousCheck }()
	configPath = path
	for _, online := range []bool{false, true} {
		checkOIDC = online
		var output bytes.Buffer
		cmd := &cobra.Command{}
		cmd.SetContext(context.Background())
		cmd.SetOut(&output)
		if err := validateConfigCmd.RunE(cmd, nil); err != nil {
			t.Fatal(err)
		}
		if !online && requests.Load() != 0 {
			t.Fatal("offline validation contacted IdP")
		}
	}
	if requests.Load() != 1 {
		t.Fatalf("discovery requests=%d", requests.Load())
	}
	issuer.Store("https://wrong-issuer.example")
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	if err := validateConfigCmd.RunE(cmd, nil); err == nil {
		t.Fatal("mismatched issuer accepted")
	}
}

func TestOIDCRebindRequiresOfflineAcknowledgement(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().Bool("acknowledge-servers-stopped", false, "")
	if err := oidcRebindCmd.RunE(cmd, nil); err == nil || !strings.Contains(err.Error(), "stop every server") {
		t.Fatalf("missing offline guard: %v", err)
	}
}

func TestOIDCRecoveryUsesExistingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recovery.sqlite")
	dsn, err := oidcRecoveryDSN(path)
	if err != nil {
		t.Fatal(err)
	}
	if s, err := db.Open(dsn); err == nil {
		s.Close()
		t.Fatal("recovery created a missing database")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("missing target created: %v", err)
	}
	store, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	if err := store.BindOIDCProvider("https://old", "client"); err != nil {
		t.Fatal(err)
	}
	store.Close()
	recovered, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	if err := recovered.CheckOIDCProvider("https://old", "client"); err != nil {
		t.Fatal("recovery opened different database:", err)
	}
}

func TestOIDCRecoveryRelativeDatabase(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	for _, path := range []string{"recovery.sqlite", "./sub/recovery.sqlite"} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		recoveryDSN, err := oidcRecoveryDSN(path)
		if err != nil {
			t.Fatal(err)
		}
		if s, err := db.Open(recoveryDSN); err == nil {
			s.Close()
			t.Fatalf("created missing relative database %s", path)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("missing database created: %v", err)
		}
		store, err := db.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Migrate(); err != nil {
			t.Fatal(err)
		}
		if err := store.BindOIDCProvider("https://relative", "client"); err != nil {
			t.Fatal(err)
		}
		store.Close()
		recovered, err := db.Open(recoveryDSN)
		if err != nil {
			t.Fatal(err)
		}
		if err := recovered.CheckOIDCProvider("https://relative", "client"); err != nil {
			t.Fatal(err)
		}
		var fk int
		if err := recovered.DB().QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil || fk != 1 {
			t.Fatalf("foreign keys disabled: %d %v", fk, err)
		}
		var busy int
		if err := recovered.DB().QueryRow("PRAGMA busy_timeout").Scan(&busy); err != nil || busy <= 0 {
			t.Fatalf("busy timeout missing: %d %v", busy, err)
		}
		recovered.Close()
	}
}
