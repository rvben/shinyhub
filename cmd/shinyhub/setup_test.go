package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/auth"
	shinycli "github.com/rvben/shinyhub/internal/cli"
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
	t.Setenv("SHINYHUB_AUTH_SECRET_FILE", "")
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

	dbtest.WriteSQLiteFile(t, databasePath)
	store, err := db.Open(databasePath)
	if err != nil {
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

// The password prompt has to carry the length requirement itself. Supplying an
// acceptable password on the first try means the rejection message is never
// printed, so the requirement can only reach this output from the prompt.
func TestRunSetupPasswordPromptStatesTheMinimumUpFront(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "shinyhub.yaml")
	databasePath := filepath.Join(dir, "shinyhub.db")
	setupTestEnv(t, databasePath)
	setSetupTTY(t, true, "confirmed-password", "confirmed-password")
	cmd, output := setupTestCommand("\n")

	if _, err := runSetup(cmd, &setupFlags{configPath: configPath}); err != nil {
		t.Fatalf("runSetup: %v", err)
	}
	got := output.String()
	if strings.Contains(got, "must be at least") {
		t.Fatalf("a valid first password should not print the rejection message:\n%s", got)
	}
	want := fmt.Sprintf("Administrator password (at least %d characters):", auth.MinPasswordLength)
	if !strings.Contains(got, want) {
		t.Fatalf("password prompt missing %q:\n%s", want, got)
	}
}

