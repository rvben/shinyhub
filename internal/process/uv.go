package process

import (
	"fmt"
	"os/exec"
)

// CheckUV verifies that the uv binary is available in PATH.
func CheckUV() error {
	if _, err := exec.LookPath("uv"); err != nil {
		return fmt.Errorf("uv not found in PATH: %w", err)
	}
	return nil
}

// Sync runs `uv sync` in dir, creating/updating the .venv.
func Sync(dir string) error {
	cmd := exec.Command("uv", "sync")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("uv sync: %w\n%s", err, out)
	}
	return nil
}

// EnsurePython runs `uv python install <version>` if version is non-empty.
func EnsurePython(version string) error {
	if version == "" {
		return nil
	}
	cmd := exec.Command("uv", "python", "install", version)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("uv python install %s: %w\n%s", version, err, out)
	}
	return nil
}
