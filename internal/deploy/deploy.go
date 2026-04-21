package deploy

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rvben/shinyhub/internal/bundle"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
)

var portCounter atomic.Int64

func init() {
	portCounter.Store(20000)
}

// pythonSyncFn / rSyncFn are package-level indirections so tests can observe
// (or replace) host-side dependency installation. Production code always
// goes through process.Sync / process.SyncR.
var (
	pythonSyncFn = process.Sync
	rSyncFn      = process.SyncR
)

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

// Params controls a deploy operation.
type Params struct {
	Slug      string
	BundleDir string
	// Command overrides auto-detection. If empty, the app type is detected from
	// the bundle and the appropriate runtime command is built per replica.
	Command         []string
	Env             []string
	Workers         int
	Replicas        int // 0 → 1 (single-replica fallback)
	Manager         *process.Manager
	Proxy           *proxy.Proxy
	HealthTimeout   time.Duration // 0 means the 120 s default
	MemoryLimitMB   int          // 0 = no limit
	CPUQuotaPercent int          // 0 = no limit; 100 = 1 full core
	// HealthCheck is called after each replica starts to verify it is ready.
	// If nil, the default HTTP health poller is used.
	// Set to a no-op function in tests that do not serve HTTP.
	HealthCheck func(port int, timeout time.Duration) error
}

// Result contains identifiers for a single successfully deployed replica.
type Result struct {
	Index int
	PID   int
	Port  int
}

// PoolResult contains the full set of replicas that were successfully booted.
type PoolResult struct {
	Replicas []Result
}

// resolveBootParams resolves Command defaults, HealthCheck defaults, and
// HealthTimeout defaults for a pool/replica boot. Returns the resolved
// base command, detected app type, the effective health-check func, and
// the effective timeout.
func resolveBootParams(p Params) (baseCmd []string, appType string, hc func(int, time.Duration) error, timeout time.Duration, err error) {
	if len(p.Command) > 0 {
		baseCmd = p.Command
	} else {
		appType = DetectAppType(p.BundleDir)
		// Container runtimes prepare dependencies inside the image/container, so
		// running uv sync / renv::restore on the host would leak host state into
		// what is supposed to be an isolated boot path (and fail outright on
		// hosts where uv/Rscript aren't installed).
		hostDeps := p.Manager == nil || p.Manager.HostPreparesDeps()
		switch appType {
		case "python":
			if hostDeps {
				if err = pythonSyncFn(p.BundleDir); err != nil {
					return nil, "", nil, 0, fmt.Errorf("uv sync: %w", err)
				}
			}
		case "r":
			if hostDeps {
				if err = rSyncFn(p.BundleDir); err != nil {
					return nil, "", nil, 0, fmt.Errorf("renv restore: %w", err)
				}
			}
		default:
			return nil, "", nil, 0, fmt.Errorf("no app.py or app.R found in %s", p.BundleDir)
		}
		// baseCmd remains nil — bootReplica constructs the per-replica command
		// using the real port once it is allocated.
	}

	hc = p.HealthCheck
	if hc == nil {
		hc = waitHealthy
	}
	timeout = p.HealthTimeout
	if timeout == 0 {
		timeout = 120 * time.Second
	}
	return baseCmd, appType, hc, timeout, nil
}

// Run orchestrates a parallel pool deploy: spawns N replicas concurrently,
// health-checks each, and registers surviving replicas with the reverse proxy.
// Partial failure (some replicas healthy, some not) is accepted and logged.
// All-fail returns an error.
func Run(p Params) (*PoolResult, error) {
	if p.Replicas <= 0 {
		p.Replicas = 1
	}

	p.Proxy.SetPoolSize(p.Slug, p.Replicas)

	baseCmd, appType, hc, timeout, err := resolveBootParams(p)
	if err != nil {
		return nil, err
	}

	type bootResult struct {
		idx int
		res Result
		err error
	}
	results := make(chan bootResult, p.Replicas)
	var wg sync.WaitGroup

	for i := 0; i < p.Replicas; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			r, err := bootReplica(p, idx, baseCmd, appType, hc, timeout)
			results <- bootResult{idx: idx, res: r, err: err}
		}(i)
	}
	wg.Wait()
	close(results)

	ok := make([]Result, 0, p.Replicas)
	var bootErrs []error
	for br := range results {
		if br.err != nil {
			bootErrs = append(bootErrs, fmt.Errorf("replica %d: %w", br.idx, br.err))
			continue
		}
		ok = append(ok, br.res)
	}
	sort.Slice(ok, func(a, b int) bool { return ok[a].Index < ok[b].Index })

	if len(ok) == 0 {
		return nil, fmt.Errorf("all replicas failed health check: %w", errors.Join(bootErrs...))
	}
	for _, e := range bootErrs {
		slog.Warn("replica boot failed", "slug", p.Slug, "err", e)
	}
	return &PoolResult{Replicas: ok}, nil
}

