//go:build linux

package deploy

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/sandbox"
)

// probeWheel builds a minimal pure-Python wheel for the package
// "provision-probe" 0.0.1 entirely in Go (a wheel is a zip with dist-info
// metadata; no Python toolchain is needed to produce one). The package name
// is unique to this repo, so it can never resolve from a public index - if
// it installs, it came from the test's own index.
func probeWheel(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	files := []struct{ name, body string }{
		{"provision_probe/__init__.py", "MARKER = \"shinyhub-provision-probe\"\n"},
		{"provision_probe-0.0.1.dist-info/METADATA", "Metadata-Version: 2.1\nName: provision-probe\nVersion: 0.0.1\n"},
		{"provision_probe-0.0.1.dist-info/WHEEL", "Wheel-Version: 1.0\nGenerator: shinyhub-e2e\nRoot-Is-Purelib: true\nTag: py3-none-any\n"},
	}
	record := ""
	for _, f := range files {
		w, err := zw.Create(f.name)
		if err != nil {
			t.Fatalf("wheel zip create %s: %v", f.name, err)
		}
		if _, err := w.Write([]byte(f.body)); err != nil {
			t.Fatalf("wheel zip write %s: %v", f.name, err)
		}
		record += f.name + ",,\n"
	}
	record += "provision_probe-0.0.1.dist-info/RECORD,,\n"
	w, err := zw.Create("provision_probe-0.0.1.dist-info/RECORD")
	if err != nil {
		t.Fatalf("wheel zip create RECORD: %v", err)
	}
	if _, err := w.Write([]byte(record)); err != nil {
		t.Fatalf("wheel zip write RECORD: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("wheel zip close: %v", err)
	}
	return buf.Bytes()
}

// startProbeIndex serves a PEP 503 "simple" index whose only project is
// provision-probe, backed by the in-memory wheel. It returns two index URLs:
// probe (…/simple, where the package lives) and empty (…/empty, a valid
// index location that has no packages - every project page 404s, which uv
// reads as "not in this index").
func startProbeIndex(t *testing.T) (probe, empty string) {
	t.Helper()
	wheel := probeWheel(t)
	const wheelName = "provision_probe-0.0.1-py3-none-any.whl"
	mux := http.NewServeMux()
	mux.HandleFunc("/simple/provision-probe/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<!DOCTYPE html><html><body><a href="/wheels/%s">%s</a></body></html>`, wheelName, wheelName)
	})
	mux.HandleFunc("/wheels/"+wheelName, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(wheel)
	})
	srv := httptest.NewServer(mux) // unregistered paths (…/empty/*) 404
	t.Cleanup(srv.Close)
	return srv.URL + "/simple", srv.URL + "/empty"
}

// TestSandboxedPythonSync_PrivateIndexE2E_Live drives the exact production
// build path (sandboxedPythonSync) against a real uv on a bundle whose only
// dependency exists solely behind UV_EXTRA_INDEX_URL - the exact variable
// and topology of the v0.10.x regression (#41: the build env allowlist
// dropped UV_EXTRA_INDEX_URL, so private-registry deploys failed). Both
// index vars are set in the SERVICE environment, the way operators configure
// them, and must survive process.SanitizedEnv's allowlist into the sandboxed
// build: UV_INDEX_URL points at a hermetic empty index (so the happy path
// never leaves localhost) and UV_EXTRA_INDEX_URL at the index that has the
// probe package. If the allowlist stops passing UV_EXTRA_INDEX_URL, uv
// cannot resolve the probe package and this test fails.
//
// requires-python forces a uv-managed interpreter as well (the #40 class:
// the managed-Python download must land in the per-app UV_PYTHON_INSTALL_DIR
// the sandbox makes writable), so this one test covers the full corp shape:
// no suitable system Python + a private index.
//
// Gated behind SHINYHUB_LIVE_UV=1 (needs uv on PATH plus network for the
// interpreter download); run via `make test-provisioning`.
func TestSandboxedPythonSync_PrivateIndexE2E_Live(t *testing.T) {
	if os.Getenv("SHINYHUB_LIVE_UV") != "1" {
		t.Skip("set SHINYHUB_LIVE_UV=1 to run the live uv private-index e2e")
	}
	if !sandbox.Supported() {
		t.Skip("no isolation backend on this platform")
	}
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not on PATH")
	}

	probeURL, emptyURL := startProbeIndex(t)
	// The mechanism under test: index env vars set in the service environment
	// must survive process.SanitizedEnv's allowlist into the sandboxed build.
	// Deliberately NOT routed through the SHINYHUB_APP_ENV_ALLOW escape hatch.
	t.Setenv("UV_INDEX_URL", emptyURL)
	t.Setenv("UV_EXTRA_INDEX_URL", probeURL)
	// Force the corp-host scenario regardless of what Python the test image
	// ships (as in the ManagedPythonE2E test): the escape hatch is fine here,
	// UV_PYTHON_PREFERENCE is not the mechanism under test.
	t.Setenv("UV_PYTHON_PREFERENCE", "only-managed")
	t.Setenv("SHINYHUB_APP_ENV_ALLOW", "UV_PYTHON_PREFERENCE")

	base := t.TempDir()
	buildDir := filepath.Join(base, "corpapp", "versions", "v1")
	if err := os.MkdirAll(buildDir, 0o770); err != nil {
		t.Fatal(err)
	}
	pyproject := `[project]
name = "corp-e2e"
version = "0.0.1"
requires-python = ">=3.12"
dependencies = ["provision-probe==0.0.1"]

[tool.uv]
package = false
`
	if err := os.WriteFile(filepath.Join(buildDir, "pyproject.toml"), []byte(pyproject), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := sandboxedPythonSync(ctx, buildDir, nil); err != nil {
		t.Fatalf("sandboxed uv sync against the private index failed: %v", err)
	}

	// The probe package must be installed in the bundle venv - it only exists
	// on the test's index, so its presence proves the index env var reached
	// the sandboxed build.
	matches, err := filepath.Glob(filepath.Join(buildDir, ".venv", "lib", "python*", "site-packages", "provision_probe", "__init__.py"))
	if err != nil || len(matches) == 0 {
		t.Errorf("provision_probe not installed in the bundle venv (glob err=%v, matches=%v)", err, matches)
	}

	// And the interpreter must be a managed one in the per-app store (#40).
	pyDir := filepath.Join(base, "corpapp", "uv-python")
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
}
