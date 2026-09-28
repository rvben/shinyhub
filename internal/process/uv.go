package process

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/rvben/shinyhub/internal/fsx"
	"github.com/rvben/shinyhub/internal/spanerr"
)

// CheckUV verifies that the uv binary is available in PATH.
func CheckUV() error {
	if _, err := exec.LookPath("uv"); err != nil {
		return fmt.Errorf("uv not found in PATH: %w", err)
	}
	return nil
}

// uvBuildCmd builds a uv command for a dependency step in dir. uv runs the
// project's build backend, which is deployer-controlled code, so the env base
// is scrubbed of server secrets via SanitizedEnv; env (the app's own variables
// and its requirements index configuration, see ReadRequirementsBuild) is
// layered on top, and the host build interpreter policy stays authoritative.
func uvBuildCmd(ctx context.Context, dir string, env []string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "uv", args...)
	cmd.Dir = dir
	cmd.Env = WithBuildInterpreterPolicy(append(SanitizedEnv(), env...))
	return cmd
}

// uvSyncCmd builds the `uv sync` command.
func uvSyncCmd(ctx context.Context, dir string, env, flags []string) *exec.Cmd {
	return uvBuildCmd(ctx, dir, env, append([]string{"sync"}, flags...)...)
}

// RequirementsBuild is what a uv dependency step in a bundle needs beyond the
// SanitizedEnv base, derived from the bundle's requirements.txt (see
// RequirementsIndex).
type RequirementsBuild struct {
	// Env is the app's env followed by the package-index configuration the
	// requirements declare, resolved against the server and app environment.
	Env []string
	// Flags are the uv options that configuration needs on the command line.
	Flags []string
	index RequirementsIndex
}

// Output prepares a step's captured output for embedding in an error, as
// uvBuildOutput does, and masks credentials in it: uv hides URL userinfo in
// its errors but quotes an index URL's query string and path verbatim. Every
// URL's query values are masked, which covers a token written literally into
// the requirements, and so is every value a ${NAME} reference expanded,
// wherever it sits.
func (b RequirementsBuild) Output(out []byte) []byte {
	return []byte(b.index.Redact(spanerr.RedactURLs(string(uvBuildOutput(out)))))
}

// ReadRequirementsBuild resolves the RequirementsBuild for dir. It fails when
// the requirements cannot be read safely (an include outside the bundle), so
// a build never resolves without an index its bundle names.
func ReadRequirementsBuild(dir string, appEnv []string) (RequirementsBuild, error) {
	base := append(SanitizedEnv(), appEnv...)
	idx, err := ReadRequirementsIndex(dir, base)
	if err != nil {
		return RequirementsBuild{}, fmt.Errorf("read package-index options from requirements.txt: %w", err)
	}
	return RequirementsBuild{
		Env:   append(append([]string{}, appEnv...), idx.Env(base)...),
		Flags: idx.Args(),
		index: idx,
	}, nil
}

// uvBuildOutput prepares captured build output for embedding in an error that
// ends up in a JSON response. uv writes plain text into a pipe, but the build
// backends it runs are the app author's own code and colour their output
// whether or not anything is reading it from a terminal. The R build path
// (SyncR) strips for the same reason.
func uvBuildOutput(out []byte) []byte { return StripANSI(out) }

// uvPythonInstallCmd builds the `uv python install <version>` command with a
// scrubbed env, for the same reason as uvSyncCmd.
func uvPythonInstallCmd(version string) *exec.Cmd {
	cmd := exec.Command("uv", "python", "install", version)
	cmd.Env = WithBuildInterpreterPolicy(SanitizedEnv())
	return cmd
}

// Sync runs `uv sync` in dir if a pyproject.toml is present, creating/updating
// the .venv. For requirements.txt-only projects, dependency installation is
// handled lazily by `uv run --with-requirements` at process start.
func Sync(ctx context.Context, dir string) error {
	if _, err := os.Stat(filepath.Join(dir, "pyproject.toml")); os.IsNotExist(err) {
		return nil
	}
	build, err := ReadRequirementsBuild(dir, nil)
	if err != nil {
		return err
	}
	out, err := uvSyncCmd(ctx, dir, build.Env, build.Flags).CombinedOutput()
	if err != nil {
		switch ctx.Err() {
		case context.DeadlineExceeded:
			return fmt.Errorf("build exceeded the build timeout: %w", ctx.Err())
		case context.Canceled:
			return fmt.Errorf("build canceled: %w", ctx.Err())
		}
		return fmt.Errorf("%w\n%s", err, build.Output(out))
	}
	return nil
}

