package process

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	gops "github.com/shirou/gopsutil/v4/process"

	"github.com/rvben/shinyhub/internal/sandbox"
)

// nativeChildEnv returns the env slice for a native child process: the
// inherited host env (minus SHINYHUB_*), then the caller-supplied app env,
// then platform-controlled vars (currently SHINYHUB_APP_DATA when set on the
// params). Platform vars are appended last so that a user-supplied
// SHINYHUB_APP_DATA in p.Env cannot shadow the platform value — os/exec
// resolves duplicate keys by last occurrence.
func nativeChildEnv(p StartParams) []string {
	env := append(filteredEnv(), p.Env...)
	// Secret env vars are injected as plaintext alongside Env: native processes
	// share the host trust boundary, so there is no out-of-band channel to
	// deliver them through. Keys are disjoint from Env, so append order is safe.
	env = append(env, p.SecretEnv...)
	if p.AppDataPath != "" {
		env = append(env, "SHINYHUB_APP_DATA="+p.AppDataPath)
	}
	return env
}

// applySharedMounts symlinks each shared mount under p.Dir/data/shared/<slug>.
// Idempotent: existing correct symlinks are left alone; wrong ones return an
// error so we never silently corrupt a bundle. RO is a convention for native
// (the OS does not enforce it on a symlinked dir); enforced for Docker.
func applySharedMounts(p StartParams) error {
	if len(p.SharedMounts) == 0 {
		return nil
	}
	base := filepath.Join(p.Dir, "data", "shared")
	if err := os.MkdirAll(base, 0o750); err != nil {
		return fmt.Errorf("mkdir data/shared: %w", err)
	}
	for _, m := range p.SharedMounts {
		if err := os.MkdirAll(m.HostPath, 0o750); err != nil {
			return fmt.Errorf("mkdir source data %s: %w", m.HostPath, err)
		}
		link := filepath.Join(base, m.SourceSlug)
		switch info, err := os.Lstat(link); {
		case err == nil:
			if info.Mode()&os.ModeSymlink != 0 {
				if existing, _ := os.Readlink(link); existing == m.HostPath {
					continue
				}
			}
			return fmt.Errorf("data/shared/%s exists and is not a matching symlink (%s)", m.SourceSlug, info.Mode())
		case !os.IsNotExist(err):
			return fmt.Errorf("lstat %s: %w", link, err)
		}
		if err := os.Symlink(m.HostPath, link); err != nil {
			return fmt.Errorf("symlink %s: %w", link, err)
		}
	}
	return nil
}

// compile-time checks that NativeRuntime implements Runtime and Snapshotter.
var (
	_ Runtime     = (*NativeRuntime)(nil)
	_ Snapshotter = (*NativeRuntime)(nil)
)

// NativeRuntime runs app processes as direct OS child processes.
type NativeRuntime struct {
	mu    sync.Mutex
	cmds  map[int]*exec.Cmd
	procs map[int]*gops.Process // cached gopsutil handles for CPU delta computation

	// Per-app cgroup state, shared by warm-wake (Snapshotter) and native resource
	// limits. snapshotEnabled is the warm-wake intent; cgroupBaseReady is set once
	// ensureCgroupBase has prepared the delegated cgroup subtree (lazily, on the
	// first Start that needs it - warm-wake on, or a per-app limit set). cgroupBase
	// is the delegated base dir under which per-app cgroups are created; appCgroups
	// maps a running PID to its app cgroup dir so Suspend/Resume/teardown can find
	// it. All are guarded by mu (snapshotEnabled/reclaimMinFraction are set once at
	// startup before any Start, so reads of them need no lock).
	snapshotEnabled    bool
	reclaimMinFraction float64
	cgroupOnce         sync.Once
	cgroupBaseReady    bool
	cgroupBase         string
	appCgroups         map[int]string
	// oomBaseline records each running PID's cgroup oom-kill counter at placement
	// time; oomVerdict records, after exit, whether that counter advanced (a
	// kernel OOM-kill due to the per-app memory limit). Both guarded by mu.
	oomBaseline map[int]uint64
	oomVerdict  map[int]bool
	// limitsMemoryEnforced / limitsCPUEnforced report whether the corresponding
	// cgroup v2 controller is delegated to the service, so a per-app limit is
	// actually enforced (vs silently ignored). Set when ensureCgroupBase succeeds.
	limitsMemoryEnforced bool
	limitsCPUEnforced    bool

	// isolation is the process-isolation dial. When enabled (and the platform
	// supports it), Start/RunOnce launch the app through the re-exec sandbox shim
	// so Landlock confines it. Set once at startup before any Start.
	isolation sandbox.Level
}

