//go:build linux

package deploy

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/sandbox"
)

const frozenE2EPyproject = `[project]
name = "frozen-e2e"
version = "0.0.1"
requires-python = ">=3.9"
dependencies = ["provision-probe==0.0.1"]

[tool.uv]
package = false
`

// lockAgainst writes pyproject into a fresh bundle dir and locks it with a real
// `uv lock` against index, outside the sandbox, the way a developer produces
// the uv.lock they ship.
func lockAgainst(t *testing.T, index, pyproject string) (dir string, lock []byte) {
	t.Helper()
	dir = filepath.Join(t.TempDir(), "frozenapp", "versions", "v1")
	if err := os.MkdirAll(dir, 0o770); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte(pyproject), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("uv", "lock", "--default-index", index)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("uv lock: %v\n%s", err, out)
	}
	lock, err := os.ReadFile(filepath.Join(dir, "uv.lock"))
	if err != nil {
		t.Fatal(err)
	}
	return dir, lock
}

// TestSandboxedPythonSync_FrozenLockE2E_Live drives the production build path
// (sandboxedPythonSync) with a real uv on a bundle that ships a uv.lock, while
// the server is configured with a different index than the one the lock was
// made against. A plain `uv sync` re-resolves in that situation: with an index
// that differs only by a trailing slash it rewrites uv.lock and exits 0, and
// with an index that lacks the package it fails the build. The shipped lock
// must instead be installed as-is in both cases.
//
// Gated behind SHINYHUB_LIVE_UV=1; run via `make test-provisioning`.
func TestSandboxedPythonSync_FrozenLockE2E_Live(t *testing.T) {
	if os.Getenv("SHINYHUB_LIVE_UV") != "1" {
		t.Skip("set SHINYHUB_LIVE_UV=1 to run the live uv frozen-lock e2e")
	}
	if !sandbox.Supported() {
		t.Skip("no isolation backend on this platform")
	}
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not on PATH")
	}
	probeURL, emptyURL := startProbeIndex(t)

	cases := []struct {
		name, serverIndex string
	}{
		{"server index differs by a trailing slash", probeURL + "/"},
		{"server index lacks the locked package", emptyURL},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, lock := lockAgainst(t, probeURL, frozenE2EPyproject)
			// Set in the service environment, the way operators configure a
			// mirror; UV_DEFAULT_INDEX survives SanitizedEnv into the build.
			t.Setenv("UV_DEFAULT_INDEX", tc.serverIndex)

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			if err := sandboxedPythonSync(ctx, dir, nil); err != nil {
				t.Fatalf("sandboxedPythonSync: %v", err)
			}
			after, err := os.ReadFile(filepath.Join(dir, "uv.lock"))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(after, lock) {
				t.Errorf("uv.lock was rewritten by the build\nbefore:\n%s\nafter:\n%s", lock, after)
			}
			matches, _ := filepath.Glob(filepath.Join(dir, ".venv", "lib", "python*", "site-packages", "provision_probe", "__init__.py"))
			if len(matches) == 0 {
				t.Error("provision_probe not installed in the bundle venv")
			}
		})
	}
}

// TestCheckLockCurrent_AgreesWithUVE2E_Live holds CheckLockCurrent to uv's own
// verdict (`uv lock --check`) on locks real uv generated: each project is locked,
// then edited the way a developer forgets to re-lock after. A change in the
// shape uv writes shows up here instead of as a rejected current lock or an
// accepted stale one.
func TestCheckLockCurrent_AgreesWithUVE2E_Live(t *testing.T) {
	if os.Getenv("SHINYHUB_LIVE_UV") != "1" {
		t.Skip("set SHINYHUB_LIVE_UV=1 to run the live uv lock-check e2e")
	}
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not on PATH")
	}
	probeURL, _ := startProbeIndex(t)

	const probe = `"provision-probe==0.0.1"`
	project := func(deps, extras, groups string) string {
		return "[project]\nname = \"agree-e2e\"\nversion = \"0.0.1\"\nrequires-python = \">=3.9\"\n" +
			"dependencies = [" + deps + "]\n\n[project.optional-dependencies]\n" + extras +
			"\n[dependency-groups]\n" + groups + "\n[tool.uv]\npackage = false\n"
	}
	cases := []struct{ name, locked, edited string }{
		{"dependency dropped",
			project(probe, "", ""),
			project("", "", "")},
		{"extra requested on a dependency",
			project(probe, "", ""),
			project(`"provision-probe[fast]==0.0.1"`, "", "")},
		{"dependency moved out of an extra",
			project("", "fast = ["+probe+"]\n", ""),
			project(probe, "fast = []\n", "")},
		{"dependency added to a group",
			project("", "", "dev = []\n"),
			project("", "", "dev = ["+probe+"]\n")},
		{"dependency added through an included group",
			project("", "", "base = []\nlint = [{include-group = \"base\"}]\n"),
			project("", "", "base = ["+probe+"]\nlint = [{include-group = \"base\"}]\n")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, _ := lockAgainst(t, probeURL, tc.locked)
			uvCheck := func() error {
				cmd := exec.Command("uv", "lock", "--check", "--default-index", probeURL)
				cmd.Dir = dir
				if out, err := cmd.CombinedOutput(); err != nil {
					return errors.New(string(out))
				}
				return nil
			}

			if err := uvCheck(); err != nil {
				t.Fatalf("uv reports the fresh lock stale: %s", err)
			}
			if err := process.CheckLockCurrent(dir); err != nil {
				t.Errorf("CheckLockCurrent rejects a lock uv accepts: %v", err)
			}

			if err := os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte(tc.edited), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := uvCheck(); err == nil {
				t.Fatal("uv accepts the lock after the edit; the fixture is not stale")
			}
			if err := process.CheckLockCurrent(dir); !errors.Is(err, process.ErrStaleLock) {
				t.Errorf("CheckLockCurrent = %v, want ErrStaleLock for a lock uv reports stale", err)
			}
		})
	}
}