// SynthesizedProjectMarker is a sentinel EnsureProject drops next to a
// pyproject.toml it generated from a requirements.txt. It distinguishes a
// synthesized project (valid only where this host prepared the deps and synced
// the .venv) from one the author shipped (valid everywhere).
const SynthesizedProjectMarker = ".shinyhub-synthesized-project"

func uvInitCmd(ctx context.Context, dir string, env []string) *exec.Cmd {
	// --bare yields a non-package project (no [build-system]), so `uv sync`
	// installs only the dependencies, never the app directory itself. --name is
	// explicit because the version dir is an all-digits timestamp, which uv
	// would otherwise use as the project name.
	return uvBuildCmd(ctx, dir, env, "init", "--bare", "--name", "shinyhub-app")
}

func uvAddRequirementsCmd(ctx context.Context, dir string, env, flags []string) *exec.Cmd {
	// uv parses the requirements file (including its grammar) and writes the
	// resolved deps into pyproject.toml plus a native uv.lock. It does not
	// apply the file's index options, which arrive through env and flags.
	return uvBuildCmd(ctx, dir, env, append([]string{"add", "--requirements", "requirements.txt"}, flags...)...)
}

// uvAddCmd builds a `uv add <pkgs...>` command.
func uvAddCmd(ctx context.Context, dir string, env, flags []string, pkgs ...string) *exec.Cmd {
	return uvBuildCmd(ctx, dir, env, append(append([]string{"add"}, flags...), pkgs...)...)
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
//
// appEnv is the app's own environment, which the conversion sees as the app
// process does (private index credentials are commonly stored there). The
// package-index options in requirements.txt are applied explicitly, since
// `uv add --requirements` ignores them; see ReadRequirementsBuild.
func EnsureProject(ctx context.Context, dir string, appEnv []string) error {
	if _, err := os.Stat(filepath.Join(dir, "pyproject.toml")); err == nil {
		return nil
	}
	if _, err := os.Stat(filepath.Join(dir, "requirements.txt")); err != nil {
		return nil
	}
	build, err := ReadRequirementsBuild(dir, appEnv)
	if err != nil {
		return err
	}
	if out, err := uvInitCmd(ctx, dir, build.Env).CombinedOutput(); err != nil {
		return fmt.Errorf("uv init: %w\n%s", err, build.Output(out))
	}
	if out, err := uvAddRequirementsCmd(ctx, dir, build.Env, build.Flags).CombinedOutput(); err != nil {
		_ = os.Remove(filepath.Join(dir, "pyproject.toml"))
		_ = os.Remove(filepath.Join(dir, "uv.lock"))
		_ = fsx.RemoveAll(filepath.Join(dir, ".venv"))
		return fmt.Errorf("uv add requirements: %w\n%s", err, build.Output(out))
	}
	// shiny's UI imports shinychat, which imports pydantic unconditionally while
	// declaring it optional (shinychat 0.5.0). Add pydantic for shiny apps so they
	// do not crash on `import shiny.ui`. See requirementsImplyPydantic.
	if reqs, rerr := os.ReadFile(filepath.Join(dir, "requirements.txt")); rerr == nil && requirementsImplyPydantic(string(reqs)) {
		if out, err := uvAddCmd(ctx, dir, build.Env, build.Flags, "pydantic").CombinedOutput(); err != nil {
			_ = os.Remove(filepath.Join(dir, "pyproject.toml"))
			_ = os.Remove(filepath.Join(dir, "uv.lock"))
			_ = fsx.RemoveAll(filepath.Join(dir, ".venv"))
			return fmt.Errorf("uv add pydantic (shiny chat dependency): %w\n%s", err, build.Output(out))
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
		return fmt.Errorf("uv python install %s: %w\n%s", version, err, uvBuildOutput(out))
	}
	return nil
}