// NewNativeRuntime returns a ready-to-use NativeRuntime.
func NewNativeRuntime() *NativeRuntime {
	return &NativeRuntime{
		cmds:        make(map[int]*exec.Cmd),
		procs:       make(map[int]*gops.Process),
		appCgroups:  make(map[int]string),
		oomBaseline: make(map[int]uint64),
		oomVerdict:  make(map[int]bool),
	}
}

// SetSnapshot enables warm-wake (SIGSTOP freeze + per-app cgroup reclaim) and
// sets the reclaim-success threshold. Called once at startup from buildRuntime,
// before any Start. The delegated cgroup base is prepared lazily on the first
// Start that needs it (see ensureCgroupBase); if that preparation fails the
// runtime degrades gracefully and hibernates via Stop as before.
func (r *NativeRuntime) SetSnapshot(enabled bool, reclaimMinFraction float64) {
	r.snapshotEnabled = enabled
	r.reclaimMinFraction = reclaimMinFraction
}

// SetIsolation sets the native process-isolation dial. Called once at startup
// from buildRuntime, before any Start. If isolation is requested on a platform
// with no enforcement backend (non-Linux), it warns and runs without it rather
// than failing to start apps.
func (r *NativeRuntime) SetIsolation(level sandbox.Level) {
	if level.Enabled() && !sandbox.Supported() {
		slog.Warn("native isolation requested but unsupported on this platform; running without it",
			"isolation", string(level))
	}
	r.isolation = level
}

// sandboxWrap wraps the app command in the re-exec sandbox shim when isolation
// is enabled and supported, returning the argv to launch plus extra env (a
// private TMPDIR and the encoded spec). It creates the private temp dir. When
// isolation is off or unsupported it returns the command unchanged.
func (r *NativeRuntime) sandboxWrap(p StartParams) (argv, extraEnv []string, err error) {
	if !r.isolation.Enabled() || !sandbox.Supported() {
		return p.Command, nil, nil
	}
	if len(p.Command) == 0 {
		return nil, nil, fmt.Errorf("command must not be empty")
	}
	self, err := os.Executable()
	if err != nil {
		return nil, nil, fmt.Errorf("resolve self for sandbox: %w", err)
	}
	// Resolve the executable to an absolute path in the parent, matching the
	// unsandboxed path's lookup (a bare name via the server PATH, a path with a
	// separator against the app working dir). Passing the resolved absolute path
	// to the shim keeps command resolution identical: the shim does not re-look-up
	// the name against the app-provided PATH, and a missing executable fails the
	// launch synchronously here.
	bin, err := resolveExecutable(p.Command[0], p.Dir)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve command %q: %w", p.Command[0], err)
	}
	// A private TMPDIR inside the app dir (already writable) gives a well-behaved
	// app an isolated temp area while /tmp stays a fallback.
	tmpDir := filepath.Join(p.Dir, ".sandbox-tmp")
	if err := os.MkdirAll(tmpDir, 0o770); err != nil {
		return nil, nil, fmt.Errorf("sandbox tmp dir: %w", err)
	}
	spec := sandbox.ComputeSpec(r.isolation, p.Dir, p.AppDataPath)
	enc, err := spec.Encode()
	if err != nil {
		return nil, nil, err
	}
	argv = append([]string{self, sandbox.ShimCommand, "--", bin}, p.Command[1:]...)
	extraEnv = sandboxLaunchEnv(p.Dir, tmpDir, enc)
	return argv, extraEnv, nil
}

