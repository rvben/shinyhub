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

// EnsureProject must not invoke uv (and must not write a pyproject) on its no-op
// paths: a pyproject.toml already present, or no requirements.txt to convert.
// These run without uv installed.
func TestEnsureProject_NoOpPaths(t *testing.T) {
	t.Run("no requirements.txt", func(t *testing.T) {
		dir := t.TempDir()
		if err := process.EnsureProject(dir); err != nil {
			t.Fatalf("EnsureProject: %v", err)
		}
		if _, err := os.Stat(filepath.Join(dir, "pyproject.toml")); !os.IsNotExist(err) {
			t.Error("must not synthesize a project when there is no requirements.txt")
		}
		if process.IsSynthesizedProject(dir) {
			t.Error("must not mark a project that was not synthesized")
		}
	})

	t.Run("existing pyproject is left alone", func(t *testing.T) {
		dir := t.TempDir()
		_ = os.WriteFile(filepath.Join(dir, "requirements.txt"), []byte("shiny\n"), 0o644)
		const authored = "[project]\nname = \"mine\"\n"
		if err := os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte(authored), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := process.EnsureProject(dir); err != nil {
			t.Fatalf("EnsureProject: %v", err)
		}
		got, _ := os.ReadFile(filepath.Join(dir, "pyproject.toml"))
		if string(got) != authored {
			t.Errorf("author pyproject must be untouched, got %q", got)
		}
		if process.IsSynthesizedProject(dir) {
			t.Error("an author-provided project must not be marked synthesized")
		}
	})
}

// IsSynthesizedProject reflects the marker EnsureProject would write.
func TestIsSynthesizedProject(t *testing.T) {
	dir := t.TempDir()
	if process.IsSynthesizedProject(dir) {
		t.Error("empty dir must not report a synthesized project")
	}
	_ = os.WriteFile(filepath.Join(dir, process.SynthesizedProjectMarker), []byte("1\n"), 0o644)
	if !process.IsSynthesizedProject(dir) {
		t.Error("a dir with the marker must report a synthesized project")
	}
}
