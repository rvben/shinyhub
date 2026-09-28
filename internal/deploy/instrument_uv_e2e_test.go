//go:build unix

package deploy

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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
	syncCtx, cancelSync := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancelSync()
	if err := pythonSyncFn(syncCtx, dir, nil); err != nil {
		t.Fatalf("preparing the app's .venv: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".venv", "bin", "shiny")); err != nil {
		t.Fatalf("the .venv must carry its own shiny console script for this test to mean anything: %v", err)
	}

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()

	argv := buildCommand(dir, port, 1, "127.0.0.1", instrumentOverlay(true, nil), true)
	if !useProjectMode(dir, true) || !strings.Contains(strings.Join(argv, " "), "--no-sync") {
		t.Fatalf("launch is not the on-host project-mode command: %v", argv)
	}
	runCtx, cancelRun := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancelRun()
	cmd := exec.CommandContext(runCtx, argv[0], argv[1:]...)
	cmd.Dir = dir
	// uv runs the app as its child, so stop the whole process group rather
	// than leave the server running after the test.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.Env = process.WithBuildInterpreterPolicy(append(os.Environ(),
		"OTEL_SERVICE_NAME=demo",
		"OTEL_TRACES_EXPORTER=console",
		"OTEL_METRICS_EXPORTER=none",
		"OTEL_LOGS_EXPORTER=none",
		"OTEL_BSP_SCHEDULE_DELAY=100",
	))
	var out lockedBuffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %v: %v", argv, err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() {
		cancelRun()
		<-exited
	})

	url := fmt.Sprintf("http://127.0.0.1:%d/", port)
	served := false
	for deadline := time.Now().Add(3 * time.Minute); time.Now().Before(deadline) && !served; {
		select {
		case err := <-exited:
			t.Fatalf("instrumented app exited before serving (%v):\n%s", err, out.String())
		case <-time.After(250 * time.Millisecond):
		}
		resp, err := http.Get(url)
		if err != nil {
			continue
		}
		_ = resp.Body.Close()
		served = resp.StatusCode == http.StatusOK
	}
	if !served {
		t.Fatalf("instrumented app never answered 200 on %s:\n%s", url, out.String())
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
}
