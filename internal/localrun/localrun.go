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
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/rvben/shinyhub/internal/bundle"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/process"
	slugpkg "github.com/rvben/shinyhub/internal/slug"
)

// Options configures a local foreground run.
type Options struct {
	// BundleDir is the app bundle directory to run (required).
	BundleDir string
	// Slug is a human label for log output. Defaults to the basename of BundleDir.
	Slug string
	// DataDir is the host path used as SHINYHUB_APP_DATA. When empty, it lives
	// in ShinyHub's per-app local state directory, outside the source checkout.
	DataDir string
	// StateDir is the ShinyHub-owned workspace and state directory. Empty uses
	// the OS user cache, keyed by the absolute source path.
	StateDir string
	// ManifestPath identifies a fleet-aware run. When set, the default workspace
	// is keyed by the canonical manifest path plus Slug rather than BundleDir.
	ManifestPath string
	// BundleInputs are manifest-relative files composed into the generated
	// workspace. They are resolved and snapshotted again for every reload.
	BundleInputs []bundle.FileInputSpec
	// Port is the local TCP port to bind. When 0, a free port is allocated.
	Port int
	// Env is additional environment in KEY=VALUE form, layered above the
	// sanitized host env but below the platform-controlled PORT and SHINYHUB_APP_DATA.
	Env []string
	// NoSync skips the explicit dep-prep steps (uv sync / renv restore).
	NoSync bool
	// NoReload disables ShinyHub's staged file-watch reload loop.
	NoReload bool
	// Fresh discards the generated dependency mirror before starting while
	// preserving the app's durable data directory.
	Fresh bool
	// Open opens the serving URL in the default browser after readiness.
	Open bool
	// Check runs in preflight mode: boot, verify healthy, stop, exit 0/1.
	Check bool
}

// ValidationError marks user-correctable preflight failures so the CLI emits
// a validation error envelope instead of misclassifying them as internals.
type ValidationError struct{ Err error }

func (e *ValidationError) Error() string { return e.Err.Error() }
func (e *ValidationError) Unwrap() error { return e.Err }

func validationErrorf(format string, args ...any) error {
	return &ValidationError{Err: fmt.Errorf(format, args...)}
}

// reservedEnvKeys are platform-authoritative in local and deployed apps.
var reservedEnvKeys = []string{"PORT", "SHINYHUB_APP_DATA", "SHINYHUB_APP_SLUG"}

func validateUserEnv(env []string) ([]string, error) {
	reserved := make(map[string]struct{}, len(reservedEnvKeys))
	for _, k := range reservedEnvKeys {
		reserved[k] = struct{}{}
	}
	out := append([]string(nil), env...)
	for _, kv := range env {
		key, _, ok := strings.Cut(kv, "=")
		if !ok || key == "" {
			return nil, fmt.Errorf("invalid environment assignment %q: expected KEY=VALUE", kv)
		}
		if _, blocked := reserved[key]; blocked {
			return nil, fmt.Errorf("environment key %s is managed by ShinyHub and cannot be overridden", key)
		}
	}
	return out, nil
}

type childProcess struct {
	cmd    *exec.Cmd
	exitCh <-chan error
	port   int
	plan   *deploy.LaunchPlan
}

// synchronizedWriterPair keeps runner status and child-process output from
// writing the caller's stdout/stderr concurrently. A shared lock covers both
// streams because callers may intentionally route them to the same writer.
func synchronizedWriterPair(stdout, stderr io.Writer) (io.Writer, io.Writer) {
	mu := &sync.Mutex{}
	return &synchronizedWriter{mu: mu, w: stdout}, &synchronizedWriter{mu: mu, w: stderr}
}

type synchronizedWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (w *synchronizedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.w.Write(p)
}