// resolveExecutable returns the absolute path of name for a child whose working
// directory is workDir, matching os/exec + native launch semantics: a name
// containing a path separator is resolved relative to workDir (or used as is
// when absolute), otherwise it is looked up on the server PATH. Returning an
// absolute path lets the shim exec it directly without a second, app-PATH-
// dependent lookup.
func resolveExecutable(name, workDir string) (string, error) {
	if strings.ContainsRune(name, filepath.Separator) {
		path := name
		if !filepath.IsAbs(path) {
			path = filepath.Join(workDir, path)
		}
		info, err := os.Stat(path)
		if err != nil {
			return "", err
		}
		if info.IsDir() || info.Mode().Perm()&0o111 == 0 {
			return "", fmt.Errorf("%s is not an executable file", path)
		}
		return path, nil
	}
	return exec.LookPath(name)
}

// sandboxLaunchEnv builds the extra environment for a sandboxed launch. It
// redirects tool caches into the app's own writable tree: launchers like
// `uv run` must initialize a cache even with --frozen --no-sync, and under the
// read-only root that write would be denied and the app would fail to start, so
// UV_CACHE_DIR / XDG_CACHE_HOME (and TMPDIR) point at writable app subdirs.
// These are appended after the app's own env, so they take effect under the
// sandbox. Pure, so the cache contract is unit-testable on any platform.
func sandboxLaunchEnv(appDir, tmpDir, encSpec string) []string {
	return []string{
		"TMPDIR=" + tmpDir,
		"UV_CACHE_DIR=" + filepath.Join(appDir, ".uv-cache"),
		"XDG_CACHE_HOME=" + filepath.Join(appDir, ".cache"),
		sandbox.EnvVar + "=" + encSpec,
	}
}

// ensureCgroupBase prepares the delegated cgroup base exactly once, before the
// first child that needs it is forked, so the child is born in base/_supervisor
// (where shinyhub now lives) rather than base, which must stay empty of
// processes to delegate the memory controller. It backs both warm-wake (reclaim)
// and native resource limits. The caller decides when it is needed (warm-wake
// enabled, or a per-app limit set). Any failure leaves cgroupBaseReady false and
// both features degrade gracefully for the process lifetime.
func (r *NativeRuntime) ensureCgroupBase() {
	r.cgroupOnce.Do(func() {
		base, cpuAvailable, err := ensureDelegatedBase()
		if err != nil {
			slog.Warn("native: cgroup base unavailable; warm-wake and resource limits disabled", "err", err)
			return
		}
		r.mu.Lock()
		r.cgroupBase = base
		r.cgroupBaseReady = true
		// Memory is required for the base to come up, so it is enforced on success;
		// cpu is enforced only when its controller is also delegated (Delegate=cpu).
		r.limitsMemoryEnforced = true
		r.limitsCPUEnforced = cpuAvailable
		r.mu.Unlock()
		slog.Info("native: cgroup base ready", "cgroup_base", base, "memory_enforced", true, "cpu_enforced", cpuAvailable)
	})
}

// ResourceEnforcement reports whether per-app memory / cpu limits are actually
// enforced on this host (the controller is delegated to the service), preparing
// the cgroup base on first call. The UI/API surface this so an operator is not
// misled into thinking a limit applies when cgroup delegation is absent.
func (r *NativeRuntime) ResourceEnforcement() (memory, cpu bool) {
	r.ensureCgroupBase()
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.limitsMemoryEnforced, r.limitsCPUEnforced
}

