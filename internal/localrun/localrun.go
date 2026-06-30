// Package localrun provides the foreground app runner for `shinyhub run`.
// It resolves the exact launch a hub native runtime would perform and runs the
// app process locally, with readiness polling, --check mode, and signal handling.
package localrun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/process"
)

// Options configures a local foreground run.
type Options struct {
	// BundleDir is the app bundle directory to run (required).
	BundleDir string
	// Slug is a human label for log output. Defaults to the basename of BundleDir.
	Slug string
	// DataDir is the host path used as SHINYHUB_APP_DATA and symlinked to
	// <BundleDir>/data. When empty, defaults to <BundleDir>/.shinyhub-run/data.
	DataDir string
	// Port is the local TCP port to bind. When 0, a free port is allocated.
	Port int
	// Env is additional environment in KEY=VALUE form, layered above the
	// sanitized host env but below the platform-controlled PORT and SHINYHUB_APP_DATA.
	Env []string
	// NoSync skips the explicit dep-prep steps (uv sync / renv restore).
	NoSync bool
	// NoReload disables framework hot reload and the file-watch fallback.
	NoReload bool
	// Open opens the serving URL in the default browser after readiness.
	Open bool
	// Check runs in preflight mode: boot, verify healthy, stop, exit 0/1.
	Check bool
}

// reservedEnvKeys are env keys that localrun always controls; user-supplied
// values for these keys are silently dropped to prevent them from shadowing
// the platform-authoritative values appended later.
var reservedEnvKeys = []string{"PORT", "SHINYHUB_APP_DATA"}

// dropReservedKeys returns a copy of env with every "KEY=VALUE" entry whose
// key appears in reserved removed.
func dropReservedKeys(env []string) []string {
	reserved := make(map[string]struct{}, len(reservedEnvKeys))
	for _, k := range reservedEnvKeys {
		reserved[k] = struct{}{}
	}
	out := make([]string, 0, len(env))
	for _, kv := range env {
		key := kv
		if i := len(key); i > 0 {
			for j, c := range kv {
				if c == '=' {
					key = kv[:j]
					break
				}
			}
		}
		if _, blocked := reserved[key]; !blocked {
			out = append(out, kv)
		}
	}
	return out
}