// Run boots the app bundle and blocks until the context is cancelled (or, in
// --check mode, until the first healthy poll or crash). It streams all app
// output to stdout/stderr and returns a non-nil error on any failure.
func Run(ctx context.Context, o Options, stdout, stderr io.Writer) error {
	stdout, stderr = synchronizedWriterPair(stdout, stderr)
	sourceDir, err := filepath.Abs(o.BundleDir)
	if err != nil {
		return validationErrorf("resolve bundle dir: %v", err)
	}
	info, err := os.Stat(sourceDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return validationErrorf("bundle directory does not exist: %s", sourceDir)
		}
		return fmt.Errorf("inspect bundle directory: %w", err)
	}
	if !info.IsDir() {
		return validationErrorf("bundle path is not a directory: %s", sourceDir)
	}
	if resolved, resolveErr := filepath.EvalSymlinks(sourceDir); resolveErr == nil {
		sourceDir = resolved
	} else {
		return validationErrorf("resolve bundle directory symlinks: %v", resolveErr)
	}
	if _, err := os.Lstat(filepath.Join(sourceDir, "data")); err == nil {
		return validationErrorf("bundle contains reserved 'data' entry; move seed data outside the bundle and use --data-dir")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect reserved data path: %w", err)
	}
	slug := o.Slug
	if slug == "" {
		slug = normalizeLocalSlug(filepath.Base(sourceDir))
	}
	if !slugpkg.Valid(slug) {
		return validationErrorf("invalid slug %q: must be %s", slug, slugpkg.HumanRule)
	}
	userEnv, err := validateUserEnv(o.Env)
	if err != nil {
		return &ValidationError{Err: err}
	}
	if o.Port < 0 || o.Port > 65535 {
		return validationErrorf("port must be between 0 and 65535, got %d", o.Port)
	}
	manifestPath := ""
	manifestRoot := ""
	if o.ManifestPath != "" {
		invocationPath, absErr := filepath.Abs(o.ManifestPath)
		if absErr != nil {
			return validationErrorf("resolve fleet manifest: %v", absErr)
		}
		manifestRoot, err = filepath.EvalSymlinks(filepath.Dir(invocationPath))
		if err != nil {
			return validationErrorf("resolve fleet manifest root: %v", err)
		}
		manifestPath, err = filepath.EvalSymlinks(invocationPath)
		if err != nil {
			return validationErrorf("resolve fleet manifest: %v", err)
		}
	}
	inputSnapshots, err := resolveInputSnapshots(manifestRoot, sourceDir, o.BundleInputs)
	if err != nil {
		return &ValidationError{Err: fmt.Errorf("resolve bundle inputs: %w", err)}
	}
	// Read-only source preflight. This deliberately runs before workspace or
	// data creation, so a typo or invalid manifest leaves no filesystem state.
	if len(inputSnapshots) == 0 {
		if _, err := deploy.ResolveLaunch(sourceDir, deploy.LaunchOptions{
			Port: 1, BindHost: "127.0.0.1", Reload: false,
		}); err != nil {
			return &ValidationError{Err: fmt.Errorf("resolve launch: %w", err)}
		}
	}

	workspaceIdentity := ""
	if manifestPath != "" {
		workspaceIdentity = manifestPath + "\x00" + slug
	}
	w, err := workspaceForIdentity(sourceDir, o.StateDir, o.DataDir, workspaceIdentity)
	if err != nil {
		return err
	}
	if err := w.validateLocations(sourceDir, manifestRoot); err != nil {
		return &ValidationError{Err: err}
	}
	releaseWorkspace, err := w.acquireLock()
	if err != nil {
		return &ValidationError{Err: err}
	}
	defer releaseWorkspace()
	if o.Fresh {
		if err := w.resetAllBundles(); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "==> cleared generated workspace (app data preserved)")
	}
	depsChanged, err := w.syncSourceWithInputs(sourceDir, inputSnapshots)
	if err != nil {
		return err
	}

	lp, err := newLocalProxy(o.Port, slug)
	if err != nil {
		return err
	}
	defer lp.close()
	proxyErrCh := make(chan error, 1)

	fmt.Fprintf(stdout, "==> source: %s\n", sourceDir)
	for _, input := range inputSnapshots {
		fmt.Fprintf(stdout, "==> bundle input: %s -> %s\n", input.From, input.To)
	}
	fmt.Fprintf(stdout, "==> workspace: %s\n", w.BundleDir)
	fmt.Fprintf(stdout, "==> data: %s\n", w.DataDir)
	if len(userEnv) > 0 {
		fmt.Fprintf(stdout, "==> environment: %s\n", strings.Join(environmentKeys(userEnv), ", "))
	}

	changeCh := make(chan struct{}, 1)
	if !o.NoReload {
		watchReady := make(chan struct{})
		inputWatchPaths := bundleInputWatchPaths(manifestRoot, o.BundleInputs)
		go func() {
			_ = watchAndRestartSourcesReady(ctx, sourceDir,
				[]string{".shinyhub-run", ".venv", ".git", "__pycache__", "node_modules", ".renv", ".Rproj.user"},
				inputWatchPaths, watchReady, func() {
					select {
					case changeCh <- struct{}{}:
					default:
					}
				})
		}()
		select {
		case <-ctx.Done():
			return nil
		case <-watchReady:
		}
	}

	current, err := startCandidate(ctx, w, slug, userEnv, o.NoSync, depsChanged, stdout, stderr)
	if err != nil {
		return err
	}
	defer func() { stopChild(current.cmd, current.exitCh, stderr) }()
	if err := waitUntilReady(ctx, current, nil); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	if err := lp.routeTo(current.port); err != nil {
		return err
	}
	lp.serve(proxyErrCh)
	publicReadyURL := joinReadyURL(lp.URL(), current.plan.ReadyPath)
	if err := pollReady(ctx, publicReadyURL, current.plan.Timeout, current.plan.ReadyStatus); err != nil {
		return fmt.Errorf("local proxy readiness: %w", err)
	}
	fmt.Fprintf(stdout, "serving on %s\n", lp.URL())
	if o.Check {
		return nil
	}
	if o.Open {
		openBrowser(lp.URL())
	}

	if o.NoReload {
		return waitForExit(ctx, current, proxyErrCh)
	}
	currentWorkspace := w
	stagingWorkspace := w.alternate()

	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-proxyErrCh:
			return err
		case exitErr := <-current.exitCh:
			// exec.CommandContext kills the child when ctx is cancelled. The
			// process exit and ctx.Done become ready concurrently, so select may
			// observe the killed child first. A requested shutdown is still clean.
			if ctx.Err() != nil {
				return nil
			}
			if exitErr != nil {
				return fmt.Errorf("app exited: %w", exitErr)
			}
			return errors.New("app exited unexpectedly")
		case <-changeCh:
			fmt.Fprintln(stdout, "==> change detected; staging reload")
			stagedInputs, resolveErr := resolveInputSnapshots(manifestRoot, sourceDir, o.BundleInputs)
			if resolveErr != nil {
				fmt.Fprintf(stderr, "reload failed; keeping the current app: resolve bundle inputs: %v\n", resolveErr)
				continue
			}
			depsChanged, syncErr := stagingWorkspace.syncSourceWithInputs(sourceDir, stagedInputs)
			if syncErr != nil {
				fmt.Fprintf(stderr, "reload failed; keeping the current app: %v\n", syncErr)
				continue
			}
			candidate, startErr := startCandidate(ctx, stagingWorkspace, slug, userEnv, o.NoSync, depsChanged, stdout, stderr)
			if startErr != nil {
				fmt.Fprintf(stderr, "reload failed; keeping the current app: %v\n", startErr)
				continue
			}

			readyErr := waitUntilReady(ctx, candidate, changeCh)
			if errors.Is(readyErr, errReloadSuperseded) {
				stopChild(candidate.cmd, candidate.exitCh, stderr)
				fmt.Fprintln(stdout, "==> newer change detected; replacing staged reload")
				select {
				case changeCh <- struct{}{}:
				default:
				}
				continue
			}
			if readyErr != nil {
				stopChild(candidate.cmd, candidate.exitCh, stderr)
				fmt.Fprintf(stderr, "reload failed; keeping the current app: %v\n", readyErr)
				continue
			}
			if err := lp.routeTo(candidate.port); err != nil {
				stopChild(candidate.cmd, candidate.exitCh, stderr)
				fmt.Fprintf(stderr, "reload failed; keeping the current app: %v\n", err)
				continue
			}
			if err := pollReady(ctx, joinReadyURL(lp.URL(), candidate.plan.ReadyPath), 5*time.Second, candidate.plan.ReadyStatus); err != nil {
				_ = lp.routeTo(current.port)
				stopChild(candidate.cmd, candidate.exitCh, stderr)
				fmt.Fprintf(stderr, "reload failed through local proxy; keeping the current app: %v\n", err)
				continue
			}
			old := current
			current = candidate
			currentWorkspace, stagingWorkspace = stagingWorkspace, currentWorkspace
			stopChild(old.cmd, old.exitCh, stderr)
			fmt.Fprintln(stdout, "==> reloaded and healthy")
		}
	}
}

