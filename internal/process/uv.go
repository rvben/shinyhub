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

// RequirementsLockName is the compiled, fully-pinned lock written next to a
// requirements.txt app. Cold starts launch from this frozen resolution instead
// of re-resolving loose pins (which drifts as upstream publishes new versions).
const RequirementsLockName = "requirements.lock"

// uvPipCompileCmd builds `uv pip compile requirements.txt -o requirements.lock`
// with a scrubbed env (the resolver runs index/build code).
func uvPipCompileCmd(dir string) *exec.Cmd {
	cmd := exec.Command("uv", "pip", "compile", "requirements.txt", "-o", RequirementsLockName)
	cmd.Dir = dir
	cmd.Env = SanitizedEnv()
	return cmd
}

// EnsureRequirementsLock freezes a requirements.txt-only app into a lock the
// first time it is prepared, so every later cold start installs the same
// resolution rather than re-resolving loose pins. It is a no-op when:
//   - a pyproject.toml is present (uv.lock already provides reproducibility),
//   - there is no requirements.txt to lock, or
//   - a lock already exists (reuse it; a redeploy lands in a fresh version dir
//     with no lock, so it re-locks from the new requirements).
//
// Compiling in dir targets the same interpreter `uv run` selects from the same
// dir, keeping the lock consistent with the launch.
func EnsureRequirementsLock(dir string) error {
	if _, err := os.Stat(filepath.Join(dir, "pyproject.toml")); err == nil {
		return nil
	}
	if _, err := os.Stat(filepath.Join(dir, "requirements.txt")); err != nil {
		return nil
	}
	if _, err := os.Stat(filepath.Join(dir, RequirementsLockName)); err == nil {
		return nil
	}
	if out, err := uvPipCompileCmd(dir).CombinedOutput(); err != nil {
		return fmt.Errorf("uv pip compile: %w\n%s", err, out)
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
