package process

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// SynthesizedProjectMarker is a sentinel EnsureProject drops next to a
// pyproject.toml it generated from a requirements.txt. It distinguishes a
// synthesized project (valid only where this host prepared the deps and synced
// the .venv) from one the author shipped (valid everywhere).
const SynthesizedProjectMarker = ".shinyhub-synthesized-project"

func uvInitCmd(dir string) *exec.Cmd {
	// --bare yields a non-package project (no [build-system]), so `uv sync`
	// installs only the dependencies, never the app directory itself. --name is
	// explicit because the version dir is an all-digits timestamp, which uv
	// would otherwise use as the project name.
	cmd := exec.Command("uv", "init", "--bare", "--name", "shinyhub-app")
	cmd.Dir = dir
	cmd.Env = SanitizedEnv()
	return cmd
}

func uvAddRequirementsCmd(dir string) *exec.Cmd {
	// uv parses the requirements file (including its grammar) and writes the
	// resolved deps into pyproject.toml plus a native uv.lock.
	cmd := exec.Command("uv", "add", "--requirements", "requirements.txt")
	cmd.Dir = dir
	cmd.Env = SanitizedEnv()
	return cmd
}

// uvAddCmd builds a `uv add <pkgs...>` command with a scrubbed env.
func uvAddCmd(dir string, pkgs ...string) *exec.Cmd {
	cmd := exec.Command("uv", append([]string{"add"}, pkgs...)...)
	cmd.Dir = dir
	cmd.Env = SanitizedEnv()
	return cmd
}

// requirementDistName extracts the lowercased distribution name from one
// requirements.txt line, stripping version specifiers, extras, environment
// markers, comments, and options. Returns "" for blank/comment/option lines.
func requirementDistName(line string) string {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "-") {
		return ""
	}
	name := line
	for _, sep := range []string{" ", "\t", ";", "@", "[", "(", "=", ">", "<", "~", "!", ","} {
		if i := strings.Index(name, sep); i >= 0 {
			name = name[:i]
		}
	}
	return strings.ToLower(strings.TrimSpace(name))
}

// requirementsImplyPydantic reports whether the requirements declare a direct
// dependency on `shiny`. shiny's UI imports `shinychat`, which imports `pydantic`
// unconditionally at module load while declaring it OPTIONAL (shinychat 0.5.0:
// `Requires-Dist: pydantic; extra == 'providers'`). A correct resolver therefore
// omits pydantic, and every shiny app then crashes on `import shiny.ui` with
// ModuleNotFoundError. EnsureProject adds pydantic for shiny apps so they run.
// Remove this once shinychat stops importing an optional dependency.
func requirementsImplyPydantic(requirements string) bool {
	for _, line := range strings.Split(requirements, "\n") {
		if requirementDistName(line) == "shiny" {
			return true
		}
	}
	return false
}

// EnsureProject converts a requirements.txt-only Python app into a uv project so
// it gains a native uv.lock (fully pinned, hashed, requires-python-aware) and
// launches in project mode. Reproducibility then comes from one mechanism -
// uv.lock - for both author-provided and requirements-based apps.
//
// It is a no-op when a pyproject.toml is already present (the author's, or a
// prior conversion) or when there is no requirements.txt to convert. On a failed
// `uv add` it removes the half-built project so the app falls back cleanly to
// requirements mode rather than launching against an incomplete environment.
func EnsureProject(dir string) error {
	if _, err := os.Stat(filepath.Join(dir, "pyproject.toml")); err == nil {
		return nil
	}
	if _, err := os.Stat(filepath.Join(dir, "requirements.txt")); err != nil {
		return nil
	}
	if out, err := uvInitCmd(dir).CombinedOutput(); err != nil {
		return fmt.Errorf("uv init: %w\n%s", err, out)
	}
	if out, err := uvAddRequirementsCmd(dir).CombinedOutput(); err != nil {
		_ = os.Remove(filepath.Join(dir, "pyproject.toml"))
		_ = os.Remove(filepath.Join(dir, "uv.lock"))
		_ = os.RemoveAll(filepath.Join(dir, ".venv"))
		return fmt.Errorf("uv add requirements: %w\n%s", err, out)
	}
	// shiny's UI imports shinychat, which imports pydantic unconditionally while
	// declaring it optional (shinychat 0.5.0). Add pydantic for shiny apps so they
	// do not crash on `import shiny.ui`. See requirementsImplyPydantic.
	if reqs, rerr := os.ReadFile(filepath.Join(dir, "requirements.txt")); rerr == nil && requirementsImplyPydantic(string(reqs)) {
		if out, err := uvAddCmd(dir, "pydantic").CombinedOutput(); err != nil {
			_ = os.Remove(filepath.Join(dir, "pyproject.toml"))
			_ = os.Remove(filepath.Join(dir, "uv.lock"))
			_ = os.RemoveAll(filepath.Join(dir, ".venv"))
			return fmt.Errorf("uv add pydantic (shiny chat dependency): %w\n%s", err, out)
		}
	}
	_ = os.WriteFile(filepath.Join(dir, SynthesizedProjectMarker), []byte("1\n"), 0o644)
	return nil
}

// IsSynthesizedProject reports whether the pyproject.toml in dir was generated
// by EnsureProject (rather than shipped by the app author).
func IsSynthesizedProject(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, SynthesizedProjectMarker))
	return err == nil
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
