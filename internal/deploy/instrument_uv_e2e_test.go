//go:build unix

package deploy

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/process"
)

// lockedBuffer collects a child's output while the test reads it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestInstrumentedProjectModeLaunch_LoadsInstrumentation boots a project-mode
// Shiny app, its .venv prepared on the host as the native runtime does, through
// the production launch command with the auto-instrument overlay. The overlay is
// a separate environment layered over the .venv, so the entrypoint must run
// under the overlay's interpreter: under the .venv's own interpreter
// opentelemetry-instrument's sitecustomize cannot import the instrumentation,
// and the app serves requests with no span, which no health check notices. The
// console exporter makes the request's server span observable in the output.
func TestInstrumentedProjectModeLaunch_LoadsInstrumentation(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a real uv-managed Shiny app")
	}
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not in PATH")
	}
	// Exercise the supported standard-Python launch, independently of any
	// free-threaded interpreter installed on the developer's host.
	previousPolicy := append([]string(nil), process.WithBuildInterpreterPolicy(nil)...)
	process.SetBuildInterpreterPolicy([]string{"UV_PYTHON=3.14+gil"})
	t.Cleanup(func() { process.SetBuildInterpreterPolicy(previousPolicy) })

	// The overlay and the app's dependencies come from the package index. An
	// unreachable index is an environment limit, not a failure of the launch.
	probeCtx, cancelProbe := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancelProbe()
	probe := exec.CommandContext(probeCtx, "uv", "run", "--no-project", "--with", "opentelemetry-distro", "python", "-c", "pass")
	probe.Dir = t.TempDir()
	probe.Env = process.WithBuildInterpreterPolicy(os.Environ())
	if out, err := probe.CombinedOutput(); err != nil {
		t.Skipf("cannot fetch Python packages with uv (offline or no index access), so the instrumented launch is not exercised: %v\n%s", err, out)
	}

	dir := t.TempDir()
	files := map[string]string{
		"pyproject.toml": "[project]\nname = \"demo\"\nversion = \"0.1.0\"\nrequires-python = \">=3.10\"\ndependencies = [\"shiny\"]\n",
		"app.py":         "from shiny import App, ui\n\napp = App(ui.page_fluid(\"hello\"), None)\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	manager := process.NewManager(dir, process.NewNativeRuntime())
	manager.SetAutoInstrumentAppsDefault(true)
	if _, err := Run(Params{Slug: "demo", BundleDir: dir, Manager: manager, PrepareOnly: true}); err != nil {
		t.Fatalf("preparing the deployment: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".venv", "bin", "shiny")); err != nil {
		t.Fatalf("the .venv must carry its own shiny console script for this test to mean anything: %v", err)
	}
	var indexRequests atomic.Int64
	index := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		indexRequests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(index.Close)

	// The app cannot pick a port of its own and report it, so the test picks a
	// free one and releases it for the app to bind. Another process can take it
	// in between; the app then exits reporting the address in use, and only
	// that exit is retried, on a fresh port. Any other early exit fails.
	const launchAttempts = 5
	var out *lockedBuffer
	for attempt := 1; ; attempt++ {
		var served bool
		out, served = launchInstrumentedApp(t, dir, index.URL, attempt < launchAttempts)
		if served {
			break
		}
		t.Logf("launch attempt %d lost its port to another process; retrying on a fresh port", attempt)
	}

	// A sitecustomize import failure is reported at interpreter start, before
	// the app serves; a working instrumentation exports the request's span.
	for _, bad := range []string{"sitecustomize", "No module named 'opentelemetry"} {
		if strings.Contains(out.String(), bad) {
			t.Fatalf("instrumentation failed to load under the launched interpreter (%q):\n%s", bad, out.String())
		}
	}
	spanDeadline := time.Now().Add(30 * time.Second)
	for !strings.Contains(out.String(), `"trace_id"`) {
		if time.Now().After(spanDeadline) {
			t.Fatalf("the app served a request but exported no span:\n%s", out.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
	if n := indexRequests.Load(); n != 0 {
		t.Fatalf("launch made %d package-index requests during the outage", n)
	}
}

// launchInstrumentedApp starts the prepared app in dir on a free port through
// the production launch command and waits for it to serve a request. It
// reports false, with the app stopped, when the app exited because another
// process held the port and retry allows another attempt; any other failure
// ends the test.
func launchInstrumentedApp(t *testing.T, dir, indexURL string, retry bool) (*lockedBuffer, bool) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()

	opts := LaunchOptions{
		Port: port, BindHost: "127.0.0.1", CommandHostDeps: true, AutoInstrumentDefault: true,
	}
	plan, err := ResolveLaunch(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	argv := plan.Command
	if !useProjectMode(dir, true) || !strings.Contains(strings.Join(argv, " "), "--no-sync") {
		t.Fatalf("launch is not the on-host project-mode command: %v", argv)
	}
	if !plan.Instrumented || !strings.Contains(strings.Join(argv, " "), "--offline") {
		t.Fatal("launch does not use the prepared instrumentation environment")
	}
	opts.AutoInstrumentExtraPackages = []string{"a-different-instrumentation-package"}
	fallback, err := ResolveLaunch(dir, opts)
	if err != nil || fallback.Instrumented || strings.Contains(strings.Join(fallback.Command, " "), "--with") {
		t.Fatalf("missing overlay should use the app environment: %+v, %v", fallback, err)
	}
	runCtx, cancelRun := context.WithTimeout(context.Background(), 5*time.Minute)
	cmd := exec.CommandContext(runCtx, argv[0], argv[1:]...)
	cmd.Dir = dir
	// uv runs the app as its child, so stop the whole process group rather
	// than leave the server running after the test.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.Env = process.WithBuildInterpreterPolicy(append(os.Environ(),
		"UV_CACHE_DIR="+t.TempDir(),
		"UV_DEFAULT_INDEX="+indexURL+"/simple/",
		"UV_INDEX=", "UV_INDEX_URL=", "UV_EXTRA_INDEX_URL=", "UV_FIND_LINKS=",
		"OTEL_SERVICE_NAME=demo",
		"OTEL_TRACES_EXPORTER=console",
		"OTEL_METRICS_EXPORTER=none",
		"OTEL_LOGS_EXPORTER=none",
		"OTEL_BSP_SCHEDULE_DELAY=100",
	))
	out := &lockedBuffer{}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		cancelRun()
		t.Fatalf("start %v: %v", argv, err)
	}
	exited := make(chan struct{})
	var exitErr error
	go func() { exitErr = cmd.Wait(); close(exited) }()
	t.Cleanup(func() {
		cancelRun()
		<-exited
	})

	// The released port can also be held by a process that answers, so a 200
	// proves nothing about who served it; only the launched app's own access
	// log does, so requests continue until the app itself reports one.
	url := fmt.Sprintf("http://127.0.0.1:%d/", port)
	const ownAccessLog = `"GET / HTTP/1.1" 200`
	// A process holding the port may accept and never answer, so each probe
	// is bounded.
	client := &http.Client{Timeout: 5 * time.Second}
	request := func() {
		resp, err := client.Get(url)
		if err != nil {
			return
		}
		_ = resp.Body.Close()
	}
	for deadline := time.Now().Add(3 * time.Minute); !strings.Contains(out.String(), ownAccessLog); {
		if time.Now().After(deadline) {
			t.Fatalf("instrumented app never logged a 200 for %s:\n%s", url, out.String())
		}
		select {
		case <-exited:
			if retry && strings.Contains(strings.ToLower(out.String()), "address already in use") {
				return out, false
			}
			t.Fatalf("instrumented app exited before serving (%v):\n%s", exitErr, out.String())
		case <-time.After(250 * time.Millisecond):
		}
		request()
	}
	return out, true
}
