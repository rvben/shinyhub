package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
	"github.com/spf13/cobra"
)

func setupTestCommand(input string) (*cobra.Command, *bytes.Buffer) {
	cmd := &cobra.Command{}
	cmd.SetIn(strings.NewReader(input))
	out := new(bytes.Buffer)
	cmd.SetErr(out)
	return cmd, out
}

func setupTestEnv(t *testing.T, databasePath string) {
	t.Helper()
	t.Setenv("SHINYHUB_CONFIG", "")
	t.Setenv("SHINYHUB_AUTH_SECRET", "")
	t.Setenv("SHINYHUB_ADMIN_USER", "")
	t.Setenv("SHINYHUB_ADMIN_PASSWORD", "")
	t.Setenv("SHINYHUB_DB_DSN", databasePath)
}

func setSetupTTY(t *testing.T, tty bool, passwords ...string) {
	t.Helper()
	previousTTY := setupIsStdinTTY
	previousReadPassword := setupReadPassword
	setupIsStdinTTY = func() bool { return tty }
	index := 0
	setupReadPassword = func() (string, error) {
		if index >= len(passwords) {
			t.Fatalf("setup requested more passwords than the test supplied")
		}
		password := passwords[index]
		index++
		return password, nil
	}
	t.Cleanup(func() {
		setupIsStdinTTY = previousTTY
		setupReadPassword = previousReadPassword
	})
}

func TestRunSetupFreshNonInteractive(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "shinyhub.yaml")
	databasePath := filepath.Join(dir, "state", "shinyhub.db")
	setupTestEnv(t, databasePath)
	t.Setenv("SHINYHUB_ADMIN_USER", "owner")
	t.Setenv("SHINYHUB_ADMIN_PASSWORD", "correct-horse-password")
	setSetupTTY(t, false)

	cmd, _ := setupTestCommand("")
	result, err := runSetup(cmd, &setupFlags{configPath: configPath})
	if err != nil {
		t.Fatalf("runSetup: %v", err)
	}
	if !result.CreatedConfig || !result.CreatedAdmin || result.Username != "owner" {
		t.Fatalf("unexpected result: %+v", result)
	}

	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("config permissions = %o, want 600", got)
	}
	dbInfo, err := os.Stat(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if got := dbInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("database permissions = %o, want 600", got)
	}
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), "correct-horse-password") {
		t.Fatal("generated config must not contain the administrator password")
	}
	if !strings.Contains(string(content), "secret:") {
		t.Fatalf("generated config has no auth secret: %s", content)
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("load generated config: %v", err)
	}
	if len(cfg.Auth.Secret) != 64 {
		t.Fatalf("generated secret length = %d, want 64", len(cfg.Auth.Secret))
	}
	if cfg.Server.Host != "127.0.0.1" {
		t.Fatalf("generated server host = %q, want loopback", cfg.Server.Host)
	}
	store, err := db.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	user, err := store.GetUserByUsername("owner")
	if err != nil {
		t.Fatalf("get administrator: %v", err)
	}
	if user.Role != "admin" {
		t.Fatalf("administrator role = %q, want admin", user.Role)
	}
	if err := auth.VerifyPassword(user.PasswordHash, "correct-horse-password"); err != nil {
		t.Fatalf("administrator password was not stored correctly: %v", err)
	}
}

func TestRunSetupIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "shinyhub.yaml")
	databasePath := filepath.Join(dir, "shinyhub.db")
	setupTestEnv(t, databasePath)
	t.Setenv("SHINYHUB_ADMIN_USER", "admin")
	t.Setenv("SHINYHUB_ADMIN_PASSWORD", "first-password-long")
	setSetupTTY(t, false)
	cmd, _ := setupTestCommand("")

	first, err := runSetup(cmd, &setupFlags{configPath: configPath})
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}

	t.Setenv("SHINYHUB_ADMIN_USER", "replacement")
	t.Setenv("SHINYHUB_ADMIN_PASSWORD", "replacement-password")
	second, err := runSetup(cmd, &setupFlags{configPath: configPath})
	if err != nil {
		t.Fatal(err)
	}
	if !first.CreatedConfig || !first.CreatedAdmin {
		t.Fatalf("first setup did not create expected state: %+v", first)
	}
	if second.CreatedConfig || second.CreatedAdmin || second.Username != "admin" {
		t.Fatalf("second setup was not idempotent: %+v", second)
	}
	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("rerunning setup changed the existing configuration")
	}
}

func TestRunSetupRepairsDatabaseWithoutAdministrator(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "shinyhub.yaml")
	databasePath := filepath.Join(dir, "shinyhub.db")
	setupTestEnv(t, databasePath)
	t.Setenv("SHINYHUB_AUTH_SECRET", strings.Repeat("s", 32))
	t.Setenv("SHINYHUB_ADMIN_USER", "viewer")
	t.Setenv("SHINYHUB_ADMIN_PASSWORD", "repair-password")
	setSetupTTY(t, false)

	store, err := db.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateUser(db.CreateUserParams{Username: "viewer", PasswordHash: "hash", Role: "viewer"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	cmd, _ := setupTestCommand("")
	if _, err := runSetup(cmd, &setupFlags{configPath: configPath}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("existing non-admin username should get an actionable error, got %v", err)
	}

	t.Setenv("SHINYHUB_ADMIN_USER", "new-admin")
	result, err := runSetup(cmd, &setupFlags{configPath: configPath})
	if err != nil {
		t.Fatalf("repair setup: %v", err)
	}
	if !result.CreatedAdmin || result.Username != "new-admin" {
		t.Fatalf("repair result: %+v", result)
	}
	store, err = db.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	admin, err := store.GetUserByUsername("new-admin")
	if err != nil {
		t.Fatal(err)
	}
	if admin.Role != "admin" {
		t.Fatalf("repaired user role = %q, want admin", admin.Role)
	}
}

func TestRunSetupInteractiveDefaultsAndRetriesPassword(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "shinyhub.yaml")
	databasePath := filepath.Join(dir, "shinyhub.db")
	setupTestEnv(t, databasePath)
	setSetupTTY(t, true,
		"short",                   // rejected
		"first-password-long",     // first attempt
		"different-password-long", // mismatch
		"confirmed-password",      // second attempt
		"confirmed-password",      // confirmation
	)
	cmd, output := setupTestCommand("\n")

	result, err := runSetup(cmd, &setupFlags{configPath: configPath})
	if err != nil {
		t.Fatalf("runSetup: %v", err)
	}
	if result.Username != "admin" || !result.CreatedAdmin {
		t.Fatalf("interactive setup result: %+v", result)
	}
	got := output.String()
	for _, want := range []string{
		"Administrator username [admin]",
		"must be at least 15 characters",
		"Passwords do not match",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("interactive output missing %q:\n%s", want, got)
		}
	}
}

