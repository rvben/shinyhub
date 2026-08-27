package localrun

import (
	"bytes"
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

	"github.com/rvben/shinyhub/internal/bundle"
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

func TestRun_CheckPresentsDevelopmentSummaryWithoutEnvironmentValues(t *testing.T) {
	dir := writeHealthyFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var stdout bytes.Buffer
	err := Run(ctx, Options{
		BundleDir: dir, StateDir: t.TempDir(), Slug: "fixture",
		Check: true, NoReload: true, Env: []string{"API_TOKEN=super-secret-value"},
	}, &stdout, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	output := stdout.String()
	for _, want := range []string{
		"Local preflight", "  Source:", "  Workspace:", "  Data:",
		"  Reload: off; exits after the first healthy start", "  Environment: API_TOKEN",
		"Ready\n  App: http://127.0.0.1:",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("summary omitted %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "super-secret-value") {
		t.Fatalf("summary exposed an environment value:\n%s", output)
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

func TestRun_FleetInputReloadKeepsLastHealthyAppAndRecovers(t *testing.T) {
	skipIfNoPython3(t)
	root := t.TempDir()
	source := filepath.Join(root, "app")
	shared := filepath.Join(root, "_shared", "version.txt")
	if err := os.MkdirAll(filepath.Dir(shared), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	server := `import http.server, os, sys
version = open("helpers/version.txt").read().strip()
if version == "bad": sys.exit(3)
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        body = open("helpers/version.txt").read().strip().encode()
        self.send_response(200)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)
    def log_message(self, fmt, *args): pass
http.server.HTTPServer(("127.0.0.1", int(os.environ["PORT"])), H).serve_forever()
`
	for path, body := range map[string]string{
		filepath.Join(source, "server.py"):     server,
		filepath.Join(source, "shinyhub.toml"): "[app]\ncommand = [\"python3\", \"server.py\"]\n",
		filepath.Join(root, "fleet.toml"):      "fleet_id = \"test\"\n",
		shared:                                 "v1\n",
	} {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	port := deploy.AllocatePort()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{
			BundleDir: source, ManifestPath: filepath.Join(root, "fleet.toml"),
			BundleInputs: []bundle.FileInputSpec{{From: "_shared/version.txt", To: "helpers/version.txt"}},
			StateDir:     t.TempDir(), Slug: "fleet-input-reload", Port: port, NoSync: true,
		}, io.Discard, io.Discard)
	}()
	url := fmt.Sprintf("http://127.0.0.1:%d/app/fleet-input-reload/", port)
	waitForBody(t, url, "v1", 5*time.Second)

	if err := os.WriteFile(shared, []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitForBody(t, url, "v2", 6*time.Second)
	if err := os.MkdirAll(filepath.Join(source, "helpers"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "helpers", "version.txt"), []byte("collision\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1 * time.Second)
	waitForBody(t, url, "v2", 2*time.Second)
	if err := os.RemoveAll(filepath.Join(source, "helpers")); err != nil {
		t.Fatal(err)
	}
	waitForBody(t, url, "v2", 3*time.Second)

	if err := os.WriteFile(shared, []byte("bad\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond)
	waitForBody(t, url, "v2", 2*time.Second)
	if err := os.Remove(shared); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1 * time.Second)
	waitForBody(t, url, "v2", 2*time.Second)

	if err := os.WriteFile(shared, []byte("v3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitForBody(t, url, "v3", 6*time.Second)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runner shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runner did not stop")
	}
	if _, err := os.Stat(filepath.Join(source, "helpers")); !os.IsNotExist(err) {
		t.Fatalf("fleet input leaked into app source: %v", err)
	}
}

func TestRun_InvalidFleetInputLeavesNoLocalState(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "app")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "shinyhub.toml"), []byte("[app]\ncommand = [\"missing-command\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(root, "fleet.toml")
	if err := os.WriteFile(manifest, []byte("fleet_id = \"test\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	err := Run(context.Background(), Options{
		BundleDir: source, ManifestPath: manifest, StateDir: state,
		BundleInputs: []bundle.FileInputSpec{{From: "_shared/missing.py", To: "helpers/shared.py"}},
	}, io.Discard, io.Discard)
	var validationErr *ValidationError
	if !errors.As(err, &validationErr) || !strings.Contains(err.Error(), "missing.py") {
		t.Fatalf("error = %v, want missing-input validation error", err)
	}
	if _, statErr := os.Stat(state); !os.IsNotExist(statErr) {
		t.Fatalf("invalid input created local state: %v", statErr)
	}
}

func TestRun_FleetWorkspaceAndDataMustStayOutsideManifestRoot(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "app")
	sharedDir := filepath.Join(root, "_shared")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sharedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "shinyhub.toml"), []byte("[app]\ncommand = [\"missing-command\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sharedDir, "helper.py"), []byte("shared\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(root, "fleet.toml")
	if err := os.WriteFile(manifest, []byte("fleet_id = \"test\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name    string
		state   string
		data    string
		created string
	}{
		{name: "state", state: filepath.Join(sharedDir, "state"), created: filepath.Join(sharedDir, "state")},
		{name: "data", state: t.TempDir(), data: filepath.Join(sharedDir, "data"), created: filepath.Join(sharedDir, "data")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := Run(context.Background(), Options{
				BundleDir: source, ManifestPath: manifest, StateDir: tc.state, DataDir: tc.data,
				BundleInputs: []bundle.FileInputSpec{{From: "_shared/helper.py", To: "helpers/shared.py"}},
			}, io.Discard, io.Discard)
			var validationErr *ValidationError
			if !errors.As(err, &validationErr) || !strings.Contains(err.Error(), "fleet manifest root") {
				t.Fatalf("error = %v, want protected fleet-root validation error", err)
			}
			if _, statErr := os.Stat(tc.created); !os.IsNotExist(statErr) {
				t.Fatalf("rejected location was created: %v", statErr)
			}
		})
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
// stopChild must treat "Wait has already returned" as exited even when another
// select (waitUntilReady, waitForExit, the reload loop) consumed the value from
// exitCh first, and even when the leader died from a signal rather than a call
// to exit(). Otherwise it signals a group whose leader is already reaped and
// then blocks for the whole shutdown grace waiting for a value that can never
// arrive.
func TestStopChild_DoesNotWaitOutGraceWhenExitChAlreadyDrained(t *testing.T) {
	skipIfNoPython3(t)
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "grandchild.pid")

	// The leader spawns a grandchild that outlives it, then sleeps. Keeping the
	// grandchild alive keeps the process group alive, so the SIGTERM below
	// succeeds instead of failing with ESRCH.
	script := fmt.Sprintf(`import subprocess, time
p = subprocess.Popen(["sleep", "60"])
with open(%q, "w") as f:
    f.write(str(p.pid))
time.sleep(60)
`, pidFile)
	if err := os.WriteFile(filepath.Join(dir, "leader.py"), []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("python3", filepath.Join(dir, "leader.py"))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// Reap through the same helper production uses, so this pins the real
	// exit-signalling contract rather than a reimplementation of it.
	exitCh := watchExit(cmd)

	grandchildPID := waitForPIDFile(t, pidFile)

	// SIGKILL the leader alone (not the group) so ProcessState reports a signal
	// death rather than a normal exit, then consume exitCh the way a concurrent
	// select in Run would.
	if err := syscall.Kill(cmd.Process.Pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	<-exitCh

	done := make(chan struct{})
	go func() {
		stopChild(cmd, exitCh, io.Discard)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("stopChild blocked on an exitCh that was already drained; it waited out the 5 s shutdown grace instead of noticing the leader was reaped")
	}

	// The surviving grandchild must still be torn down.
	proc, err := os.FindProcess(grandchildPID)
	if err != nil {
		return
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if sigErr := proc.Signal(syscall.Signal(0)); sigErr != nil {
			return // reaped
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("grandchild (pid %d) survived stopChild", grandchildPID)
}

func waitForPIDFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil {
			var pid int
			if _, err := fmt.Sscan(strings.TrimSpace(string(b)), &pid); err == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("grandchild PID file %s never appeared", path)
	return 0
}

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
