package deploy

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
)

var portCounter atomic.Int64

func init() {
	portCounter.Store(20000)
}

// AllocatePort returns a unique port in the 20000–60000 range.
// The counter wraps back to 20001 after reaching 60000.
func AllocatePort() int {
	for {
		p := portCounter.Add(1)
		if p <= 60000 {
			return int(p)
		}
		// Another goroutine may have already reset; use CompareAndSwap to let
		// exactly one resetter win and avoid a thundering-herd reset loop.
		portCounter.CompareAndSwap(p, 20000)
		// Re-try; the next Add will land at 20001.
	}
}

// Params controls a single deploy operation.
type Params struct {
	Slug          string
	BundleDir     string
	Command       []string // if empty, defaults to uv run shiny run app.py
	Env           []string
	Workers       int
	Manager       *process.Manager
	Proxy         *proxy.Proxy
	HealthTimeout time.Duration // 0 means the 30 s default
	// HealthCheck is called after the process starts to verify it is ready.
	// If nil, the default HTTP health poller is used.
	// Set to a no-op function in tests that do not serve HTTP.
	HealthCheck func(port int, timeout time.Duration) error
}

// Result contains identifiers for the successfully deployed process.
type Result struct {
	PID  int
	Port int
}

// Run orchestrates a deploy: spawns a new process, health-checks it,
// then registers it with the reverse proxy.
func Run(p Params) (*Result, error) {
	port := AllocatePort()

	cmd := p.Command
	if len(cmd) == 0 {
		workers := p.Workers
		if workers <= 0 {
			workers = 1
		}
		cmd = []string{
			"uv", "run", "shiny", "run", "app.py",
			"--host", "127.0.0.1",
			"--port", fmt.Sprintf("%d", port),
			"--workers", fmt.Sprintf("%d", workers),
		}
	}

	env := append(p.Env, fmt.Sprintf("PORT=%d", port))

	info, err := p.Manager.Start(process.StartParams{
		Slug:    p.Slug,
		Dir:     p.BundleDir,
		Command: cmd,
		Port:    port,
		Env:     env,
	})
	if err != nil {
		return nil, fmt.Errorf("start: %w", err)
	}

	healthCheck := p.HealthCheck
	if healthCheck == nil {
		healthCheck = waitHealthy
	}

	timeout := p.HealthTimeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	if err := healthCheck(port, timeout); err != nil {
		stopErr := p.Manager.Stop(p.Slug)
		return nil, errors.Join(fmt.Errorf("health check failed: %w", err), stopErr)
	}

	targetURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	if err := p.Proxy.Register(p.Slug, targetURL); err != nil {
		stopErr := p.Manager.Stop(p.Slug)
		return nil, errors.Join(fmt.Errorf("proxy register: %w", err), stopErr)
	}

	return &Result{PID: info.PID, Port: port}, nil
}

// waitHealthy polls the app's root endpoint until it responds with a non-5xx
// status or the deadline is exceeded. Each HTTP attempt is capped at 5 seconds.
func waitHealthy(port int, timeout time.Duration) error {
	client := &http.Client{Timeout: 5 * time.Second}
	healthURL := fmt.Sprintf("http://127.0.0.1:%d/", port)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithDeadline(context.Background(), deadline)
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
		resp, err := client.Do(req)
		cancel()
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode < 500 {
				return nil
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("app on port %d did not become healthy within %s", port, timeout)
}

// ExtractBundle unzips src into destDir, rejecting any entry whose resolved
// path would escape destDir (zip-slip protection).
func ExtractBundle(src, destDir string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}
	defer r.Close()

	if err := os.MkdirAll(destDir, 0755); err != nil {
		return err
	}

	// Resolve destDir to its real absolute path once so comparisons are stable.
	absDestDir, err := filepath.Abs(destDir)
	if err != nil {
		return err
	}

	for _, f := range r.File {
		// filepath.Join cleans the path, which resolves any ".." components.
		target := filepath.Join(absDestDir, filepath.Clean(f.Name))

		// Verify the resolved path is still inside destDir.
		// filepath.Rel returns a path starting with ".." when target is outside
		// absDestDir. The separator-aware check catches both ".." and "../foo".
		rel, err := filepath.Rel(absDestDir, target)
		if err != nil || strings.HasPrefix(rel, "..") {
			return fmt.Errorf("zip-slip detected in %q: entry escapes destination", f.Name)
		}

		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
			continue
		}
		if err := extractFile(f, target); err != nil {
			return err
		}
	}
	return nil
}

func extractFile(f *zip.File, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return err
	}
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, rc)
	return err
}