// Run boots the app bundle and blocks until the context is cancelled (or, in
// --check mode, until the first healthy poll or crash). It streams all app
// output to stdout/stderr and returns a non-nil error on any failure.
func Run(ctx context.Context, o Options, stdout, stderr io.Writer) error {
	bundleDir, err := filepath.Abs(o.BundleDir)
	if err != nil {
		return fmt.Errorf("resolve bundle dir: %w", err)
	}

	slug := o.Slug
	if slug == "" {
		slug = filepath.Base(bundleDir)
	}

	// Step 1: Resolve the data dir and set up <bundle>/data symlink.
	// Absolutize so that the symlink target and SHINYHUB_APP_DATA agree
	// regardless of which working directory the caller used.
	dataDir := o.DataDir
	if dataDir == "" {
		dataDir = filepath.Join(bundleDir, ".shinyhub-run", "data")
	} else {
		abs, err := filepath.Abs(dataDir)
		if err != nil {
			return fmt.Errorf("resolve data dir: %w", err)
		}
		dataDir = abs
	}
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}

	linkPath := filepath.Join(bundleDir, "data")
	if err := ensureDataSymlink(linkPath, dataDir); err != nil {
		return err
	}
	// Deferred cleanup: remove the symlink only if it still points at our dataDir.
	defer removeDataSymlinkIfOwned(linkPath, dataDir, stderr)

	// Step 2: Allocate a free port unless one was provided.
	port := o.Port
	if port == 0 {
		port = deploy.AllocatePort()
	}

	// Step 3: Resolve the launch plan via the shared seam.
	launchOpts := deploy.LaunchOptions{
		Port:                  port,
		Workers:               1,
		BindHost:              "127.0.0.1",
		Reload:                !o.NoReload,
		PrepHostDeps:          !o.NoSync,
		CommandHostDeps:       !o.NoSync,
		AutoInstrumentDefault: false,
		HonorManifestTracing:  false,
	}
	plan, err := deploy.ResolveLaunch(bundleDir, launchOpts)
	if err != nil {
		return fmt.Errorf("resolve launch: %w", err)
	}

	// Step 4: Run each dep-prep step, streaming output to the terminal.
	for _, step := range plan.DepPrep {
		fmt.Fprintf(stdout, "==> %s\n", step.Label)
		// Pass the run's context so a Ctrl-C / cancellation kills an in-flight
		// build (uv sync / renv restore) promptly. It carries no build deadline,
		// so local runs remain un-timeout-bounded; the build_timeout budget is
		// server-deploy-only.
		if err := step.Run(ctx, bundleDir); err != nil {
			return fmt.Errorf("dep prep (%s): %w", step.Label, err)
		}
	}

	// Step 5: Assemble the child env. Precedence (lowest to highest):
	//   SanitizedEnv() base, then o.Env (--env/.env) with reserved keys
	//   stripped, then plan.Env (PORT), then SHINYHUB_APP_DATA.
	//   Platform vars always win over user-supplied env.
	userEnv := dropReservedKeys(o.Env)
	childEnv := append(process.SanitizedEnv(), userEnv...)
	childEnv = append(childEnv, plan.Env...)
	childEnv = append(childEnv, "SHINYHUB_APP_DATA="+dataDir)

	readyPath := plan.ReadyPath
	if readyPath == "" {
		readyPath = "/"
	}
	readyURL := fmt.Sprintf("http://127.0.0.1:%d%s", port, readyPath)

	// spawnChild starts the app process and returns the cmd + its exit channel.
	spawnChild := func() (*exec.Cmd, <-chan error, error) {
		c := exec.CommandContext(ctx, plan.Command[0], plan.Command[1:]...) //nolint:gosec
		c.Dir = bundleDir
		c.Env = childEnv
		c.Stdout = stdout
		c.Stderr = stderr
		c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := c.Start(); err != nil {
			return nil, nil, fmt.Errorf("start %s: %w", slug, err)
		}
		ch := make(chan error, 1)
		go func() { ch <- c.Wait() }()
		return c, ch, nil
	}

	// Step 6: First spawn.
	cmd, exitCh, err := spawnChild()
	if err != nil {
		return err
	}
	// Ensure the child group is always torn down when Run returns.
	defer func() { stopChild(cmd, exitCh, stderr) }()

	// Step 7: Poll for readiness concurrently with watching for early exit.
	// pollCtx is cancelled when Run returns so the poller never outlives Run.
	pollCtx, pollCancel := context.WithCancel(ctx)
	defer pollCancel()

	readyCh := make(chan struct{}, 1)
	go pollReady(pollCtx, readyURL, plan.Timeout, readyCh)

	startTimer := time.NewTimer(plan.Timeout)
	defer startTimer.Stop()

	select {
	case exitErr := <-exitCh:
		// The process exited before becoming healthy.
		code := exitCode(exitErr)
		msg := fmt.Sprintf("app exited during startup (exit %d)", code)
		fmt.Fprintln(stderr, msg)
		return errors.New(msg)

	case <-readyCh:
		// App is healthy.
		fmt.Fprintf(stdout, "serving on http://127.0.0.1:%d\n", port)

		if o.Check {
			// --check: signal the child group and wait for it to finish.
			// The deferred stopChild above handles teardown; return nil to
			// indicate success.
			return nil
		}

		// Open browser if requested.
		if o.Open {
			openBrowser(fmt.Sprintf("http://127.0.0.1:%d", port))
		}

	case <-startTimer.C:
		return fmt.Errorf("app did not become healthy within %s", plan.Timeout)

	case <-ctx.Done():
		return nil
	}

	// Long-run: manifest-command apps (AppType == "") get a file-watch restart
	// loop when reload is enabled. Inferred python/r apps self-reload via their
	// framework flags and do not need the watcher.
	useWatcher := plan.AppType == "" && !o.NoReload
	if useWatcher {
		// changeCh buffers one signal; the loop drains it before re-spawning.
		changeCh := make(chan struct{}, 1)
		watchCtx, watchCancel := context.WithCancel(ctx)
		defer watchCancel()

		excludeDirs := []string{".shinyhub-run", ".venv", ".git", "__pycache__", "node_modules"}
		go func() {
			_ = watchAndRestart(watchCtx, bundleDir, excludeDirs, func() {
				select {
				case changeCh <- struct{}{}:
				default: // already queued; debounce
				}
			})
		}()

		for {
			select {
			case <-ctx.Done():
				return nil

			case exitErr := <-exitCh:
				if exitErr != nil {
					return fmt.Errorf("app exited: %w", exitErr)
				}
				return nil

			case <-changeCh:
				fmt.Fprintln(stdout, "==> file change detected; restarting")
				// Re-resolve the launch plan so manifest [app] command changes
				// are picked up and the new spawn gets a fresh readiness check.
				stopChild(cmd, exitCh, stderr)
				newPlan, resolveErr := deploy.ResolveLaunch(bundleDir, launchOpts)
				if resolveErr != nil {
					return fmt.Errorf("re-resolve launch after change: %w", resolveErr)
				}
				plan = newPlan
				// ReadyPath comes from the re-resolved plan; recompute the probe
				// URL so a changed readiness path is honored on restart.
				restartReadyPath := plan.ReadyPath
				if restartReadyPath == "" {
					restartReadyPath = "/"
				}
				readyURL = fmt.Sprintf("http://127.0.0.1:%d%s", port, restartReadyPath)
				newCmd, newCh, spawnErr := spawnChild()
				if spawnErr != nil {
					return spawnErr
				}
				cmd, exitCh = newCmd, newCh

				// Wait for the restarted process to become ready.
				restartPollCtx, restartPollCancel := context.WithCancel(ctx)
				restartReadyCh := make(chan struct{}, 1)
				go pollReady(restartPollCtx, readyURL, plan.Timeout, restartReadyCh)
				restartTimer := time.NewTimer(plan.Timeout)
				select {
				case <-ctx.Done():
					restartTimer.Stop()
					restartPollCancel()
					return nil
				case restartExitErr := <-exitCh:
					restartTimer.Stop()
					restartPollCancel()
					code := exitCode(restartExitErr)
					return fmt.Errorf("restarted app exited before becoming healthy (exit %d)", code)
				case <-restartReadyCh:
					restartTimer.Stop()
					restartPollCancel()
					fmt.Fprintf(stdout, "==> restarted and healthy\n")
				case <-restartTimer.C:
					restartPollCancel()
					return fmt.Errorf("restarted app did not become healthy within %s", plan.Timeout)
				}
			}
		}
	}

	// Long-run without watcher: block until context is cancelled or process exits.
	select {
	case <-ctx.Done():
		return nil
	case exitErr := <-exitCh:
		if exitErr != nil {
			return fmt.Errorf("app exited: %w", exitErr)
		}
		return nil
	}
}

