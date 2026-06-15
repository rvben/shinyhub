package process_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/rvben/shinyhub/internal/process"
)

func TestUVAvailable(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not in PATH — skipping integration test")
	}
	if err := process.CheckUV(); err != nil {
		t.Fatalf("uv check: %v", err)
	}
}

// EnsureRequirementsLock must not invoke uv (and must not write a lock) on any
// of its no-op paths: no requirements.txt, a pyproject.toml present, or a lock
// already there. These run without uv installed.
func TestEnsureRequirementsLock_NoOpPaths(t *testing.T) {
	lockPath := func(dir string) string { return filepath.Join(dir, process.RequirementsLockName) }

	t.Run("no requirements.txt", func(t *testing.T) {
		dir := t.TempDir()
		if err := process.EnsureRequirementsLock(dir); err != nil {
			t.Fatalf("EnsureRequirementsLock: %v", err)
		}
		if _, err := os.Stat(lockPath(dir)); !os.IsNotExist(err) {
			t.Error("must not write a lock when there is no requirements.txt")
		}
	})

	t.Run("pyproject present", func(t *testing.T) {
		dir := t.TempDir()
		_ = os.WriteFile(filepath.Join(dir, "requirements.txt"), []byte("shiny\n"), 0o644)
		_ = os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte("[project]\nname='x'\n"), 0o644)
		if err := process.EnsureRequirementsLock(dir); err != nil {
			t.Fatalf("EnsureRequirementsLock: %v", err)
		}
		if _, err := os.Stat(lockPath(dir)); !os.IsNotExist(err) {
			t.Error("project mode (pyproject.toml) must not get a requirements lock")
		}
	})

	t.Run("existing lock is reused", func(t *testing.T) {
		dir := t.TempDir()
		_ = os.WriteFile(filepath.Join(dir, "requirements.txt"), []byte("shiny\n"), 0o644)
		const frozen = "shiny==1.4.0\n"
		if err := os.WriteFile(lockPath(dir), []byte(frozen), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := process.EnsureRequirementsLock(dir); err != nil {
			t.Fatalf("EnsureRequirementsLock: %v", err)
		}
		got, _ := os.ReadFile(lockPath(dir))
		if string(got) != frozen {
			t.Errorf("existing lock must be left untouched, got %q", got)
		}
	})
}
