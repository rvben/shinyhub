//go:build linux

package deploy

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/sandbox"
)

// TestSandboxedPythonSync_ManagedPythonE2E_Live drives the exact production
// build path (sandboxedPythonSync, pythonSyncFn's default) against a real uv
// on a bundle whose requires-python no system interpreter satisfies, so uv
// must download a managed CPython during `uv sync`. Under the build sandbox
// that provisioning write goes to UV_PYTHON_INSTALL_DIR; before that redirect
// existed it went to $HOME/.local/share/uv/python and Landlock denied it,
// failing every native deploy on hosts without a bundle-compatible system
// Python (the v0.9.6 SEC-A1 regression).
//
// Gated behind SHINYHUB_LIVE_UV=1: it needs uv on PATH plus network access to
// download an interpreter (tens of MB), which has no place in the default
// hermetic test run. Exercise it on a live Linux kernel, e.g.:
//
//	docker run --rm --security-opt seccomp=unconfined -e SHINYHUB_LIVE_UV=1 \
//	  -v "$PWD":/src -w /src golang:1.26 \
//	  bash -c 'curl -LsSf https://astral.sh/uv/install.sh | sh && \
//	           PATH=$HOME/.local/bin:$PATH go test ./internal/deploy/ -run ManagedPythonE2E -v'
func TestSandboxedPythonSync_ManagedPythonE2E_Live(t *testing.T) {
	if os.Getenv("SHINYHUB_LIVE_UV") != "1" {
		t.Skip("set SHINYHUB_LIVE_UV=1 to run the live uv managed-Python e2e")
	}
	if !sandbox.Supported() {
		t.Skip("no isolation backend on this platform")
	}
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not on PATH")
	}

	// Force the corp-host scenario regardless of what Python the test image
	// ships: only-managed makes uv ignore system interpreters, exactly as if
	// none satisfied requires-python. Routed through SHINYHUB_APP_ENV_ALLOW
	// because the build env is scrubbed by SanitizedEnv.
	t.Setenv("UV_PYTHON_PREFERENCE", "only-managed")
	t.Setenv("SHINYHUB_APP_ENV_ALLOW", "UV_PYTHON_PREFERENCE")

	base := t.TempDir()
	buildDir := filepath.Join(base, "myapp", "versions", "v1")
	if err := os.MkdirAll(buildDir, 0o770); err != nil {
		t.Fatal(err)
	}
	// requires-python must exceed every interpreter the host image ships so uv
	// is forced to provision a managed one. No dependencies and no build
	// system, so the only network fetch is the interpreter itself.
	pyproject := `[project]
name = "sandbox-e2e"
version = "0.0.1"
requires-python = ">=3.12"

[tool.uv]
package = false
`
	if err := os.WriteFile(filepath.Join(buildDir, "pyproject.toml"), []byte(pyproject), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := sandboxedPythonSync(ctx, buildDir); err != nil {
		t.Fatalf("sandboxed uv sync with managed-Python provisioning failed: %v", err)
	}

	// The interpreter must have landed in the per-app store, not $HOME.
	pyDir := filepath.Join(base, "myapp", "uv-python")
	entries, err := os.ReadDir(pyDir)
	if err != nil {
		t.Fatalf("read per-app uv-python dir: %v", err)
	}
	var found bool
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "cpython-") {
			found = true
		}
	}
	if !found {
		t.Errorf("no managed cpython-* under %s (entries: %v)", pyDir, entries)
	}
	if _, err := os.Stat(filepath.Join(buildDir, ".venv")); err != nil {
		t.Errorf("uv sync must have created the bundle venv: %v", err)
	}
}