// ensureDataSymlink creates the <bundle>/data symlink pointing at dataDir, or
// accepts it if it already points at the right target (idempotent restart).
// Any other occupant at the path is rejected to prevent silent corruption.
func ensureDataSymlink(linkPath, dataDir string) error {
	switch info, err := os.Lstat(linkPath); {
	case err == nil:
		if info.Mode()&os.ModeSymlink != 0 {
			if existing, readErr := os.Readlink(linkPath); readErr == nil && existing == dataDir {
				return nil // already correct
			}
		}
		return fmt.Errorf("bundle already contains a 'data' entry (%s); remove it before running", info.Mode())
	case !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("lstat %s: %w", linkPath, err)
	}
	if err := os.Symlink(dataDir, linkPath); err != nil {
		return fmt.Errorf("symlink data: %w", err)
	}
	return nil
}

// removeDataSymlinkIfOwned removes the <bundle>/data symlink only when it
// still points at this run's dataDir. A foreign or replaced entry is left
// alone; a warning is emitted to stderr.
func removeDataSymlinkIfOwned(linkPath, dataDir string, stderr io.Writer) {
	info, err := os.Lstat(linkPath)
	if err != nil {
		return // already gone or unreadable; nothing to do
	}
	if info.Mode()&os.ModeSymlink == 0 {
		// Not a symlink - someone replaced it. Leave it alone.
		fmt.Fprintf(stderr, "warning: <bundle>/data is not a symlink after run; leaving it in place\n")
		return
	}
	existing, err := os.Readlink(linkPath)
	if err != nil || existing != dataDir {
		// Points elsewhere. Leave it alone.
		fmt.Fprintf(stderr, "warning: <bundle>/data symlink points to unexpected target; leaving it in place\n")
		return
	}
	if err := os.Remove(linkPath); err != nil {
		fmt.Fprintf(stderr, "warning: could not remove <bundle>/data symlink: %v\n", err)
	}
}

// pollReady polls readyURL every 200 ms until it gets a non-5xx response,
// ctx is cancelled, or timeout elapses. On success it sends to readyCh.
func pollReady(ctx context.Context, readyURL string, timeout time.Duration, readyCh chan<- struct{}) {
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-deadline.C:
			return
		case <-ticker.C:
			resp, err := client.Get(readyURL) //nolint:noctx
			if err != nil {
				continue
			}
			resp.Body.Close()
			if resp.StatusCode < 500 {
				readyCh <- struct{}{}
				return
			}
		}
	}
}

// stopChild sends SIGTERM to the child's process group, waits up to 5 s for a
// clean exit, then sends SIGKILL. It is safe to call against an already-dead
// process or when exitCh has already been drained (e.g. by the early-exit
// select in Run): a non-blocking pre-drain and a non-blocking final receive
// prevent the caller from hanging.
func stopChild(cmd *exec.Cmd, exitCh <-chan error, stderr io.Writer) {
	if cmd.Process == nil {
		return
	}
	// If the process already exited (exitCh drained by caller), there is
	// nothing left to do but ensure the group is gone.
	select {
	case <-exitCh:
		// Already exited; group is done.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) // harmless on zombie
		return
	default:
	}

	// Signal the entire process group.
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)

	select {
	case <-exitCh:
		return
	case <-time.After(5 * time.Second):
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			slog.Warn("SIGKILL failed", "err", err)
		}
		// Non-blocking drain: if the process was already reaped, don't hang.
		select {
		case <-exitCh:
		default:
		}
	}
}

// exitCode extracts the numeric exit code from a process wait error.
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return 1
}

// openBrowser opens url in the system default browser, best-effort.
func openBrowser(url string) {
	var cmd string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
	case "windows":
		cmd = "cmd"
	default:
		cmd = "xdg-open"
	}
	if runtime.GOOS == "windows" {
		_ = exec.Command(cmd, "/c", "start", url).Start()
	} else {
		_ = exec.Command(cmd, url).Start()
	}
}
