package localrun

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func skipIfNoPython3(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not in PATH: skipping integration test")
	}
}

func writeHealthyFixture(t *testing.T) string {
	t.Helper()
	skipIfNoPython3(t)
	dir := t.TempDir()
	// A tiny Python stdlib HTTP server that answers / with 200; no deps.
	app := `import http.server, os
http.server.HTTPServer(("127.0.0.1", int(os.environ["PORT"])), http.server.SimpleHTTPRequestHandler).serve_forever()
`
	if err := os.WriteFile(filepath.Join(dir, "server.py"), []byte(app), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "shinyhub.toml"),
		[]byte("[app]\ncommand = [\"python3\", \"server.py\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestRun_Check_HealthyExitsZero(t *testing.T) {
	dir := writeHealthyFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := Run(ctx, Options{BundleDir: dir, Slug: "fixture", Check: true, NoReload: true}, os.Stdout, os.Stderr)
	if err != nil {
		t.Fatalf("--check on a healthy app should exit 0, got %v", err)
	}
}

func TestRun_Check_BrokenExitsNonZero(t *testing.T) {
	skipIfNoPython3(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "shinyhub.toml"),
		[]byte("[app]\ncommand = [\"python3\", \"-c\", \"import sys; sys.exit(3)\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := Run(ctx, Options{BundleDir: dir, Slug: "broken", Check: true, NoReload: true}, os.Stdout, os.Stderr); err == nil {
		t.Fatal("--check on a crashing app must return a non-nil error")
	}
}

func TestRun_DataSymlink_CreatedThenCleaned(t *testing.T) {
	dir := writeHealthyFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = Run(ctx, Options{BundleDir: dir, Slug: "fixture", Check: true, NoReload: true}, os.Stdout, os.Stderr)
	if _, err := os.Lstat(filepath.Join(dir, "data")); !os.IsNotExist(err) {
		t.Fatalf("<bundle>/data symlink must be removed after run, lstat err=%v", err)
	}
}
