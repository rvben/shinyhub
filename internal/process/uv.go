package process

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// CheckUV verifies that the uv binary is available in PATH.
func CheckUV() error {
	if _, err := exec.LookPath("uv"); err != nil {
		return fmt.Errorf("uv not found in PATH: %w", err)
	}
	return nil
}

// uvSyncCmd builds the `uv sync` command. uv runs the project's build
// backend, which is deployer-controlled code, so the env is scrubbed of
// server secrets via SanitizedEnv.
func uvSyncCmd(dir string) *exec.Cmd {
	cmd := exec.Command("uv", "sync")
	cmd.Dir = dir
	cmd.Env = SanitizedEnv()
	return cmd
}

// uvPythonInstallCmd builds the `uv python install <version>` command with a
// scrubbed env, for the same reason as uvSyncCmd.
func uvPythonInstallCmd(version string) *exec.Cmd {
	cmd := exec.Command("uv", "python", "install", version)
	cmd.Env = SanitizedEnv()
	return cmd
}

// Sync runs `uv sync` in dir if a pyproject.toml is present, creating/updating
// the .venv. For requirements.txt-only projects, dependency installation is
// handled lazily by `uv run --with-requirements` at process start.
func Sync(dir string) error {
	if _, err := os.Stat(filepath.Join(dir, "pyproject.toml")); os.IsNotExist(err) {
		return nil
	}
	out, err := uvSyncCmd(dir).CombinedOutput()
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
	out, err := uvPythonInstallCmd(version).CombinedOutput()
	if err != nil {
		return fmt.Errorf("uv python install %s: %w\n%s", version, err, out)
	}
	return nil
}