// placeInAppCgroup moves a just-started replica process into its own per-app
// cgroup. The cgroup serves two purposes: isolated memory reclaim on Suspend
// (warm-wake) and resource enforcement (memory.max / cpu.max) when the app sets
// a limit. It is created when the base is ready and either warm-wake is enabled
// or a memory/CPU limit is set. Failures are non-fatal: a setup failure means
// the replica hibernates via Stop instead of warm-freeze and runs uncapped; a
// limit write failure leaves the replica running without that cap (logged). A
// cpu.max write fails when the cpu controller is not delegated (no Delegate=cpu).
func (r *NativeRuntime) placeInAppCgroup(p StartParams, pid int) {
	r.mu.Lock()
	ready := r.cgroupBaseReady
	base := r.cgroupBase
	r.mu.Unlock()
	if !ready || (!r.snapshotEnabled && p.MemoryLimitMB <= 0 && p.CPUQuotaPercent <= 0) {
		return
	}
	dir, err := setupAppCgroup(base, appCgroupName(p.Slug, p.Index), pid)
	if err != nil {
		slog.Warn("native: per-app cgroup setup failed; no warm-wake or resource limits for this replica",
			"slug", p.Slug, "idx", p.Index, "err", err)
		return
	}
	if p.MemoryLimitMB > 0 {
		if err := setCgroupMemoryMax(dir, p.MemoryLimitMB); err != nil {
			slog.Warn("native: memory limit not applied; replica runs uncapped",
				"slug", p.Slug, "idx", p.Index, "limit_mb", p.MemoryLimitMB, "err", err)
		} else {
			slog.Info("native: memory limit applied",
				"slug", p.Slug, "idx", p.Index, "limit_mb", p.MemoryLimitMB)
		}
	}
	if p.CPUQuotaPercent > 0 {
		if err := setCgroupCPUMax(dir, p.CPUQuotaPercent); err != nil {
			slog.Warn("native: cpu limit not applied; replica runs uncapped (needs Delegate=cpu)",
				"slug", p.Slug, "idx", p.Index, "quota_percent", p.CPUQuotaPercent, "err", err)
		} else {
			slog.Info("native: cpu limit applied",
				"slug", p.Slug, "idx", p.Index, "quota_percent", p.CPUQuotaPercent)
		}
	}
	r.mu.Lock()
	r.appCgroups[pid] = dir
	r.oomBaseline[pid] = readAppCgroupOOMCount(dir)
	r.mu.Unlock()
}

// recordOOMVerdict reads the replica's cgroup oom-kill counter at exit and, if it
// advanced past the placement-time baseline, marks the pid OOM-killed. Called from
// Wait before teardown (which removes the cgroup dir). A no-op for untracked pids.
func (r *NativeRuntime) recordOOMVerdict(pid int) {
	r.mu.Lock()
	dir, ok := r.appCgroups[pid]
	base := r.oomBaseline[pid]
	r.mu.Unlock()
	if !ok {
		return
	}
	if cur := readAppCgroupOOMCount(dir); cur > base {
		r.mu.Lock()
		r.oomVerdict[pid] = true
		r.mu.Unlock()
	}
}

// ConsumeOOMKill reports whether the pid's last exit was a kernel OOM-kill and
// clears the per-pid OOM bookkeeping. Implements the manager's oomReporter.
func (r *NativeRuntime) ConsumeOOMKill(pid int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	v := r.oomVerdict[pid]
	delete(r.oomVerdict, pid)
	delete(r.oomBaseline, pid)
	return v
}

// ReadoptWarm re-registers the per-app cgroup of a replica adopted from a prior
// process life (after a server restart), so warm-wake (Suspend/Resume) works for
// it again. The cgroup survives the restart on disk; only this runtime's
// in-memory appCgroups mapping is lost, and Adopt - unlike Start - never rebuilds
// it. ReadoptWarm reconstructs the deterministic app-<slug>-<index> directory
// under the delegated base, confirms the adopted PID is still a member, and
// re-registers the mapping. It is the adoption-time analogue of placeInAppCgroup.
//
// Best-effort: ErrRuntimeNotSnapshotter when warm-wake is off or its base never
// came up (the caller stays silent; the replica hibernates via Stop as today);
// any other error means the cgroup is gone or no longer holds the PID (the
// caller logs and degrades).
func (r *NativeRuntime) ReadoptWarm(slug string, index, pid int) error {
	if !r.snapshotEnabled {
		return ErrRuntimeNotSnapshotter
	}
	r.ensureCgroupBase()
	r.mu.Lock()
	ready := r.cgroupBaseReady
	base := r.cgroupBase
	r.mu.Unlock()
	if !ready {
		return ErrRuntimeNotSnapshotter
	}
	dir := appCgroupDir(base, slug, index)
	ok, err := cgroupContainsPID(dir, pid)
	if err != nil {
		return fmt.Errorf("readopt warm %s/%d: %w", slug, index, err)
	}
	if !ok {
		return fmt.Errorf("readopt warm %s/%d: pid %d not in cgroup %s", slug, index, pid, dir)
	}
	r.mu.Lock()
	r.appCgroups[pid] = dir
	r.mu.Unlock()
	return nil
}