// Every way `shinyhub init` can be given the wrong input is something the
// operator fixes by supplying something different, never a fault in ShinyHub.
// Reported as "internal", each one tells a supervisor or an agent caller that
// the run is worth retrying unchanged, which none of them ever is. The second
// assertion is the other bound: classifying the error must not rewrite the
// sentence the operator reads.
func TestRunSetupClassifiesOperatorInputAsValidation(t *testing.T) {
	cases := []struct {
		name    string
		arrange func(t *testing.T, dir string) *setupFlags
		want    string
	}{
		{
			name: "credentials missing entirely",
			arrange: func(t *testing.T, dir string) *setupFlags {
				return &setupFlags{}
			},
			want: "administrator credentials are required for unattended setup",
		},
		{
			name: "username is reserved",
			arrange: func(t *testing.T, dir string) *setupFlags {
				t.Setenv("SHINYHUB_ADMIN_USER", "__deploy__")
				t.Setenv("SHINYHUB_ADMIN_PASSWORD", "correct-horse-password")
				return &setupFlags{}
			},
			want: `administrator username "__deploy__" is reserved`,
		},
		{
			name: "username contains whitespace",
			arrange: func(t *testing.T, dir string) *setupFlags {
				t.Setenv("SHINYHUB_ADMIN_USER", "ad min")
				t.Setenv("SHINYHUB_ADMIN_PASSWORD", "correct-horse-password")
				return &setupFlags{}
			},
			want: "administrator username cannot contain whitespace",
		},
		{
			name: "password is too short",
			arrange: func(t *testing.T, dir string) *setupFlags {
				t.Setenv("SHINYHUB_ADMIN_USER", "owner")
				t.Setenv("SHINYHUB_ADMIN_PASSWORD", "short")
				return &setupFlags{}
			},
			want: fmt.Sprintf("administrator password must be at least %d characters", auth.MinPasswordLength),
		},
		{
			name: "auth secret is too short",
			arrange: func(t *testing.T, dir string) *setupFlags {
				t.Setenv("SHINYHUB_AUTH_SECRET", "tooshort")
				t.Setenv("SHINYHUB_ADMIN_USER", "owner")
				t.Setenv("SHINYHUB_ADMIN_PASSWORD", "correct-horse-password")
				return &setupFlags{}
			},
			want: "SHINYHUB_AUTH_SECRET must be at least 32 characters",
		},
		{
			name: "auth secret is the placeholder",
			arrange: func(t *testing.T, dir string) *setupFlags {
				t.Setenv("SHINYHUB_AUTH_SECRET", "change-me-to-a-random-string")
				t.Setenv("SHINYHUB_ADMIN_USER", "owner")
				t.Setenv("SHINYHUB_ADMIN_PASSWORD", "correct-horse-password")
				return &setupFlags{}
			},
			want: "SHINYHUB_AUTH_SECRET is the placeholder value",
		},
		{
			name: "the requested name belongs to a user who cannot administer",
			arrange: func(t *testing.T, dir string) *setupFlags {
				// A user that exists without the admin role leaves setup with a
				// name it cannot take and no administrator to fall back on.
				store, err := db.Open(filepath.Join(dir, "shinyhub.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer store.Close()
				if err := store.Migrate(); err != nil {
					t.Fatal(err)
				}
				hash, err := auth.HashPassword("correct-horse-password")
				if err != nil {
					t.Fatal(err)
				}
				if err := store.CreateUser(db.CreateUserParams{
					Username: "owner", PasswordHash: hash, Role: "developer",
				}); err != nil {
					t.Fatal(err)
				}
				t.Setenv("SHINYHUB_AUTH_SECRET", strings.Repeat("k", 32))
				t.Setenv("SHINYHUB_ADMIN_USER", "owner")
				t.Setenv("SHINYHUB_ADMIN_PASSWORD", "correct-horse-password")
				return &setupFlags{}
			},
			want: `user "owner" already exists but is not a usable local administrator`,
		},
		{
			name: "database exists but the secret that encrypted it does not",
			arrange: func(t *testing.T, dir string) *setupFlags {
				databasePath := filepath.Join(dir, "shinyhub.db")
				if err := os.WriteFile(databasePath, []byte("existing state"), 0o600); err != nil {
					t.Fatal(err)
				}
				return &setupFlags{}
			},
			want: "refusing to generate a replacement secret",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			setupTestEnv(t, filepath.Join(dir, "shinyhub.db"))
			setSetupTTY(t, false)
			f := tc.arrange(t, dir)
			f.configPath = filepath.Join(dir, "shinyhub.yaml")
			cmd, _ := setupTestCommand("")

			_, err := runSetup(cmd, f)
			if err == nil {
				t.Fatal("expected setup to fail")
			}
			var ece *shinycli.ExitCodeError
			if !errors.As(err, &ece) || ece.Kind != shinycli.KindValidation {
				t.Fatalf("error kind = %q (exit %d), want %q: %v",
					kindOrUnclassified(err), shinycli.ExitCode(err), shinycli.KindValidation, err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error message = %q, want it to contain %q", err.Error(), tc.want)
			}
			// Every one of these is fixed by supplying something different, so
			// every one owes the operator the remedy in the envelope's hint
			// field. A hint folded into the message reaches a reader who renders
			// the two separately as an empty hint, which is why this checks the
			// field rather than the sentence.
			var hinted interface{ Hint() string }
			if !errors.As(err, &hinted) || strings.TrimSpace(hinted.Hint()) == "" {
				t.Fatalf("error carries no hint, so the envelope's hint field is empty: %v", err)
			}
			if strings.Contains(err.Error(), hinted.Hint()) {
				t.Fatalf("hint %q is repeated inside the message %q; the remedy belongs in one place",
					hinted.Hint(), err.Error())
			}
		})
	}
}

func kindOrUnclassified(err error) shinycli.Kind {
	var ece *shinycli.ExitCodeError
	if errors.As(err, &ece) && ece.Kind != "" {
		return ece.Kind
	}
	return "unclassified"
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
	if !strings.Contains(err.Error(), "SHINYHUB_AUTH_SECRET_FILE") {
		t.Fatalf("unattended recovery must name the file form it recommends, got %v", err)
	}
	var ece *shinycli.ExitCodeError
	if !errors.As(err, &ece) || ece.Kind != shinycli.KindValidation {
		t.Fatalf("an uninitialized install is a setup state the operator fixes, want kind %q, got %v", shinycli.KindValidation, err)
	}
}

// A secret file configures the deployment exactly as the environment variable
// does, so serve must start rather than report an uninitialized install.
func TestMaybeRunInteractiveSetupAcceptsASecretFile(t *testing.T) {
	dir := t.TempDir()
	setupTestEnv(t, filepath.Join(dir, "shinyhub.db"))
	setSetupTTY(t, false)
	previousConfigPath := configPath
	configPath = filepath.Join(dir, "missing.yaml")
	t.Cleanup(func() { configPath = previousConfigPath })

	secretFile := filepath.Join(dir, "auth.secret")
	if err := os.WriteFile(secretFile, []byte("0123456789abcdef0123456789abcdef"), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd, _ := setupTestCommand("")
	if _, err := maybeRunInteractiveSetup(cmd); err == nil {
		t.Fatal("guard is inert: no secret at all should still report an uninitialized install")
	}

	t.Setenv("SHINYHUB_AUTH_SECRET_FILE", secretFile)
	result, err := maybeRunInteractiveSetup(cmd)
	if err != nil {
		t.Fatalf("a secret file must configure the deployment: %v", err)
	}
	if result != nil {
		t.Fatalf("a configured deployment wants nothing set up for it, got %+v", result)
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

// A supervisor reading the error envelope decides whether to restart the unit.
// "Internal" means the fault may be transient and a restart is worth trying;
// this one never clears without a human running init, so the kind is the part
// of the message that carries the operational instruction.
func TestEnsureUsableFirstLoginIsValidationNotInternal(t *testing.T) {
	store := dbtest.New(t)

	err := ensureUsableFirstLogin(&config.Config{}, store, defaultServerConfigPath)
	if err == nil {
		t.Fatal("expected the no-administrator guard to fail")
	}
	var ece *shinycli.ExitCodeError
	if !errors.As(err, &ece) {
		t.Fatalf("error carries no kind at all (%T: %v); it would fall through to the internal catch-all", err, err)
	}
	if ece.Kind != shinycli.KindValidation {
		t.Errorf("kind = %q, want %q", ece.Kind, shinycli.KindValidation)
	}
	if ece.Code != 1 {
		t.Errorf("exit code = %d, want 1", ece.Code)
	}
}

// runBootstrapAdmin calls bootstrapAdminFromEnv with the environment the test
// set up and returns everything it logged, so an assertion can read what the
// operator would have been told.
func runBootstrapAdmin(t *testing.T, store *db.Store, localLoginEnabled bool) string {
	t.Helper()
	var logged bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug}))
	if err := bootstrapAdminFromEnv(store, localLoginEnabled, logger); err != nil {
		t.Fatalf("bootstrapAdminFromEnv: %v", err)
	}
	return logged.String()
}

const conformingBootstrapPassword = "correct-horse-battery-staple"

func TestBootstrapAdminWarnsAboutAWeakPassword(t *testing.T) {
	if len(conformingBootstrapPassword) < auth.MinPasswordLength {
		t.Fatalf("the test's own conforming password is %d characters, below the %d-character minimum; the negative case below would pass for the wrong reason",
			len(conformingBootstrapPassword), auth.MinPasswordLength)
	}
	for _, tc := range []struct {
		name     string
		password string
		wantWarn bool
	}{
		{"weak", "admin", true},
		{"conforming", conformingBootstrapPassword, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := dbtest.New(t)
			setupTestEnv(t, "")
			t.Setenv("SHINYHUB_ADMIN_USER", "admin")
			t.Setenv("SHINYHUB_ADMIN_PASSWORD", tc.password)

			logged := runBootstrapAdmin(t, store, true)

			gotWarn := strings.Contains(logged, "weaker than ShinyHub accepts")
			if gotWarn != tc.wantWarn {
				t.Errorf("weak-password warning present = %v, want %v; an operator setting a password their own users would be refused is told nothing about it.\nlog:\n%s",
					gotWarn, tc.wantWarn, logged)
			}
			// The warning is advice, not a gate: the account must still exist,
			// or a local dev instance bootstrapped with admin/admin cannot log in.
			if _, err := store.GetUserByUsername("admin"); err != nil {
				t.Fatalf("admin was not created (%v); the warning turned into a refusal", err)
			}
		})
	}
}

func TestBootstrapAdminSaysItLeftAnExistingPasswordAlone(t *testing.T) {
	store := dbtest.New(t)
	setupTestEnv(t, "")
	t.Setenv("SHINYHUB_ADMIN_USER", "admin")
	t.Setenv("SHINYHUB_ADMIN_PASSWORD", conformingBootstrapPassword)

	runBootstrapAdmin(t, store, true)
	created, err := store.GetUserByUsername("admin")
	if err != nil {
		t.Fatalf("first bootstrap did not create admin: %v", err)
	}

	// A second run with a different password is the case an operator hits when
	// they change the env var and restart expecting the new password to work.
	t.Setenv("SHINYHUB_ADMIN_PASSWORD", conformingBootstrapPassword+"-rotated")
	logged := runBootstrapAdmin(t, store, true)

	if !strings.Contains(logged, "was not applied") {
		t.Errorf("nothing said the new SHINYHUB_ADMIN_PASSWORD was ignored; the login that then fails reads as a broken build.\nlog:\n%s", logged)
	}
	after, err := store.GetUserByUsername("admin")
	if err != nil {
		t.Fatalf("get admin after second bootstrap: %v", err)
	}
	if after.PasswordHash != created.PasswordHash {
		t.Errorf("the stored password changed, so the log line above is a lie")
	}
}

func TestBootstrapAdminWarnsWhenLocalLoginIsDisabled(t *testing.T) {
	for _, tc := range []struct {
		name              string
		localLoginEnabled bool
		wantWarn          bool
	}{
		{"local login off", false, true},
		{"local login on", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := dbtest.New(t)
			setupTestEnv(t, "")
			t.Setenv("SHINYHUB_ADMIN_USER", "admin")
			t.Setenv("SHINYHUB_ADMIN_PASSWORD", conformingBootstrapPassword)

			logged := runBootstrapAdmin(t, store, tc.localLoginEnabled)

			gotWarn := strings.Contains(logged, "local login is disabled")
			if gotWarn != tc.wantWarn {
				t.Errorf("local-login warning present = %v, want %v; the bootstrapped admin cannot sign in and nothing says so.\nlog:\n%s",
					gotWarn, tc.wantWarn, logged)
			}
		})
	}
}

func TestBootstrapAdminRequiresAPasswordWithTheUser(t *testing.T) {
	store := dbtest.New(t)
	setupTestEnv(t, "")
	t.Setenv("SHINYHUB_ADMIN_USER", "admin")
	t.Setenv("SHINYHUB_ADMIN_PASSWORD", "")

	err := bootstrapAdminFromEnv(store, true, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil {
		t.Fatal("an admin username with no password started the server anyway")
	}
	if !strings.Contains(err.Error(), "SHINYHUB_ADMIN_PASSWORD") {
		t.Errorf("error does not name the variable to set: %v", err)
	}
}

func TestBootstrapAdminIsANoOpWithoutTheUsername(t *testing.T) {
	store := dbtest.New(t)
	setupTestEnv(t, "")
	t.Setenv("SHINYHUB_ADMIN_USER", "")
	t.Setenv("SHINYHUB_ADMIN_PASSWORD", "ignored")

	if logged := runBootstrapAdmin(t, store, true); logged != "" {
		t.Errorf("bootstrap spoke up with no SHINYHUB_ADMIN_USER set: %s", logged)
	}
	if _, err := store.GetUserByUsername("admin"); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("an account appeared without a username being asked for (err = %v)", err)
	}
}
