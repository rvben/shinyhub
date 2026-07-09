package deploy

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// findEnv returns the value of name in env ("NAME=value" entries), or "".
func findEnv(env []string, name string) string {
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, name+"="); ok {
			return v
		}
	}
	return ""
}

// buildConfinement must make the per-app uv-python dir writable and point uv
// at it via UV_PYTHON_INSTALL_DIR: uv provisions managed interpreters into a
// DATA dir (UV_PYTHON_INSTALL_DIR / XDG_DATA_HOME), not the already-redirected
// caches, so without this a bundle whose requires-python exceeds every system
// interpreter fails its build with EACCES under the read-only root.
func TestBuildConfinement_PerAppPythonDir(t *testing.T) {
	appRoot := filepath.Join(t.TempDir(), "myapp")
	buildDir := filepath.Join(appRoot, "versions", "v1")
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		t.Fatal(err)
	}

	spec, env := buildConfinement(buildDir)

	pyDir := filepath.Join(appRoot, "uv-python")
	if got := findEnv(env, "UV_PYTHON_INSTALL_DIR"); got != pyDir {
		t.Errorf("UV_PYTHON_INSTALL_DIR = %q, want %q", got, pyDir)
	}
	if !slices.Contains(spec.WritePaths, pyDir) {
		t.Errorf("WritePaths %v must include the uv-python dir %q", spec.WritePaths, pyDir)
	}
	if !slices.Contains(spec.WritePaths, buildDir) {
		t.Errorf("WritePaths %v must include the build dir %q", spec.WritePaths, buildDir)
	}
	// Landlock write rules are IgnoreIfMissing: a rule for a dir that does not
	// exist when the ruleset is applied is silently dropped, and the parent app
	// dir is NOT writable, so uv could never create the dir itself. It must
	// exist before the shim starts.
	if info, err := os.Stat(pyDir); err != nil || !info.IsDir() {
		t.Errorf("uv-python dir must be pre-created (err=%v)", err)
	}
	// The cache redirects that predate the uv-python dir must stay intact.
	for _, name := range []string{"UV_CACHE_DIR", "XDG_CACHE_HOME", "RENV_PATHS_ROOT"} {
		if findEnv(env, name) == "" {
			t.Errorf("missing %s in build env %v", name, env)
		}
	}
}

// An operator-set UV_PYTHON_INSTALL_DIR (service environment) is honored: the
// chosen dir becomes the writable managed-Python store instead of the per-app
// default, trading cross-app interpreter isolation for a shared download.
func TestBuildConfinement_OperatorOverride(t *testing.T) {
	shared := filepath.Join(t.TempDir(), "shared-pythons")
	t.Setenv("UV_PYTHON_INSTALL_DIR", shared)

	buildDir := t.TempDir()
	spec, env := buildConfinement(buildDir)

	if got := findEnv(env, "UV_PYTHON_INSTALL_DIR"); got != shared {
		t.Errorf("UV_PYTHON_INSTALL_DIR = %q, want operator override %q", got, shared)
	}
	if !slices.Contains(spec.WritePaths, shared) {
		t.Errorf("WritePaths %v must include the operator dir %q", spec.WritePaths, shared)
	}
	if info, err := os.Stat(shared); err != nil || !info.IsDir() {
		t.Errorf("operator dir must be pre-created (err=%v)", err)
	}
}

// A permission-denied build failure under the sandbox gets an actionable hint
// naming the writable roots, instead of only uv's raw EACCES.
func TestSandboxDenialHint_WrapsPermissionDenied(t *testing.T) {
	base := errors.New("exit status 2")
	out := []byte("error: failed to create directory `/var/lib/shinyhub/.local/share/uv/python`: Permission denied (os error 13)")

	err := sandboxDenialHint(out, base, []string{"/srv/apps/x/versions/v1", "/srv/apps/x/uv-python"})

	if !errors.Is(err, base) {
		t.Fatalf("hint must wrap the original error, got %v", err)
	}
	msg := err.Error()
	for _, want := range []string{"build sandbox", "/srv/apps/x/uv-python"} {
		if !strings.Contains(msg, want) {
			t.Errorf("hint %q must mention %q", msg, want)
		}
	}
}

// Failures without a permission signature pass through untouched, and a nil
// error stays nil: the hint must never invent a sandbox problem.
func TestSandboxDenialHint_PassThrough(t *testing.T) {
	base := errors.New("exit status 1")
	if err := sandboxDenialHint([]byte("resolution failed: no matching version"), base, nil); err != base {
		t.Errorf("non-EACCES failure must pass through unchanged, got %v", err)
	}
	if err := sandboxDenialHint([]byte("Permission denied"), nil, nil); err != nil {
		t.Errorf("nil error must stay nil, got %v", err)
	}
}