func TestRunSetupNonInteractiveRequiresCredentials(t *testing.T) {
	dir := t.TempDir()
	setupTestEnv(t, filepath.Join(dir, "shinyhub.db"))
	setSetupTTY(t, false)
	cmd, _ := setupTestCommand("")

	_, err := runSetup(cmd, &setupFlags{configPath: filepath.Join(dir, "shinyhub.yaml")})
	if err == nil || !strings.Contains(err.Error(), "unattended setup") {
		t.Fatalf("expected actionable unattended-setup error, got %v", err)
	}
}

func TestRunSetupRefusesToReplaceUnknownSecret(t *testing.T) {
	dir := t.TempDir()
	databasePath := filepath.Join(dir, "shinyhub.db")
	setupTestEnv(t, databasePath)
	if err := os.WriteFile(databasePath, []byte("existing state"), 0o600); err != nil {
		t.Fatal(err)
	}
	setSetupTTY(t, false)
	cmd, _ := setupTestCommand("")

	_, err := runSetup(cmd, &setupFlags{configPath: filepath.Join(dir, "missing.yaml")})
	if err == nil || !strings.Contains(err.Error(), "refusing to generate a replacement secret") {
		t.Fatalf("expected existing-data safety error, got %v", err)
	}
}

func TestMaybeRunInteractiveSetup(t *testing.T) {
	dir := t.TempDir()
	configFile := filepath.Join(dir, "shinyhub.yaml")
	setupTestEnv(t, filepath.Join(dir, "shinyhub.db"))
	setSetupTTY(t, true, "setup-password-long", "setup-password-long")
	previousConfigPath := configPath
	configPath = configFile
	t.Cleanup(func() { configPath = previousConfigPath })
	cmd, output := setupTestCommand("\n")

	result, err := maybeRunInteractiveSetup(cmd)
	if err != nil {
		t.Fatalf("maybeRunInteractiveSetup: %v", err)
	}
	if result == nil || !result.CreatedConfig || !result.CreatedAdmin {
		t.Fatalf("interactive setup result = %+v, want freshly initialized setup", result)
	}
	got := output.String()
	for _, want := range []string{"Welcome to ShinyHub", "Administrator \"admin\" created", "Starting ShinyHub"} {
		if !strings.Contains(got, want) {
			t.Fatalf("setup output missing %q:\n%s", want, got)
		}
	}
	if _, err := os.Stat(configFile); err != nil {
		t.Fatalf("interactive serve setup did not create config: %v", err)
	}
}

func TestMaybeRunInteractiveSetupNonTTYExplainsBothPaths(t *testing.T) {
	dir := t.TempDir()
	setupTestEnv(t, filepath.Join(dir, "shinyhub.db"))
	setSetupTTY(t, false)
	previousConfigPath := configPath
	configPath = filepath.Join(dir, "missing.yaml")
	t.Cleanup(func() { configPath = previousConfigPath })
	cmd, _ := setupTestCommand("")

	_, err := maybeRunInteractiveSetup(cmd)
	if err == nil || !strings.Contains(err.Error(), "shinyhub init") || !strings.Contains(err.Error(), "SHINYHUB_AUTH_SECRET") {
		t.Fatalf("expected interactive and unattended recovery paths, got %v", err)
	}
}

func TestEnsureUsableFirstLogin(t *testing.T) {
	store := dbtest.New(t)
	cfg := &config.Config{}

	err := ensureUsableFirstLogin(cfg, store, defaultServerConfigPath)
	if err == nil || !strings.Contains(err.Error(), "shinyhub init") {
		t.Fatalf("empty local-login database should fail with recovery, got %v", err)
	}
	if _, err := store.UpsertSystemUser(db.SystemUsernameDeploy, "developer"); err != nil {
		t.Fatal(err)
	}
	if err := ensureUsableFirstLogin(cfg, store, "/etc/shinyhub.yaml"); err == nil || !strings.Contains(err.Error(), "--config '/etc/shinyhub.yaml'") {
		t.Fatalf("system user must not make local login usable, got %v", err)
	}
	if err := store.CreateUser(db.CreateUserParams{Username: "human", PasswordHash: "hash", Role: "viewer"}); err != nil {
		t.Fatal(err)
	}
	if err := ensureUsableFirstLogin(cfg, store, defaultServerConfigPath); err == nil {
		t.Fatal("a viewer must not satisfy the local-administrator guard")
	}
	hash, err := auth.HashPassword("admin-password")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	if err := ensureUsableFirstLogin(cfg, store, defaultServerConfigPath); err != nil {
		t.Fatalf("password-backed administrator should make login usable: %v", err)
	}
}