func bundleInputWatchPaths(manifestRoot string, specs []bundle.FileInputSpec) []string {
	if manifestRoot == "" || len(specs) == 0 {
		return nil
	}
	paths := make([]string, 0, len(specs))
	for _, spec := range specs {
		paths = append(paths, filepath.Join(manifestRoot, filepath.FromSlash(spec.From)))
	}
	return paths
}

func resolveInputSnapshots(manifestRoot, sourceDir string, specs []bundle.FileInputSpec) ([]bundle.FileInputSnapshot, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	if manifestRoot == "" {
		return nil, fmt.Errorf("bundle inputs require a fleet manifest identity")
	}
	resolved, err := bundle.ResolveFileInputs(manifestRoot, sourceDir, specs)
	if err != nil {
		return nil, err
	}
	return bundle.SnapshotFileInputs(resolved)
}

var errReloadSuperseded = errors.New("reload superseded by a newer change")

func startCandidate(ctx context.Context, w *workspace, slug string, userEnv []string, noSync, depsChanged bool, stdout, stderr io.Writer) (*childProcess, error) {
	port := deploy.AllocatePort()
	baseOpts := deploy.LaunchOptions{
		Port: port, Workers: 1, BindHost: "127.0.0.1", Reload: false,
		CommandHostDeps: !noSync, AutoInstrumentDefault: false,
		HonorManifestTracing: false, AppEnv: userEnv,
	}
	plan, err := deploy.ResolveLaunch(w.BundleDir, baseOpts)
	if err != nil {
		return nil, fmt.Errorf("resolve launch: %w", err)
	}
	needsPrep := !noSync && (depsChanged || !deploy.HostEnvironmentReady(w.BundleDir, plan.AppType))
	if needsPrep {
		if err := w.markDependenciesDirty(); err != nil {
			return nil, err
		}
		prepOpts := baseOpts
		prepOpts.PrepHostDeps = true
		plan, err = deploy.ResolveLaunch(w.BundleDir, prepOpts)
		if err != nil {
			return nil, fmt.Errorf("resolve dependency preparation: %w", err)
		}
		for _, step := range plan.DepPrep {
			fmt.Fprintf(stdout, "==> %s\n", step.Label)
			if err := step.Run(ctx, w.BundleDir); err != nil {
				return nil, fmt.Errorf("dependency preparation (%s): %w", step.Label, err)
			}
		}
		// Preparation may synthesize a project, changing the canonical command.
		plan, err = deploy.ResolveLaunch(w.BundleDir, baseOpts)
		if err != nil {
			return nil, fmt.Errorf("resolve prepared launch: %w", err)
		}
		if err := w.markDependenciesReady(); err != nil {
			return nil, err
		}
	}

	appType := plan.AppType
	if appType == "" {
		appType = "manifest command"
	}
	readyContract := "2xx or 3xx"
	if plan.ReadyStatus != 0 {
		readyContract = fmt.Sprintf("HTTP %d", plan.ReadyStatus)
	}
	fmt.Fprintf(stdout, "==> starting %s; readiness GET %s expects %s\n", appType, plan.ReadyPath, readyContract)

	childEnv := append(process.SanitizedEnv(), userEnv...)
	childEnv = append(childEnv, plan.Env...)
	childEnv = append(childEnv,
		"SHINYHUB_APP_DATA="+w.DataDir,
		"SHINYHUB_APP_SLUG="+slug,
	)
	c := exec.CommandContext(ctx, plan.Command[0], plan.Command[1:]...) //nolint:gosec
	c.Dir = w.BundleDir
	c.Env = childEnv
	c.Stdout = stdout
	c.Stderr = stderr
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := c.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", slug, err)
	}
	return &childProcess{cmd: c, exitCh: watchExit(c), port: port, plan: plan}, nil
}