// ReadoptCgroup re-registers an adopted replica's per-app cgroup independent of
// warm-wake, so a limited replica adopted after a server restart can still be
// torn down and have its OOM-kills detected. Best-effort and idempotent: it
// no-ops when the delegated base is unavailable or the deterministic cgroup
// either does not exist or no longer holds the pid (i.e. the replica was
// uncapped). It re-seeds the OOM baseline so only post-readopt kills count.
func (r *NativeRuntime) ReadoptCgroup(slug string, index, pid int) error {
	r.ensureCgroupBase()
	r.mu.Lock()
	ready := r.cgroupBaseReady
	base := r.cgroupBase
	r.mu.Unlock()
	if !ready {
		return nil
	}
	dir := appCgroupDir(base, slug, index)
	ok, err := cgroupContainsPID(dir, pid)
	if err != nil || !ok {
		// No per-app cgroup for this replica (uncapped, no warm-wake), or it is
		// gone. Not an error: the replica simply has nothing to re-adopt.
		return nil
	}
	r.mu.Lock()
	r.appCgroups[pid] = dir
	if _, seeded := r.oomBaseline[pid]; !seeded {
		r.oomBaseline[pid] = readAppCgroupOOMCount(dir)
	}
	r.mu.Unlock()
	return nil
}

// HostPreparesDeps reports true: native runtime executes app processes on the
// host using its PATH, so bundle dependencies must be installed locally before
// Start.
func (r *NativeRuntime) HostPreparesDeps() bool { return true }

// AppBindHost returns "127.0.0.1": native processes share the host network and
// must only be reachable via the in-process proxy.
func (r *NativeRuntime) AppBindHost() string { return "127.0.0.1" }

// HostProvidesAppData reports that the native runtime provisions app data on
// the local host.
func (r *NativeRuntime) HostProvidesAppData() bool { return true }

func (r *NativeRuntime) Start(_ context.Context, p StartParams, logWriter io.Writer) (ReplicaEndpoint, error) {
	if len(p.Command) == 0 {
		return ReplicaEndpoint{}, fmt.Errorf("command must not be empty")
	}
	if err := applySharedMounts(p); err != nil {
		return ReplicaEndpoint{}, err
	}
	// Prepare the delegated cgroup base before forking (once) so the child is born
	// in base/_supervisor and can be moved into its own app cgroup below. Needed
	// when warm-wake is enabled or the app sets a memory/CPU limit.
	if r.snapshotEnabled || p.MemoryLimitMB > 0 || p.CPUQuotaPercent > 0 {
		r.ensureCgroupBase()
	}
	argv, extraEnv, err := r.sandboxWrap(p)
	if err != nil {
		return ReplicaEndpoint{}, err
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = p.Dir
	cmd.Env = append(nativeChildEnv(p), extraEnv...)
	cmd.Stdout = logWriter
	cmd.Stderr = logWriter
	// Place the child in its own process group so signals can be sent to the
	// entire group, avoiding orphaned sub-processes.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return ReplicaEndpoint{}, fmt.Errorf("start process: %w", err)
	}
	pid := cmd.Process.Pid
	r.mu.Lock()
	r.cmds[pid] = cmd
	r.mu.Unlock()
	// Move the replica into its own cgroup for isolated warm-wake reclaim and to
	// enforce memory.max / cpu.max when a limit is set. No-op when neither
	// warm-wake nor a limit applies, or when cgroup v2 delegation is unavailable;
	// failures degrade gracefully (stop-hibernate / uncapped).
	r.placeInAppCgroup(p, pid)
	return ReplicaEndpoint{
		URL:      fmt.Sprintf("http://127.0.0.1:%d", p.Port),
		Provider: "native",
		WorkerID: strconv.Itoa(pid),
		Handle:   RunHandle{PID: pid},
	}, nil
}

