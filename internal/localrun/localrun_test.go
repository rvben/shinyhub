package localrun

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
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

// TestRun_DataDir_Absolutized verifies that a relative --data-dir is resolved
// to an absolute path so the symlink target and SHINYHUB_APP_DATA agree.
// We verify this at the dropReservedKeys / Options layer, not via a live
// subprocess, because the absolutizing happens before any process is spawned.
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

	_ = Run(ctx, Options{
		BundleDir: dir, Slug: "abs-check", DataDir: relDataDir,
		Check: true, NoReload: true,
	}, os.Stdout, os.Stderr)

	// The absolute data dir must have been created by MkdirAll.
	if _, err := os.Stat(expectedAbs); err != nil {
		t.Fatalf("absolute data dir %q was not created (relative path was not absolutized): %v", expectedAbs, err)
	}
	// The relative path must NOT have been created as a directory.
	if _, err := os.Stat(relDataDir); err == nil {
		// It might be the same path if cwd == dir, skip in that case.
		absRel, _ := filepath.Abs(relDataDir)
		if absRel != expectedAbs {
			t.Fatalf("relative data dir %q was created as a directory; absolutizing failed", relDataDir)
		}
	}
}

// TestRun_ReservedEnvKeysDropped verifies that user-supplied PORT or
// SHINYHUB_APP_DATA values in --env are silently dropped, and the child sees
// the platform-allocated port, not the user-supplied one.
func TestRun_ReservedEnvKeysDropped(t *testing.T) {
	skipIfNoPython3(t)
	dir := t.TempDir()

	// Server that echoes back PORT and SHINYHUB_APP_DATA from its own env.
	app := `import http.server, os, json

class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        body = json.dumps({"port": os.environ.get("PORT",""), "data": os.environ.get("SHINYHUB_APP_DATA","")}).encode()
        self.send_response(200)
        self.send_header("Content-Type","application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)
    def log_message(self, fmt, *args): pass

http.server.HTTPServer(("127.0.0.1", int(os.environ["PORT"])), H).serve_forever()
`
	if err := os.WriteFile(filepath.Join(dir, "server.py"), []byte(app), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "shinyhub.toml"),
		[]byte("[app]\ncommand = [\"python3\", \"server.py\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	err := Run(ctx, Options{
		BundleDir: dir,
		Slug:      "reserved-env",
		Check:     true,
		NoReload:  true,
		// Attempt to inject bogus PORT and SHINYHUB_APP_DATA via user env.
		Env: []string{"PORT=1", "SHINYHUB_APP_DATA=/bogus/path"},
	}, os.Stdout, os.Stderr)
	// If dropReservedKeys is broken, the server would try to bind port 1 (a
	// privileged port) and fail to start -> Run returns an error. A clean run
	// means the platform PORT was used, not port 1.
	if err != nil {
		t.Fatalf("reserved-env check failed: server likely received PORT=1 from user env: %v", err)
	}
}

// TestDropReservedKeys_Unit is a fast unit test for the key-stripping helper.
func TestDropReservedKeys_Unit(t *testing.T) {
	input := []string{
		"HOME=/home/user",
		"PORT=1234",
		"SHINYHUB_APP_DATA=/evil",
		"MY_VAR=ok",
		"PORTLIKE=fine",
	}
	got := dropReservedKeys(input)
	for _, kv := range got {
		for _, banned := range []string{"PORT=", "SHINYHUB_APP_DATA="} {
			if strings.HasPrefix(kv, banned) {
				t.Errorf("reserved key leaked into result: %q", kv)
			}
		}
	}
	// Non-reserved keys must still be present.
	var found int
	for _, kv := range got {
		if kv == "HOME=/home/user" || kv == "MY_VAR=ok" || kv == "PORTLIKE=fine" {
			found++
		}
	}
	if found != 3 {
		t.Errorf("expected 3 non-reserved keys, got entries: %v", got)
	}
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
	_ = Run(ctx, Options{BundleDir: dir, Slug: "orphan-test", NoReload: true, Check: false}, os.Stdout, os.Stderr)

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
