package localrun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/deploy"
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
	err := Run(ctx, Options{BundleDir: dir, StateDir: t.TempDir(), Slug: "fixture", Check: true, NoReload: true}, os.Stdout, os.Stderr)
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
	if err := Run(ctx, Options{BundleDir: dir, StateDir: t.TempDir(), Slug: "broken", Check: true, NoReload: true}, os.Stdout, os.Stderr); err == nil {
		t.Fatal("--check on a crashing app must return a non-nil error")
	}
}

func TestRun_DataSymlinkNeverAppearsInSource(t *testing.T) {
	dir := writeHealthyFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = Run(ctx, Options{BundleDir: dir, StateDir: t.TempDir(), Slug: "fixture", Check: true, NoReload: true}, os.Stdout, os.Stderr)
	if _, err := os.Lstat(filepath.Join(dir, "data")); !os.IsNotExist(err) {
		t.Fatalf("<bundle>/data symlink must be removed after run, lstat err=%v", err)
	}
}

// TestRun_DataDir_Absolutized verifies that a relative --data-dir is resolved
// to an absolute path so the symlink target and SHINYHUB_APP_DATA agree.
// The workspace output and created directory must use the same absolute path.
func TestRun_DataDir_Absolutized(t *testing.T) {
	skipIfNoPython3(t)
	dir := writeHealthyFixture(t)

	// Use a relative data-dir (relative to current working directory, not bundle).
	relDataDir := "testdata-abs-check-" + fmt.Sprintf("%d", time.Now().UnixNano())
	defer os.RemoveAll(relDataDir)

	expectedAbs, err := filepath.Abs(relDataDir)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	state := t.TempDir()

	_ = Run(ctx, Options{
		BundleDir: dir, StateDir: state, Slug: "abs-check", DataDir: relDataDir,
		Check: true, NoReload: true,
	}, os.Stdout, os.Stderr)

	// The absolute data dir must have been created by MkdirAll.
	if _, err := os.Stat(expectedAbs); err != nil {
		t.Fatalf("absolute data dir %q was not created (relative path was not absolutized): %v", expectedAbs, err)
	}
	target, err := os.Readlink(filepath.Join(state, "bundles", "0", "data"))
	if err != nil {
		t.Fatal(err)
	}
	canonicalExpected, err := canonicalPath(expectedAbs)
	if err != nil {
		t.Fatal(err)
	}
	if target != canonicalExpected {
		t.Fatalf("workspace data link = %q, want absolute %q", target, canonicalExpected)
	}
}

// Platform-managed values are rejected explicitly rather than being silently
// ignored or allowed to diverge from a deployed app.
func TestValidateUserEnv_RejectsReservedKeys(t *testing.T) {
	for _, assignment := range []string{"PORT=1", "SHINYHUB_APP_DATA=/bogus", "SHINYHUB_APP_SLUG=wrong"} {
		if _, err := validateUserEnv([]string{assignment}); err == nil || !strings.Contains(err.Error(), "managed by ShinyHub") {
			t.Fatalf("%q: expected actionable reserved-key error, got %v", assignment, err)
		}
	}
	got, err := validateUserEnv([]string{"HOME=/home/user", "MY_VAR=ok"})
	if err != nil || len(got) != 2 {
		t.Fatalf("ordinary env rejected: got=%v err=%v", got, err)
	}
}

func TestRun_MissingSourceDoesNotCreateIt(t *testing.T) {
	parent := t.TempDir()
	missing := filepath.Join(parent, "typo")
	err := Run(context.Background(), Options{BundleDir: missing, Check: true}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("missing source error = %v", err)
	}
	if _, statErr := os.Stat(missing); !os.IsNotExist(statErr) {
		t.Fatalf("missing source was materialized: %v", statErr)
	}
}

func TestRun_RejectsGeneratedStateInsideSourceWithoutCreatingIt(t *testing.T) {
	source := writeHealthyFixture(t)
	for _, tc := range []struct {
		name string
		opts Options
		path string
	}{
		{name: "state", opts: Options{StateDir: filepath.Join(source, ".state")}, path: filepath.Join(source, ".state")},
		{name: "data", opts: Options{StateDir: t.TempDir(), DataDir: filepath.Join(source, "dev-data")}, path: filepath.Join(source, "dev-data")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.opts.BundleDir = source
			tc.opts.Check = true
			err := Run(context.Background(), tc.opts, io.Discard, io.Discard)
			var validationErr *ValidationError
			if !errors.As(err, &validationErr) {
				t.Fatalf("error = %v, want ValidationError", err)
			}
			if _, statErr := os.Stat(tc.path); !os.IsNotExist(statErr) {
				t.Fatalf("rejected path was still created: %v", statErr)
			}
		})
	}
}