func (r *NativeRuntime) Signal(handle RunHandle, sig syscall.Signal) error {
	// ESRCH means the process group is already gone — treat as a no-op so
	// Stop remains idempotent when the process exits before SIGTERM arrives.
	if err := syscall.Kill(-handle.PID, sig); err != nil && err != syscall.ESRCH {
		return fmt.Errorf("signal %v to pgid %d: %w", sig, handle.PID, err)
	}
	return nil
}

func (r *NativeRuntime) Wait(ctx context.Context, handle RunHandle) error {
	r.mu.Lock()
	cmd, ok := r.cmds[handle.PID]
	if ok {
		delete(r.cmds, handle.PID)
	}
	r.mu.Unlock()

	if !ok {
		// Adopted process: this runtime instance never started it, so we have
		// no *exec.Cmd to wait on. Poll the kernel for liveness; signal 0 is a
		// permission/existence probe that returns ESRCH once the PID is gone.
		err := waitForPIDExit(ctx, handle.PID)
		// Read the OOM counter before teardown removes the cgroup dir.
		r.recordOOMVerdict(handle.PID)
		r.teardownAppCgroupFor(handle.PID)
		return err
	}

	err := cmd.Wait()

	r.mu.Lock()
	delete(r.procs, handle.PID)
	r.mu.Unlock()
	// The process has exited, so its cgroup is now empty and can be removed.
	// Read the OOM counter first: teardown rmdirs the cgroup.
	r.recordOOMVerdict(handle.PID)
	r.teardownAppCgroupFor(handle.PID)
	return err
}

// teardownAppCgroupFor removes a replica's per-app cgroup once its process has
// exited and forgets the mapping. A no-op for a pid that was never tracked
// (warm-wake off, setup failed, or an adopted process from a prior instance).
func (r *NativeRuntime) teardownAppCgroupFor(pid int) {
	r.mu.Lock()
	dir, ok := r.appCgroups[pid]
	if ok {
		delete(r.appCgroups, pid)
	}
	r.mu.Unlock()
	if !ok {
		return
	}
	if err := teardownAppCgroup(dir); err != nil {
		slog.Warn("native: app cgroup teardown failed", "pid", pid, "dir", dir, "err", err)
	}
}

// adoptedPollInterval is how often we re-check a PID we don't own. Two seconds
// keeps watcher restart latency tight without measurable CPU overhead.
const adoptedPollInterval = 2 * time.Second