// watchExit reaps c in the background and reports its exit over the returned
// channel. The buffered send hands the exit error to whichever select receives
// first; the close then makes "the leader has been reaped" observable to every
// later receiver, including a stopChild whose channel another select already
// drained. Consulting cmd.ProcessState for that instead would race with Wait,
// which owns that field until it returns.
func watchExit(c *exec.Cmd) <-chan error {
	exitCh := make(chan error, 1)
	go func() { exitCh <- c.Wait(); close(exitCh) }()
	return exitCh
}

func waitUntilReady(ctx context.Context, child *childProcess, changes <-chan struct{}) error {
	readyURL := joinReadyURL(fmt.Sprintf("http://127.0.0.1:%d/", child.port), child.plan.ReadyPath)
	readyCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	readyCh := make(chan error, 1)
	go func() { readyCh <- pollReady(readyCtx, readyURL, child.plan.Timeout, child.plan.ReadyStatus) }()
	select {
	case exitErr := <-child.exitCh:
		return fmt.Errorf("app exited during startup (exit %d)", exitCode(exitErr))
	case err := <-readyCh:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-changes:
		return errReloadSuperseded
	}
}

func waitForExit(ctx context.Context, child *childProcess, proxyErrCh <-chan error) error {
	select {
	case <-ctx.Done():
		return nil
	case err := <-proxyErrCh:
		return err
	case exitErr := <-child.exitCh:
		if ctx.Err() != nil {
			return nil
		}
		if exitErr != nil {
			return fmt.Errorf("app exited: %w", exitErr)
		}
		return errors.New("app exited unexpectedly")
	}
}

