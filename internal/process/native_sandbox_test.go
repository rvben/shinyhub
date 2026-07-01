package process

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/sandbox"
)

// A workdir-relative executable (e.g. "./start.sh" from shinyhub.toml) must be
// resolved against the app working dir, not the server's cwd, so isolation does
// not reject valid app commands - and to an absolute path so the shim need not
// re-look-it-up against the app's PATH. A bare name resolves via PATH; a missing
// or non-executable file errors.
func TestResolveExecutable_ResolvesAgainstWorkDir(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "start.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Relative-with-separator resolves to an absolute path under workDir.
	got, err := resolveExecutable("./start.sh", dir)
	if err != nil {
		t.Errorf("workdir-relative ./start.sh should resolve: %v", err)
	} else if got != script {
		t.Errorf("resolved to %q, want %q", got, script)
	}
	// Absolute path is returned as is.
	if got, err := resolveExecutable(script, dir); err != nil || got != script {
		t.Errorf("absolute path = %q, %v; want %q", got, err, script)
	}
	// Bare name resolves via PATH to an absolute path.
	if got, err := resolveExecutable("sh", dir); err != nil || !filepath.IsAbs(got) {
		t.Errorf("bare name should resolve via PATH to abs, got %q, %v", got, err)
	}
	// Missing relative file, and a non-executable file, both error.
	if _, err := resolveExecutable("./nope.sh", dir); err == nil {
		t.Error("missing executable must error")
	}
	_ = os.WriteFile(filepath.Join(dir, "data.txt"), []byte("x"), 0o644)
	if _, err := resolveExecutable("./data.txt", dir); err == nil {
		t.Error("non-executable file must error")
	}
}

// A sandboxed launch must redirect tool caches into the app's own writable tree,
// or cache-writing launchers like `uv run` fail to start under the read-only
// root (proven on a live Landlock kernel). This pins that contract.
func TestSandboxLaunchEnv_RedirectsCaches(t *testing.T) {
	env := sandboxLaunchEnv("/srv/app", "/srv/app/.sandbox-tmp", "ENCODED")
	got := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		got[k] = v
	}
	want := map[string]string{
		"TMPDIR":         "/srv/app/.sandbox-tmp",
		"UV_CACHE_DIR":   "/srv/app/.uv-cache",
		"XDG_CACHE_HOME": "/srv/app/.cache",
		sandbox.EnvVar:   "ENCODED",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}