func waitForPIDExit(ctx context.Context, pid int) error {
	ticker := time.NewTicker(adoptedPollInterval)
	defer ticker.Stop()
	for {
		err := syscall.Kill(pid, 0)
		if err == syscall.ESRCH {
			return nil
		}
		// EPERM means the PID still exists but is owned by another user; any
		// other error (including nil) means we should keep polling.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (r *NativeRuntime) Stats(_ context.Context, handle RunHandle) (float64, uint64, error) {
	r.mu.Lock()
	p, ok := r.procs[handle.PID]
	if !ok {
		var err error
		p, err = gops.NewProcess(int32(handle.PID))
		if err != nil {
			r.mu.Unlock()
			return 0, 0, fmt.Errorf("process %d: %w", handle.PID, err)
		}
		r.procs[handle.PID] = p
	}
	r.mu.Unlock()

	cpu, err := p.CPUPercent()
	if err != nil {
		r.mu.Lock()
		delete(r.procs, handle.PID)
		r.mu.Unlock()
		return 0, 0, fmt.Errorf("cpu percent: %w", err)
	}
	mem, err := p.MemoryInfo()
	if err != nil {
		r.mu.Lock()
		delete(r.procs, handle.PID)
		r.mu.Unlock()
		return 0, 0, fmt.Errorf("memory info: %w", err)
	}
	return cpu, mem.RSS, nil
}

// jobHasLimit reports whether a one-shot job run carries a per-replica resource
// limit that the native runtime should enforce via a dedicated cgroup.
func jobHasLimit(p StartParams) bool {
	return p.JobRunID != 0 && (p.MemoryLimitMB > 0 || p.CPUQuotaPercent > 0)
}

// placeJobInCgroup moves a one-shot job process into its OWN cgroup
// (job-<slug>-<runID>, never a live replica's app-<slug>-<index>) and applies the
// app's memory/CPU limits, returning a teardown closure for the caller to defer.
// Best-effort: returns a no-op when no limit is set or the delegated base is
// unavailable (the job then runs uncapped, exactly as before this existed).
func (r *NativeRuntime) placeJobInCgroup(p StartParams, pid int) func() {
	noop := func() {}
	if !jobHasLimit(p) {
		return noop
	}
	r.mu.Lock()
	ready := r.cgroupBaseReady
	base := r.cgroupBase
	r.mu.Unlock()
	if !ready {
		return noop
	}
	dir, err := setupAppCgroup(base, jobCgroupName(p.Slug, p.JobRunID), pid)
	if err != nil {
		slog.Warn("native: per-job cgroup setup failed; job runs uncapped",
			"slug", p.Slug, "run_id", p.JobRunID, "err", err)
		return noop
	}
	if p.MemoryLimitMB > 0 {
		if err := setCgroupMemoryMax(dir, p.MemoryLimitMB); err != nil {
			slog.Warn("native: job memory limit not applied; job runs uncapped",
				"slug", p.Slug, "run_id", p.JobRunID, "limit_mb", p.MemoryLimitMB, "err", err)
		}
	}
	if p.CPUQuotaPercent > 0 {
		if err := setCgroupCPUMax(dir, p.CPUQuotaPercent); err != nil {
			slog.Warn("native: job cpu limit not applied; job runs uncapped (needs Delegate=cpu)",
				"slug", p.Slug, "run_id", p.JobRunID, "quota_percent", p.CPUQuotaPercent, "err", err)
		}
	}
	return func() {
		// A one-shot job may background children that outlive its main process and
		// stay in this cgroup. Reap them first so rmdir does not EBUSY-leak the
		// cgroup (and the orphans). For a job whose process group is intact this is
		// belt-and-suspenders; for children that escaped the group via setsid the
		// cgroup membership is the authoritative handle.
		killAppCgroupProcs(dir)
		if err := teardownAppCgroup(dir); err != nil {
			slog.Warn("native: job cgroup teardown failed", "slug", p.Slug, "run_id", p.JobRunID, "dir", dir, "err", err)
		}
	}
}

// RunOnce blocks until the process exits or ctx is cancelled. On ctx cancel,
// the process group receives SIGTERM, then SIGKILL after a 10-second grace.
func (r *NativeRuntime) RunOnce(ctx context.Context, p StartParams, logWriter io.Writer) (ExitInfo, error) {
	if len(p.Command) == 0 {
		return ExitInfo{}, fmt.Errorf("command must not be empty")
	}
	if err := applySharedMounts(p); err != nil {
		return ExitInfo{}, err
	}
	// Prepare the delegated cgroup base before forking (once) when this job needs
	// a limit, so the child is born in base/_supervisor and can be moved into its
	// own job cgroup below.
	if jobHasLimit(p) {
		r.ensureCgroupBase()
	}
	// One-shot jobs (e.g. scheduled runs) are app code too, so they get the same
	// isolation as the long-running Start path.
	argv, extraEnv, err := r.sandboxWrap(p)
	if err != nil {
		return ExitInfo{}, err
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = p.Dir
	cmd.Env = append(nativeChildEnv(p), extraEnv...)
	cmd.Stdout = logWriter
	cmd.Stderr = logWriter
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return ExitInfo{}, fmt.Errorf("start one-shot: %w", err)
	}
	// Cap the job in its own cgroup; teardown on every exit path (success,
	// error, ctx timeout/cancel) via defer.
	teardownJob := r.placeJobInCgroup(p, cmd.Process.Pid)
	defer teardownJob()

	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()

	select {
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-waitDone:
		case <-time.After(10 * time.Second):
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-waitDone
		}
		return ExitInfo{Code: -1, Signaled: true}, nil
	case err := <-waitDone:
		if err == nil {
			return ExitInfo{Code: 0}, nil
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			ws, _ := exitErr.Sys().(syscall.WaitStatus)
			if ws.Signaled() {
				return ExitInfo{Code: -1, Signaled: true}, nil
			}
			return ExitInfo{Code: exitErr.ExitCode()}, nil
		}
		return ExitInfo{}, fmt.Errorf("wait one-shot: %w", err)
	}
}