// bootReplica starts a single replica: allocates a port, starts the process,
// health-checks it, and registers it with the proxy. baseCmd == nil signals
// that the command should be built from appType using the allocated port.
func bootReplica(p Params, idx int, baseCmd []string, appType string, hc func(int, time.Duration) error, timeout time.Duration) (Result, error) {
	port := AllocatePort()

	var cmd []string
	if baseCmd != nil {
		cmd = baseCmd
	} else {
		bindHost := "127.0.0.1"
		if p.Manager != nil {
			bindHost = p.Manager.AppBindHost()
		}
		switch appType {
		case "python":
			workers := p.Workers
			if workers <= 0 {
				workers = 1
			}
			cmd = buildCommand(p.BundleDir, port, workers, bindHost)
		case "r":
			cmd = BuildRCommand(p.BundleDir, port, bindHost)
		default:
			return Result{}, fmt.Errorf("no app.py or app.R found in %s", p.BundleDir)
		}
	}

	env := append(append([]string{}, p.Env...), fmt.Sprintf("PORT=%d", port))

	info, err := p.Manager.Start(process.StartParams{
		Slug:            p.Slug,
		Index:           idx,
		Dir:             p.BundleDir,
		Command:         cmd,
		Port:            port,
		Env:             env,
		MemoryLimitMB:   p.MemoryLimitMB,
		CPUQuotaPercent: p.CPUQuotaPercent,
	})
	if err != nil {
		return Result{}, fmt.Errorf("start: %w", err)
	}

	if err := hc(port, timeout); err != nil {
		_ = p.Manager.StopReplica(p.Slug, idx)
		return Result{}, fmt.Errorf("health: %w", err)
	}

	targetURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	if err := p.Proxy.RegisterReplica(p.Slug, idx, targetURL); err != nil {
		_ = p.Manager.StopReplica(p.Slug, idx)
		return Result{}, fmt.Errorf("register: %w", err)
	}
	return Result{Index: idx, PID: info.PID, Port: port}, nil
}