func normalizeLocalSlug(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	lastHyphen := false
	for _, r := range name {
		valid := r >= 'a' && r <= 'z' || r >= '0' && r <= '9'
		if valid {
			b.WriteRune(r)
			lastHyphen = false
		} else if !lastHyphen && b.Len() > 0 {
			b.WriteByte('-')
			lastHyphen = true
		}
		if b.Len() >= slugpkg.MaxLen {
			break
		}
	}
	return strings.Trim(b.String(), "-")
}

func environmentKeys(env []string) []string {
	keys := make([]string, 0, len(env))
	for _, assignment := range env {
		key, _, _ := strings.Cut(assignment, "=")
		keys = append(keys, key)
	}
	return keys
}

// pollReady polls readyURL until the configured readiness contract succeeds.
// ReadyStatus 0 accepts 2xx/3xx; a positive value requires that exact status.
func pollReady(ctx context.Context, readyURL string, timeout time.Duration, readyStatus int) error {
	client := &http.Client{
		Timeout: 2 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	lastStatus := 0
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			if lastStatus != 0 {
				return fmt.Errorf("app did not satisfy readiness at %s within %s (last status %d)", readyURL, timeout, lastStatus)
			}
			return fmt.Errorf("app did not satisfy readiness at %s within %s", readyURL, timeout)
		case <-ticker.C:
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, readyURL, nil)
			if err != nil {
				return fmt.Errorf("build readiness request: %w", err)
			}
			resp, err := client.Do(req)
			if err != nil {
				continue
			}
			resp.Body.Close()
			lastStatus = resp.StatusCode
			if readyStatus > 0 && resp.StatusCode == readyStatus || readyStatus == 0 && resp.StatusCode >= 200 && resp.StatusCode < 400 {
				return nil
			}
		}
	}
}

func joinReadyURL(base, readyPath string) string {
	if readyPath == "" || readyPath == "/" {
		return strings.TrimSuffix(base, "/") + "/"
	}
	return strings.TrimSuffix(base, "/") + "/" + strings.TrimPrefix(readyPath, "/")
}

// stopChild sends SIGTERM to the child's process group, waits up to 5 s for a
// clean exit, then sends SIGKILL. It is safe to call against an already-dead
// process or when exitCh has already been drained (e.g. by the early-exit
// select in Run): the channel is closed once Wait returns, so every receive
// below completes rather than hanging, whether the leader exited normally or
// died from a signal.
func stopChild(cmd *exec.Cmd, exitCh <-chan error, stderr io.Writer) {
	if cmd.Process == nil {
		return
	}
	// If the leader has already been reaped, there is nothing left to do but
	// ensure any surviving grandchildren in its group are gone.
	select {
	case <-exitCh:
		// Already exited; group is done.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) // harmless on zombie
		return
	default:
	}

	// Signal the entire process group.
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM); errors.Is(err, syscall.ESRCH) {
		return
	}

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
