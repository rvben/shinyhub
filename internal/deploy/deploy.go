package deploy

import (
	"archive/zip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/rvben/shinyhost/internal/process"
	"github.com/rvben/shinyhost/internal/proxy"
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

// SetPortCounter overrides the internal port counter. Used only in tests.
func SetPortCounter(v int64) {
	portCounter.Store(v)
}

// Params controls a single deploy operation.
type Params struct {
	Slug            string
	BundleDir       string
	Command         []string      // if empty, defaults to uv run shiny run app.py
	Env             []string
	Workers         int
	Manager         *process.Manager
	Proxy           *proxy.Proxy
	SkipHealthCheck bool          // skip HTTP health polling; intended for tests
	HealthTimeout   time.Duration // 0 means the 30 s default
}

// Result contains identifiers for the successfully deployed process.
type Result struct {
	PID  int
	Port int
}

// Run orchestrates a deploy: spawns a new process, optionally health-checks it,
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

	if !p.SkipHealthCheck {
		timeout := p.HealthTimeout
		if timeout == 0 {
			timeout = 30 * time.Second
		}
		if err := waitHealthy(port, timeout); err != nil {
			p.Manager.Stop(p.Slug) //nolint:errcheck
			return nil, fmt.Errorf("health check failed: %w", err)
		}
	}

	targetURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	if err := p.Proxy.Register(p.Slug, targetURL); err != nil {
		p.Manager.Stop(p.Slug) //nolint:errcheck
		return nil, fmt.Errorf("proxy register: %w", err)
	}

	return &Result{PID: info.PID, Port: port}, nil
}

// waitHealthy polls the app's root endpoint until it responds with a non-5xx
// status or the deadline is exceeded.
func waitHealthy(port int, timeout time.Duration) error {
	url := fmt.Sprintf("http://127.0.0.1:%d/", port)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url) //nolint:noctx
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
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			// Skip entries that would escape the destination directory.
			continue
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
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, rc)
	return err
}

// CreateTestBundle writes a zip archive at path containing the provided files.
// Intended for use in tests only.
func CreateTestBundle(path string, files map[string]string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := zip.NewWriter(f)
	defer w.Close()
	for name, content := range files {
		fw, err := w.Create(name)
		if err != nil {
			return err
		}
		if _, err := fw.Write([]byte(content)); err != nil {
			return err
		}
	}
	return nil
}