func TestRun_CheckDoesNotMutateSource(t *testing.T) {
	dir := writeHealthyFixture(t)
	before, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err = Run(ctx, Options{
		BundleDir: dir,
		StateDir:  t.TempDir(),
		Check:     true,
		Fresh:     true,
		NoReload:  true,
	}, io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(entryNames(before)) != fmt.Sprint(entryNames(after)) {
		t.Fatalf("source tree changed: before=%v after=%v", entryNames(before), entryNames(after))
	}
}

func entryNames(entries []os.DirEntry) []string {
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func TestRun_ReloadKeepsLastHealthyAppAndRecovers(t *testing.T) {
	skipIfNoPython3(t)
	source := t.TempDir()
	server := `import http.server, os, sys
if open("version.txt").read().strip() == "bad":
    sys.exit(3)
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        body = open("version.txt").read().strip().encode()
        self.send_response(200)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)
    def log_message(self, fmt, *args): pass
http.server.HTTPServer(("127.0.0.1", int(os.environ["PORT"])), H).serve_forever()
`
	for name, body := range map[string]string{
		"server.py":     server,
		"version.txt":   "v1\n",
		"shinyhub.toml": "[app]\ncommand = [\"python3\", \"server.py\"]\n",
	} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	port := deploy.AllocatePort()
	state := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{
			BundleDir: source,
			StateDir:  state,
			Slug:      "reload-test",
			Port:      port,
		}, io.Discard, io.Discard)
	}()
	url := fmt.Sprintf("http://127.0.0.1:%d/app/reload-test/", port)
	waitForBody(t, url, "v1", 5*time.Second)

	if err := os.WriteFile(filepath.Join(source, "version.txt"), []byte("bad\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("runner exited after a bad save: %v", err)
	default:
	}
	waitForBody(t, url, "v1", 2*time.Second)

	if err := os.WriteFile(filepath.Join(source, "version.txt"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitForBody(t, url, "v2", 6*time.Second)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runner shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runner did not stop")
	}
}

func waitForBody(t *testing.T, url, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url) //nolint:noctx
		if err == nil {
			body, readErr := io.ReadAll(resp.Body)
			resp.Body.Close()
			if readErr == nil && resp.StatusCode == http.StatusOK && string(body) == want {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%s did not return %q within %s", url, want, timeout)
}

// TestRun_ChildGroupKilledOnSelfExit verifies that when the foreground app
// process exits on its own (crash or normal), Run tears down its entire
// process group. We spawn a manifest command that backgrounds a long-lived
// grandchild (sleep), then exits normally. After Run returns the grandchild
// must be gone.
func TestRun_ChildGroupKilledOnSelfExit(t *testing.T) {
	skipIfNoPython3(t)
	dir := t.TempDir()

	// The manifest command spawns a grandchild (sleep 60 in the background)
	// then exits quickly. The grandchild would linger forever without group
	// signal teardown.
	//
	// We identify the grandchild by writing its PID to a temp file, then
	// verify it is not running after Run returns.
	pidFile := filepath.Join(dir, "grandchild.pid")
	script := fmt.Sprintf(`import subprocess, os, time
p = subprocess.Popen(["sleep", "60"])
with open(%q, "w") as f:
    f.write(str(p.pid))
time.sleep(0.1)  # let the grandchild start
`, pidFile)
	if err := os.WriteFile(filepath.Join(dir, "crasher.py"), []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "shinyhub.toml"),
		[]byte("[app]\ncommand = [\"python3\", \"crasher.py\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Run will exit with an error (process exits before healthy) which is fine.
	_ = Run(ctx, Options{BundleDir: dir, StateDir: t.TempDir(), Slug: "orphan-test", NoReload: true, Check: false}, os.Stdout, os.Stderr)

	// Give the OS a moment to reap.
	time.Sleep(200 * time.Millisecond)

	pidBytes, err := os.ReadFile(pidFile)
	if err != nil {
		// PID file not created: grandchild may not have started at all.
		t.Skip("grandchild PID file not created; skipping orphan check")
	}
	var grandchildPID int
	if _, err := fmt.Sscan(string(pidBytes), &grandchildPID); err != nil || grandchildPID <= 0 {
		t.Skipf("could not parse grandchild PID from %q", string(pidBytes))
	}

	// Check whether the grandchild process still exists.
	proc, err := os.FindProcess(grandchildPID)
	if err != nil {
		return // already gone
	}
	// On Unix, FindProcess always succeeds; send signal 0 to test existence.
	if sigErr := proc.Signal(os.Signal(syscall.Signal(0))); sigErr == nil {
		t.Errorf("grandchild (pid %d) is still alive after Run returned; subprocess group was not killed", grandchildPID)
	}
	// If sigErr != nil, the process is gone - correct behaviour.
}
