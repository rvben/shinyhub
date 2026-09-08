package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/config"
)

const fileSecret = "0123456789abcdef0123456789abcdef"

// writeSecretFile writes a secret file with the given mode and returns its
// path. The mode is applied with an explicit Chmod because the process umask
// silently narrows the mode passed to WriteFile, which would make a
// permissions test pass without exercising the check.
func writeSecretFile(t *testing.T, body string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "auth.secret")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func loadWithSecretFile(t *testing.T, path string) (*config.Config, error) {
	t.Helper()
	t.Setenv("SHINYHUB_AUTH_SECRET", "")
	t.Setenv("SHINYHUB_AUTH_SECRET_FILE", path)
	return config.Load(filepath.Join(t.TempDir(), "absent.yaml"))
}

// The environment is the wrong place for the key that signs every session and
// encrypts every app secret: on the native runtime a deployed app runs as this
// same user, and a same-user process can read the server's environment.
func TestAuthSecretFileSuppliesTheSecret(t *testing.T) {
	// A trailing newline is what `openssl rand -hex 32 > secret` writes and
	// what every editor adds; keeping it would give a different secret than
	// the operator generated and invalidate every session.
	cfg, err := loadWithSecretFile(t, writeSecretFile(t, fileSecret+"\n", 0o600))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Auth.Secret != fileSecret {
		t.Errorf("secret = %q, want %q", cfg.Auth.Secret, fileSecret)
	}
	if cfg.Auth.SecretSource != "file" {
		t.Errorf("source = %q, want \"file\"", cfg.Auth.SecretSource)
	}
}

// A group- or world-readable secret file hands the reach back to exactly the
// processes this setting exists to keep it from, while looking like the secure
// option. Accepting it with a warning would be worse than not offering it.
func TestAuthSecretFileRejectsAReadableFile(t *testing.T) {
	for _, mode := range []os.FileMode{0o640, 0o604, 0o644, 0o666} {
		_, err := loadWithSecretFile(t, writeSecretFile(t, fileSecret, mode))
		if err == nil {
			t.Errorf("mode %04o was accepted", mode)
			continue
		}
		if !strings.Contains(err.Error(), "readable by group or other") {
			t.Errorf("mode %04o: %v, want a permissions refusal", mode, err)
		}
	}
	// The owner-only modes must still load, or the check is just a wall.
	for _, mode := range []os.FileMode{0o400, 0o600} {
		if _, err := loadWithSecretFile(t, writeSecretFile(t, fileSecret, mode)); err != nil {
			t.Errorf("mode %04o was refused: %v", mode, err)
		}
	}
}

// Preferring one silently would leave an operator who believes they rotated
// the secret running on the other, with the environment copy - the readable
// one - still in place.
func TestAuthSecretFileConflictingWithTheEnvironmentIsAnError(t *testing.T) {
	path := writeSecretFile(t, fileSecret, 0o600)
	t.Setenv("SHINYHUB_AUTH_SECRET", "fedcba9876543210fedcba9876543210")
	t.Setenv("SHINYHUB_AUTH_SECRET_FILE", path)
	_, err := config.Load(filepath.Join(t.TempDir(), "absent.yaml"))
	if err == nil {
		t.Fatal("a file and an environment variable holding different secrets loaded without complaint")
	}
	if !strings.Contains(err.Error(), "different secrets") {
		t.Errorf("error = %v, does not name the conflict", err)
	}
}

// Migrating in stages leaves both set to the same value for a while; that is
// not a conflict and must not stop the server.
func TestAuthSecretFileMatchingTheEnvironmentLoads(t *testing.T) {
	path := writeSecretFile(t, fileSecret+"\n", 0o600)
	t.Setenv("SHINYHUB_AUTH_SECRET", fileSecret)
	t.Setenv("SHINYHUB_AUTH_SECRET_FILE", path)
	cfg, err := config.Load(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Auth.Secret != fileSecret {
		t.Errorf("secret = %q, want %q", cfg.Auth.Secret, fileSecret)
	}
}

func TestAuthSecretFileReportsAMissingOrEmptyFile(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "")
	t.Setenv("SHINYHUB_AUTH_SECRET_FILE", filepath.Join(t.TempDir(), "absent.secret"))
	if _, err := config.Load(filepath.Join(t.TempDir(), "absent.yaml")); err == nil ||
		!strings.Contains(err.Error(), "auth.secret_file") {
		t.Errorf("missing file: %v, want an auth.secret_file error", err)
	}
	if _, err := loadWithSecretFile(t, writeSecretFile(t, "\n", 0o600)); err == nil ||
		!strings.Contains(err.Error(), "is empty") {
		t.Errorf("empty file: %v, want an empty-file error", err)
	}
}

// The warning at startup keys off this, so a secret that came from the
// environment has to be distinguishable from one that did not.
func TestAuthSecretSourceRecordsTheEnvironment(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", fileSecret)
	t.Setenv("SHINYHUB_AUTH_SECRET_FILE", "")
	cfg, err := config.Load(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Auth.SecretSource != "env" {
		t.Errorf("source = %q, want \"env\"", cfg.Auth.SecretSource)
	}
}
