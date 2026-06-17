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
	"sync"
	"syscall"
	"time"

	gops "github.com/shirou/gopsutil/v4/process"
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

	// Warm-wake (Snapshotter) state. snapshotEnabled is the configured intent;
	// snapshotReady is set once ensureDelegatedBase has prepared the cgroup
	// subtree (lazily, on the first snapshot-enabled Start). cgroupBase is the
	// delegated base dir under which per-app cgroups are created; appCgroups maps
	// a running PID to its app cgroup dir so Suspend/Resume/teardown can find it.
	// All four are guarded by mu (snapshotEnabled/reclaimMinFraction are set once
	// at startup before any Start, so reads of them need no lock).
	snapshotEnabled    bool
	reclaimMinFraction float64
	snapshotOnce       sync.Once
	snapshotReady      bool
	cgroupBase         string
	appCgroups         map[int]string
}

// NewNativeRuntime returns a ready-to-use NativeRuntime.
func NewNativeRuntime() *NativeRuntime {
	return &NativeRuntime{
		cmds:       make(map[int]*exec.Cmd),
		procs:      make(map[int]*gops.Process),
		appCgroups: make(map[int]string),
	}
}

// SetSnapshot enables warm-wake (SIGSTOP freeze + per-app cgroup reclaim) and
// sets the reclaim-success threshold. Called once at startup from buildRuntime,
// before any Start. The delegated cgroup base is prepared lazily on the first
// snapshot-enabled Start (see ensureSnapshotBase); if that preparation fails the
// runtime degrades gracefully and hibernates via Stop as before.
func (r *NativeRuntime) SetSnapshot(enabled bool, reclaimMinFraction float64) {
	r.snapshotEnabled = enabled
	r.reclaimMinFraction = reclaimMinFraction
}

// ensureSnapshotBase prepares the delegated cgroup base exactly once, on the
// first snapshot-enabled Start. It runs BEFORE the child is forked so the child
// is born in base/_supervisor (where shinyhub now lives) rather than base, which
// must stay empty of processes to delegate the memory controller. Any failure
// leaves snapshotReady false and warm-wake stays off for the process lifetime.
func (r *NativeRuntime) ensureSnapshotBase() {
	if !r.snapshotEnabled {
		return
	}
	r.snapshotOnce.Do(func() {
		base, err := ensureDelegatedBase()
		if err != nil {
			slog.Warn("native: warm-wake unavailable; apps hibernate via stop", "err", err)
			return
		}
		r.mu.Lock()
		r.cgroupBase = base
		r.snapshotReady = true
		r.mu.Unlock()
		slog.Info("native: warm-wake enabled", "cgroup_base", base)
	})
}

// placeInAppCgroup moves a just-started replica process into its own per-app
// cgroup so its memory can be reclaimed in isolation on Suspend. A failure is
// non-fatal: the replica simply hibernates via Stop instead of warm-freeze.
func (r *NativeRuntime) placeInAppCgroup(p StartParams, pid int) {
	r.mu.Lock()
	ready := r.snapshotReady
	base := r.cgroupBase
	r.mu.Unlock()
	if !ready {
		return
	}
	dir, err := setupAppCgroup(base, appCgroupName(p.Slug, p.Index), pid)
	if err != nil {
		slog.Warn("native: per-app cgroup setup failed; replica hibernates via stop",
			"slug", p.Slug, "idx", p.Index, "err", err)
		return
	}
	r.mu.Lock()
	r.appCgroups[pid] = dir
	r.mu.Unlock()
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
	r.ensureSnapshotBase()
	r.mu.Lock()
	ready := r.snapshotReady
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
	// Prepare the delegated cgroup base before forking (once) so the child is
	// born in base/_supervisor and can be moved into its own app cgroup below.
	r.ensureSnapshotBase()
	cmd := exec.Command(p.Command[0], p.Command[1:]...)
	cmd.Dir = p.Dir
	cmd.Env = nativeChildEnv(p)
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
	// Move the replica into its own cgroup for isolated warm-wake reclaim. No-op
	// when warm-wake is off or unavailable; failures degrade to stop-hibernate.
	r.placeInAppCgroup(p, pid)
	// MemoryLimitMB and CPUQuotaPercent are enforced by DockerRuntime only;
	// the native runtime inherits OS scheduling with no additional limits.
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
		r.teardownAppCgroupFor(handle.PID)
		return err
	}

	err := cmd.Wait()

	r.mu.Lock()
	delete(r.procs, handle.PID)
	r.mu.Unlock()
	// The process has exited, so its cgroup is now empty and can be removed.
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

// RunOnce blocks until the process exits or ctx is cancelled. On ctx cancel,
// the process group receives SIGTERM, then SIGKILL after a 10-second grace.
func (r *NativeRuntime) RunOnce(ctx context.Context, p StartParams, logWriter io.Writer) (ExitInfo, error) {
	if len(p.Command) == 0 {
		return ExitInfo{}, fmt.Errorf("command must not be empty")
	}
	if err := applySharedMounts(p); err != nil {
		return ExitInfo{}, err
	}
	cmd := exec.Command(p.Command[0], p.Command[1:]...)
	cmd.Dir = p.Dir
	cmd.Env = nativeChildEnv(p)
	cmd.Stdout = logWriter
	cmd.Stderr = logWriter
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return ExitInfo{}, fmt.Errorf("start one-shot: %w", err)
	}

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
	ready := r.snapshotReady
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