// RunReplica boots a single replica at the given index. The proxy pool size
// must already be set to at least index+1 before calling this function.
// Used by the watchdog's per-replica crash-restart path.
func RunReplica(p Params, index int) (*Result, error) {
	baseCmd, appType, hc, timeout, err := resolveBootParams(p)
	if err != nil {
		return nil, err
	}
	r, err := bootReplica(p, index, baseCmd, appType, hc, timeout)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// DetectAppType returns "python" if app.py exists, "r" if app.R exists, or ""
// if neither is found.
func DetectAppType(bundleDir string) string {
	if _, err := os.Stat(filepath.Join(bundleDir, "app.py")); err == nil {
		return "python"
	}
	if _, err := os.Stat(filepath.Join(bundleDir, "app.R")); err == nil {
		return "r"
	}
	return ""
}

// BuildRCommand returns the command to start an R Shiny app on the given port.
// bindHost is the address the app listens on inside its execution environment
// (the host for native, the container for Docker bridge mode).
func BuildRCommand(bundleDir string, port int, bindHost string) []string {
	expr := fmt.Sprintf(
		`shiny::runApp('.', host='%s', port=%d, launch.browser=FALSE)`, bindHost, port)
	return []string{"Rscript", "--vanilla", "-e", expr}
}

// buildCommand constructs the uv launch command for a bundle directory.
// If a pyproject.toml is present, uv sync has already prepared the environment
// and we use plain `uv run`. If only requirements.txt is present, we pass
// --with-requirements so uv installs deps into an ephemeral environment.
// bindHost has the same meaning as in BuildRCommand.
func buildCommand(bundleDir string, port, workers int, bindHost string) []string {
	base := []string{"uv", "run", "--no-project"}
	if _, err := os.Stat(filepath.Join(bundleDir, "pyproject.toml")); err == nil {
		// Project mode: environment was synced by process.Sync.
		base = []string{"uv", "run"}
	} else if _, err := os.Stat(filepath.Join(bundleDir, "requirements.txt")); err == nil {
		base = append(base, "--with-requirements", "requirements.txt")
	}
	return append(base,
		"shiny", "run", "app.py",
		"--host", bindHost,
		"--port", fmt.Sprintf("%d", port),
	)
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

// ErrBundleTooLarge is returned by ExtractBundle when a single entry, or the
// combined size of all entries, exceeds the configured limits. Zip-bomb
// protection: uncompressed sizes in the zip header are attacker-controlled, so
// we also enforce the caps while streaming bytes to disk.
var ErrBundleTooLarge = errors.New("bundle exceeds extracted size limit")

// ErrBundleRejected is returned by ExtractBundle when a bundle entry violates
// the content policy (data dirs, forbidden extensions, etc.). Callers can use
// errors.Is to map this to a 422 Unprocessable Entity response.
var ErrBundleRejected = errors.New("bundle rejected")

const (
	// DefaultMaxEntrySize caps the extracted size of a single file inside the
	// bundle. Matches the upload size cap — a single file can never be larger
	// than the full archive.
	DefaultMaxEntrySize int64 = 128 << 20
	// DefaultMaxBundleSize caps the combined extracted size of all entries.
	DefaultMaxBundleSize int64 = 512 << 20
)

// ExtractBundle unzips src into destDir with the default size limits.
func ExtractBundle(src, destDir string) error {
	return ExtractBundleWithLimits(src, destDir, DefaultMaxEntrySize, DefaultMaxBundleSize)
}

// ExtractBundleWithLimits unzips src into destDir, rejecting any entry whose
// resolved path would escape destDir (zip-slip protection) and enforcing both
// a per-entry and aggregate size cap (zip-bomb protection). A zero or negative
// limit means unlimited.
func ExtractBundleWithLimits(src, destDir string, maxEntrySize, maxTotalSize int64) error {
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

	rules := bundle.DefaultRules()

	var total int64
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

		// Apply bundle filter rules before any disk side effects. Cache dirs are
		// silently skipped; data dirs and disallowed extensions are hard errors.
		decision := rules.Inspect(f.Name, int64(f.UncompressedSize64))
		switch decision {
		case bundle.FilterAccept:
			// proceed with extraction
		case bundle.FilterSkipCacheDir:
			continue
		case bundle.FilterRejectDataDir,
			bundle.FilterRejectDatasetDir,
			bundle.FilterRejectExtension,
			bundle.FilterRejectFileSize:
			return fmt.Errorf("%w: bundle entry %q: %s", ErrBundleRejected, f.Name, decision)
		default:
			return fmt.Errorf("bundle entry %q: unhandled filter decision %v", f.Name, decision)
		}

		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
			continue
		}

		// Trust-but-verify: reject up front when the declared size is already
		// over budget so we avoid any extraction work for obviously malicious
		// archives.
		if maxEntrySize > 0 && int64(f.UncompressedSize64) > maxEntrySize {
			return fmt.Errorf("%w: %q declared %d bytes", ErrBundleTooLarge, f.Name, f.UncompressedSize64)
		}

		written, err := extractFile(f, target, maxEntrySize)
		if err != nil {
			return err
		}
		total += written
		if maxTotalSize > 0 && total > maxTotalSize {
			return fmt.Errorf("%w: extracted %d bytes exceeds %d", ErrBundleTooLarge, total, maxTotalSize)
		}
	}
	return nil
}

// extractFile streams f into dest, capped at maxEntrySize bytes. Returns the
// number of bytes written. If the entry produces more bytes than the cap, the
// copy is aborted and ErrBundleTooLarge is returned; the partially-written
// file is removed so caller cleanup logic isn't needed.
func extractFile(f *zip.File, dest string, maxEntrySize int64) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return 0, err
	}
	rc, err := f.Open()
	if err != nil {
		return 0, err
	}
	defer rc.Close()
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
	if err != nil {
		return 0, err
	}

	var src io.Reader = rc
	if maxEntrySize > 0 {
		// Read one extra byte so we can detect an overflow.
		src = io.LimitReader(rc, maxEntrySize+1)
	}
	n, copyErr := io.Copy(out, src)
	closeErr := out.Close()
	if copyErr != nil {
		os.Remove(dest)
		return 0, copyErr
	}
	if closeErr != nil {
		os.Remove(dest)
		return 0, closeErr
	}
	if maxEntrySize > 0 && n > maxEntrySize {
		os.Remove(dest)
		return 0, fmt.Errorf("%w: %q expanded past %d bytes", ErrBundleTooLarge, f.Name, maxEntrySize)
	}
	return n, nil
}

// ResolveMemoryLimitMB returns perAppMB if non-nil, otherwise defaultMB.
// Zero means no limit in both cases.
func ResolveMemoryLimitMB(perAppMB *int, defaultMB int) int {
	if perAppMB != nil {
		return *perAppMB
	}
	return defaultMB
}

// ResolveCPUQuotaPercent returns perAppPct if non-nil, otherwise defaultPct.
// Zero means no limit in both cases.
func ResolveCPUQuotaPercent(perAppPct *int, defaultPct int) int {
	if perAppPct != nil {
		return *perAppPct
	}
	return defaultPct
}
