package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestServeBootsAndStopsInProcess runs the real serve path against a fresh
// SQLite database in temporary directories: it must become ready, answer the
// dashboard and the API, and return cleanly once its context is cancelled,
// leaving no goroutine of its own behind.
func TestServeBootsAndStopsInProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("boots the full server")
	}
	dir := t.TempDir()
	port := freeTCPPort(t)
	cfgFile := filepath.Join(dir, "shinyhub.yaml")
	cfg := fmt.Sprintf(`server:
  host: 127.0.0.1
  port: %d
database:
  dsn: %q
storage:
  apps_dir: %q
  app_data_dir: %q
`, port, filepath.Join(dir, "shinyhub.db"), filepath.Join(dir, "apps"), filepath.Join(dir, "app-data"))
	if err := os.WriteFile(cfgFile, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHINYHUB_AUTH_SECRET", "boot-test-secret-boot-test-secret-boot-test")
	t.Setenv("SHINYHUB_ADMIN_USER", "admin")
	t.Setenv("SHINYHUB_ADMIN_PASSWORD", "admin-password")
	prev := configPath
	configPath = cfgFile
	t.Cleanup(func() { configPath = prev })

	before := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	done := make(chan error, 1)
	go func() { done <- runServe(ctx, logger, serveOptions{ErrOut: io.Discard}) }()

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	waitForStatus(t, done, base+"/readyz", http.StatusOK, 30*time.Second)
	waitForStatus(t, done, base+"/api/server-info", http.StatusOK, 5*time.Second)

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runServe returned %v after cancellation", err)
		}
	case <-time.After(45 * time.Second):
		t.Fatal("runServe did not return within 45s of cancellation")
	}
	http.DefaultClient.CloseIdleConnections()
	assertNoServeGoroutines(t, before)
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func waitForStatus(t *testing.T, done <-chan error, url string, want int, budget time.Duration) {
	t.Helper()
	deadline := time.Now().Add(budget)
	last := "no response"
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			t.Fatalf("runServe returned %v before %s answered %d", err, url, want)
		default:
		}
		resp, err := http.Get(url)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == want {
				return
			}
			last = resp.Status
		} else {
			last = err.Error()
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s did not answer %d within %s (last: %s)", url, want, budget, last)
}

// assertNoServeGoroutines waits for goroutines started by this module to
// exit. Only stacks running shinyhub code count: the runtime, the test
// framework and net/http's idle client connections own the rest.
func assertNoServeGoroutines(t *testing.T, before int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var leaked []string
	for {
		leaked = serveGoroutines()
		if len(leaked) == 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(leaked) > 0 {
		t.Fatalf("%d shinyhub goroutine(s) outlived runServe (started with %d total):\n\n%s",
			len(leaked), before, strings.Join(leaked, "\n\n"))
	}
}

func serveGoroutines() []string {
	buf := make([]byte, 1<<22)
	buf = buf[:runtime.Stack(buf, true)]
	var out []string
	for _, g := range strings.Split(string(buf), "\n\n") {
		if !strings.Contains(g, "github.com/rvben/shinyhub/") {
			continue
		}
		if strings.Contains(g, "TestServeBootsAndStopsInProcess") || strings.Contains(g, "assertNoServeGoroutines") {
			continue
		}
		out = append(out, g)
	}
	return out
}