// Suspend freezes a replica's process group (SIGSTOP) and reclaims its resident
// memory to swap via its per-app cgroup's memory.reclaim, returning freed=true
// only when the reclaimed fraction meets the configured threshold. On any
// non-(true,nil) result it sends SIGCONT so the process is left normally
// stoppable. When warm-wake is disabled or its cgroup base never came up it
// reports ErrRuntimeNotSnapshotter so the watcher hibernates via Stop; a replica
// that was started without a per-app cgroup reports (false, nil) for the same
// fallback without flagging the whole runtime as non-snapshotting.
func (r *NativeRuntime) Suspend(_ context.Context, handle RunHandle) (bool, error) {
	if !r.snapshotEnabled {
		return false, ErrRuntimeNotSnapshotter
	}
	r.mu.Lock()
	ready := r.cgroupBaseReady
	dir, tracked := r.appCgroups[handle.PID]
	r.mu.Unlock()
	if !ready {
		return false, ErrRuntimeNotSnapshotter
	}
	if !tracked {
		// This replica has no per-app cgroup (setup failed, or it predates
		// delegation): it cannot be warm-frozen, so cold-stop it.
		return false, nil
	}

	pid := handle.PID
	// Freeze the whole process group so its memory is stable across the reclaim.
	if err := syscall.Kill(-pid, syscall.SIGSTOP); err != nil {
		if err == syscall.ESRCH {
			return false, fmt.Errorf("suspend: process group %d is gone", pid)
		}
		return false, fmt.Errorf("sigstop pgid %d: %w", pid, err)
	}
	cont := func(reason string) {
		if cerr := syscall.Kill(-pid, syscall.SIGCONT); cerr != nil && cerr != syscall.ESRCH {
			slog.Warn("native: sigcont after "+reason, "pid", pid, "err", cerr)
		}
	}
	preMem, err := appCgroupCurrentMemory(dir)
	if err != nil {
		cont("failed pre-measure")
		return false, fmt.Errorf("measure memory before reclaim: %w", err)
	}
	if err := reclaimAppCgroup(dir, preMem); err != nil {
		cont("failed reclaim")
		return false, fmt.Errorf("reclaim: %w", err)
	}
	postMem, err := appCgroupCurrentMemory(dir)
	if err != nil {
		cont("failed post-measure")
		return false, fmt.Errorf("measure memory after reclaim: %w", err)
	}
	if !reclaimFreed(preMem, postMem, r.reclaimMinFraction) {
		// Not enough RAM freed (e.g. no swap): thaw and let the caller Stop, which
		// frees all the RAM. Never leave a frozen-but-resident idle app.
		cont("insufficient reclaim")
		return false, nil
	}
	// Success: leave the process group frozen with its memory reclaimed.
	return true, nil
}

// Resume thaws a previously suspended replica (SIGCONT). It is idempotent:
// SIGCONT on an already-running process group is a no-op. The PID and port are
// preserved, so the route URL is unchanged and the returned endpoint carries an
// empty URL for the Manager to preserve the known route. A vanished process
// group (ESRCH) is a genuine error so the caller cold-boots; its now-empty cgroup
// is reclaimed by the replica's Wait.
func (r *NativeRuntime) Resume(_ context.Context, handle RunHandle) (ReplicaEndpoint, error) {
	pid := handle.PID
	if err := syscall.Kill(-pid, syscall.SIGCONT); err != nil {
		if err == syscall.ESRCH {
			return ReplicaEndpoint{}, fmt.Errorf("resume: process group %d is gone", pid)
		}
		return ReplicaEndpoint{}, fmt.Errorf("sigcont pgid %d: %w", pid, err)
	}
	return ReplicaEndpoint{Provider: "native", WorkerID: strconv.Itoa(pid), Handle: handle}, nil
}
