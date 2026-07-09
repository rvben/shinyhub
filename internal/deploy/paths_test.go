package deploy

import (
	"path/filepath"
	"testing"
)

func TestBundleDir(t *testing.T) {
	got := BundleDir("/abs/apps", "myapp", "v3")
	want := filepath.Join("/abs/apps", "myapp", "versions", "v3")
	if got != want {
		t.Fatalf("BundleDir = %q, want %q", got, want)
	}
}

// A bundle dir in the canonical server layout (<appsDir>/<slug>/versions/<v>)
// gets a per-app uv-python dir as a sibling of versions/, so managed
// interpreters are provisioned once per app, survive version pruning, and stay
// out of other apps' writable sets.
func TestPythonInstallDir_ServerLayout(t *testing.T) {
	bundle := BundleDir("/abs/apps", "myapp", "v3")
	got := pythonInstallDir(bundle)
	want := filepath.Join("/abs/apps", "myapp", "uv-python")
	if got != want {
		t.Fatalf("pythonInstallDir(%q) = %q, want %q", bundle, got, want)
	}
}

// A bundle dir outside the server layout (`shinyhub run` on an arbitrary
// project dir, tests) falls back to a self-contained .uv-python inside the
// bundle, next to .venv / .uv-cache.
func TestPythonInstallDir_FallbackBundleLocal(t *testing.T) {
	got := pythonInstallDir("/home/dev/myproject")
	want := filepath.Join("/home/dev/myproject", ".uv-python")
	if got != want {
		t.Fatalf("pythonInstallDir = %q, want %q", got, want)
	}
}
