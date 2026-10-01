package process

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/rvben/shinyhub/internal/nativebroker"
)

// SystemdRuntime is the opt-in native backend with separate app identities.
// Unit names, not bare PIDs, are its durable operational identities. It exposes
// the same labelled-resource recovery contract as local container runtimes.
type SystemdRuntime struct {
	client             nativebroker.Client
	policy             nativebroker.Policy
	mu                 sync.Mutex
	stats              *NativeRuntime
	snapshotEnabled    bool
	reclaimMinFraction float64
	logs               map[string]systemdLog
}
type systemdLog struct {
	reader *os.File
	done   chan struct{}
}

func NewSystemdRuntime(ctx context.Context, socket string) (*SystemdRuntime, error) {
	if runtime.GOOS != "linux" {
		return nil, errors.New("native user isolation requires Linux; no fallback")
	}
	c := nativebroker.Client{Socket: socket}
	response, err := c.Call(ctx, nativebroker.Request{Op: "hello"}, nil)
	if err != nil {
		return nil, err
	}
	if response.Policy == nil {
		return nil, errors.New("native broker omitted policy")
	}
	p := *response.Policy
	if err = p.Validate(); err != nil {
		return nil, err
	}
	if p.ControlUID != os.Getuid() || p.ControlGID != os.Getgid() || p.Socket != socket {
		return nil, errors.New("native broker control identity or socket does not match this server")
	}
	groups, err := os.Getgroups()
	if err != nil {
		return nil, err
	}
	for _, a := range p.Apps {
		found := false
		for _, gid := range groups {
			if gid == a.GID {
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("control plane must belong to the private group for registered app %q (restart after provisioning groups)", a.Slug)
		}
	}
	return &SystemdRuntime{client: c, policy: p, stats: NewNativeRuntime(), logs: map[string]systemdLog{}}, nil
}
func (r *SystemdRuntime) HostPreparesDeps() bool      { return true }
func (r *SystemdRuntime) HostProvidesAppData() bool   { return true }
func (r *SystemdRuntime) AppBindHost() string         { return "127.0.0.1" }
func (r *SystemdRuntime) InheritsLifetimeFiles() bool { return true }
func (r *SystemdRuntime) SupportsGuardedStart() bool  { return true }

// PrepareStorage runs after control-file validation and before any app starts.
// Restore creates controller-owned directories; prepare every registered app,
// including those that will not launch immediately.
func (r *SystemdRuntime) PrepareStorage(ctx context.Context) error {
	parents := map[string]bool{}
	for _, a := range r.policy.Apps {
		for _, path := range []string{filepath.Dir(filepath.Dir(a.BundleRoot)), filepath.Dir(a.BundleRoot), filepath.Dir(a.DataRoot), filepath.Dir(a.CacheRoot)} {
			parents[path] = true
		}
	}
	for path := range parents {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil || resolved != path {
			return errors.New("isolated storage parents must not contain symlinks")
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		info, err := f.Stat()
		if err == nil {
			st, ok := info.Sys().(*syscall.Stat_t)
			if !ok || !info.IsDir() || int(st.Uid) != r.policy.ControlUID {
				err = errors.New("isolated storage parents must be owned by the controller")
			} else {
				err = f.Chmod(0711)
			}
		}
		_ = f.Close()
		if err != nil {
			return err
		}
	}
	_, err := r.client.Call(ctx, nativebroker.Request{Op: "prepare"}, nil)
	return err
}

func (r *SystemdRuntime) launch(ctx context.Context, p StartParams, kind string, out io.Writer) (ReplicaEndpoint, error) {
	if len(p.Command) == 0 {
		return ReplicaEndpoint{}, errors.New("command must not be empty")
	}
	dir, err := filepath.Abs(p.Dir)
	if err != nil {
		return ReplicaEndpoint{}, err
	}
	bin, err := resolveExecutable(p.Command[0], dir)
	if err != nil {
		return ReplicaEndpoint{}, err
	}
	argv := append([]string{bin}, p.Command[1:]...)
	// Always use a guard, even for ordinary starts: a lost RPC response cannot
	// leave an unrecorded unit executing app code.
	guardRead, guardWrite, err := os.Pipe()
	if err != nil {
		return ReplicaEndpoint{}, err
	}
	defer guardRead.Close()
	keepGuard := false
	defer func() {
		if !keepGuard {
			_ = guardWrite.Close()
		}
	}()
	logRead, logWrite, err := os.Pipe()
	if err != nil {
		return ReplicaEndpoint{}, err
	}
	defer logWrite.Close()
	if out == nil {
		out = io.Discard
	}
	logDone := make(chan struct{})
	go func() { defer close(logDone); defer logRead.Close(); _, _ = io.Copy(out, logRead) }()
	labels := dockerLabels(p)
	labels[LabelProvider] = "native"
	labels[LabelPort] = strconv.Itoa(p.Port)
	if kind == "job" {
		labels[LabelKind] = KindScheduleRun
	} else if kind != "replica" {
		labels[LabelKind] = kind
	}
	launch := nativebroker.Launch{AppID: p.AppID, Slug: p.Slug, Kind: kind, Dir: dir, Argv: argv, Env: nativeChildEnv(p),
		DataDir: p.AppDataPath, CacheDir: p.AppCachePath, MemoryMB: p.MemoryLimitMB, CPUPercent: p.CPUQuotaPercent,
		Guarded: true, LifetimeCount: len(p.LifetimeFiles), Labels: labels}
	files := append([]*os.File{logWrite, guardRead}, p.LifetimeFiles...)
	response, err := r.client.Call(ctx, nativebroker.Request{Op: "start", Launch: &launch}, files)
	if err != nil {
		return ReplicaEndpoint{}, err
	}
	if response.State == nil || response.State.PID <= 0 || response.State.Unit == "" {
		return ReplicaEndpoint{}, errors.New("native broker did not return a worker identity")
	}
	ep := ReplicaEndpoint{URL: fmt.Sprintf("http://127.0.0.1:%d", p.Port), Provider: "native", WorkerID: response.State.Unit,
		Handle: RunHandle{PID: response.State.PID, ContainerID: response.State.Unit}}
	r.mu.Lock()
	r.logs[ep.WorkerID] = systemdLog{reader: logRead, done: logDone}
	r.mu.Unlock()
	if kind == "replica" || kind == "job" {
		r.stats.ObserveWorkload(p, ep.Handle)
	}
	if p.GuardUntilAcknowledged {
		ep.StartupGuard = guardWrite
		keepGuard = true
	} else {
		if _, err = guardWrite.Write([]byte("ready\n")); err != nil {
			_, _ = r.client.Call(context.Background(), nativebroker.Request{Op: "stop", Unit: ep.WorkerID}, nil)
			r.finishLogs(ep.WorkerID)
			r.forgetStats(ep.Handle.PID)
			return ReplicaEndpoint{}, err
		}
	}
	return ep, nil
}
func (r *SystemdRuntime) Start(ctx context.Context, p StartParams, out io.Writer) (ReplicaEndpoint, error) {
	if len(p.SharedMounts) > 0 {
		return ReplicaEndpoint{}, errors.New("isolated native backend does not yet support cross-app shared mounts")
	}
	if err := applySharedMounts(p); err != nil {
		return ReplicaEndpoint{}, err
	}
	return r.launch(ctx, p, "replica", out)
}
func (r *SystemdRuntime) state(ctx context.Context, handle RunHandle) (nativebroker.State, error) {
	if handle.ContainerID == "" {
		return nativebroker.State{}, errors.New("isolated native operations require a unit identity")
	}
	result, err := r.client.Call(ctx, nativebroker.Request{Op: "status", Unit: handle.ContainerID}, nil)
	if err != nil {
		return nativebroker.State{}, err
	}
	if result.State == nil {
		return nativebroker.State{}, errors.New("broker omitted worker state")
	}
	return *result.State, nil
}
func (r *SystemdRuntime) Signal(h RunHandle, sig syscall.Signal) error {
	_, err := r.client.Call(context.Background(), nativebroker.Request{Op: "signal", Unit: h.ContainerID, Signal: int(sig)}, nil)
	return err
}
func (r *SystemdRuntime) Wait(ctx context.Context, h RunHandle) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		state, err := r.state(ctx, h)
		if err != nil {
			// Broker failure cannot prove physical worker exit.
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
				continue
			}
		}
		if !state.Active {
			// A replica leader exiting makes its remaining descendants orphan workers.
			if _, err = r.client.Call(ctx, nativebroker.Request{Op: "stop", Unit: h.ContainerID}, nil); err != nil {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-ticker.C:
					continue
				}
			}
			r.finishLogs(h.ContainerID)
			r.forgetStats(h.PID)
			if state.Code != 0 || state.Signaled {
				return fmt.Errorf("isolated native worker exited (code=%d, signaled=%t)", state.Code, state.Signaled)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
func (r *SystemdRuntime) RunOnce(ctx context.Context, p StartParams, out io.Writer) (ExitInfo, error) {
	if len(p.SharedMounts) > 0 {
		return ExitInfo{}, errors.New("isolated native backend does not yet support cross-app shared mounts")
	}
	if err := applySharedMounts(p); err != nil {
		return ExitInfo{}, err
	}
	return r.runOnce(ctx, p, "job", out)
}
func (r *SystemdRuntime) runOnce(ctx context.Context, p StartParams, kind string, out io.Writer) (ExitInfo, error) {
	p.GuardUntilAcknowledged = false
	ep, err := r.launch(ctx, p, kind, out)
	if err != nil {
		return ExitInfo{}, err
	}
	defer func() {
		_, _ = r.client.Call(context.Background(), nativebroker.Request{Op: "stop", Unit: ep.WorkerID}, nil)
		r.finishLogs(ep.WorkerID)
		r.forgetStats(ep.Handle.PID)
	}()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		state, err := r.state(ctx, ep.Handle)
		if err != nil {
			if ctx.Err() == nil {
				select {
				case <-ctx.Done():
				case <-ticker.C:
					continue
				}
			}
			stopCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			_, stopErr := r.client.Call(stopCtx, nativebroker.Request{Op: "stop", Unit: ep.WorkerID}, nil)
			cancel()
			if stopErr != nil {
				return ExitInfo{}, fmt.Errorf("%w: %v", ErrStopUnconfirmed, stopErr)
			}
			return ExitInfo{Code: -1, Signaled: true}, nil
		}
		if !state.Active && !state.Populated {
			if _, err = r.client.Call(ctx, nativebroker.Request{Op: "stop", Unit: ep.WorkerID}, nil); err != nil {
				return ExitInfo{}, fmt.Errorf("%w: %v", ErrStopUnconfirmed, err)
			}
			if state.Signaled {
				return ExitInfo{Code: -1, Signaled: true}, nil
			}
			return ExitInfo{Code: state.Code, Signaled: state.Signaled}, nil
		}
		select {
		case <-ctx.Done():
			stopCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if _, err = r.client.Call(stopCtx, nativebroker.Request{Op: "stop", Unit: ep.WorkerID}, nil); err != nil {
				return ExitInfo{}, fmt.Errorf("%w: %v", ErrStopUnconfirmed, err)
			}
			return ExitInfo{Code: -1, Signaled: true}, nil
		case <-ticker.C:
		}
	}
}

// RunBuild also handles project conversion and hooks; they cannot silently
// fall back to the controller identity when this backend is configured.
func (r *SystemdRuntime) RunBuild(ctx context.Context, dir string, argv, env []string, out io.Writer) error {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	for _, a := range r.policy.Apps {
		rel, err := filepath.Rel(a.BundleRoot, dir)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		p := StartParams{AppID: a.ID, Slug: a.Slug, Dir: dir, Command: argv, Env: env}
		exit, err := r.runOnce(ctx, p, "build", out)
		if err != nil {
			return err
		}
		if exit.Code != 0 || exit.Signaled {
			return fmt.Errorf("isolated build exited (code=%d, signaled=%t)", exit.Code, exit.Signaled)
		}
		return nil
	}
	return errors.New("build directory is not registered in native broker policy")
}
func (r *SystemdRuntime) Stats(ctx context.Context, h RunHandle) (*float64, uint64, error) {
	state, err := r.state(ctx, h)
	if err != nil {
		return nil, 0, err
	}
	if !state.Active {
		return nil, 0, errors.New("isolated worker is not running")
	}
	// Metadata reads do not require access to /proc/environ or cwd.
	return r.stats.Stats(ctx, RunHandle{PID: state.PID})
}

// Labelled unit inventory plugs into the existing local-resource recovery and
// orphan-sweep paths. A restart never adopts a bare or reused PID.
func (r *SystemdRuntime) ListByLabel(filter string) ([]ContainerInfo, error) {
	var result []ContainerInfo
	cursor := ""
	for {
		response, err := r.client.Call(context.Background(), nativebroker.Request{Op: "list", Cursor: cursor}, nil)
		if err != nil {
			return nil, err
		}
		for _, s := range response.States {
			state := "exited"
			if s.Active {
				state = "created"
				if s.Ready {
					state = "running"
				}
			}
			if s.Frozen {
				state = "paused"
			}
			result = append(result, ContainerInfo{ID: s.Unit, Labels: s.Labels, State: state})
		}
		if response.NextCursor == "" {
			break
		}
		if response.NextCursor <= cursor {
			return nil, errors.New("broker inventory cursor did not advance")
		}
		cursor = response.NextCursor
	}
	return result, nil
}
func (r *SystemdRuntime) InspectPID(unit string) (int, error) {
	state, err := r.state(context.Background(), RunHandle{ContainerID: unit})
	if err == nil && !state.Ready {
		return 0, nil
	}
	return state.PID, err
}
func (r *SystemdRuntime) RemoveContainer(unit string) error {
	_, err := r.client.Call(context.Background(), nativebroker.Request{Op: "stop", Unit: unit}, nil)
	if err == nil {
		r.finishLogs(unit)
	}
	return err
}
func (r *SystemdRuntime) RemoveHandle(h RunHandle) error { return r.RemoveContainer(h.ContainerID) }
func (r *SystemdRuntime) finishLogs(unit string) {
	r.mu.Lock()
	log, ok := r.logs[unit]
	r.mu.Unlock()
	if !ok {
		return
	}
	select {
	case <-log.done:
	case <-time.After(leaderExitPipeGrace):
		_ = log.reader.Close()
		<-log.done
	}
	r.mu.Lock()
	delete(r.logs, unit)
	r.mu.Unlock()
}
func (r *SystemdRuntime) SetSnapshot(enabled bool, fraction float64) {
	r.snapshotEnabled, r.reclaimMinFraction = enabled, fraction
}
func (r *SystemdRuntime) Suspend(ctx context.Context, h RunHandle) (bool, error) {
	if !r.snapshotEnabled {
		return false, ErrRuntimeNotSnapshotter
	}
	response, err := r.client.Call(ctx, nativebroker.Request{Op: "suspend", Unit: h.ContainerID, ReclaimFraction: r.reclaimMinFraction}, nil)
	return response.Freed, err
}
func (r *SystemdRuntime) Resume(ctx context.Context, h RunHandle) (ReplicaEndpoint, error) {
	_, err := r.client.Call(ctx, nativebroker.Request{Op: "thaw", Unit: h.ContainerID}, nil)
	if err != nil {
		stopCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if _, stopErr := r.client.Call(stopCtx, nativebroker.Request{Op: "stop", Unit: h.ContainerID}, nil); stopErr != nil {
			return ReplicaEndpoint{}, fmt.Errorf("%w: resume failed: %v; stop: %v", ErrStopUnconfirmed, err, stopErr)
		}
		r.finishLogs(h.ContainerID)
		r.forgetStats(h.PID)
	}
	return ReplicaEndpoint{Provider: "native", WorkerID: h.ContainerID, Handle: h}, err
}
func (r *SystemdRuntime) UpdateResources(ctx context.Context, h RunHandle, limits ResourceLimits) error {
	_, err := r.client.Call(ctx, nativebroker.Request{Op: "limits", Unit: h.ContainerID, Limits: &nativebroker.Limits{MemoryMB: limits.MemoryLimitMB, CPUPercent: limits.CPUQuotaPercent}}, nil)
	return err
}
func (r *SystemdRuntime) SetWorkloadObserver(observer WorkloadObserver) {
	r.stats.SetWorkloadObserver(observer)
}
func (r *SystemdRuntime) ObserveRecoveredWorkload(p StartParams, h RunHandle) {
	r.stats.ObserveWorkload(p, h)
}
func (r *SystemdRuntime) forgetStats(pid int) {
	r.stats.finishObservation(pid)
	r.stats.mu.Lock()
	delete(r.stats.procs, pid)
	r.stats.mu.Unlock()
}

// ValidateStorage prevents a policy from silently pointing at a different
// storage layout than the control plane. Registration is deliberately root-only.
func (r *SystemdRuntime) ValidateStorage(apps, data, cache string) error {
	roots := []*string{&apps, &data, &cache}
	for _, root := range roots {
		abs, err := filepath.Abs(*root)
		if err != nil {
			return err
		}
		*root = abs
	}
	for _, a := range r.policy.Apps {
		if a.BundleRoot != filepath.Join(apps, a.Slug, "versions") || a.DataRoot != filepath.Join(data, a.Slug) || a.CacheRoot != filepath.Join(cache, a.Slug) {
			return fmt.Errorf("registered storage paths for app %q do not match server configuration", a.Slug)
		}
	}

	return nil
}

// Private control storage must not live in an app-writable tree. The private
// parent also protects SQLite journals, snapshots and server-running markers.
func (r *SystemdRuntime) ValidateControlDatabase(path string) error {
	if err := r.ValidateControlPath(path); err != nil {
		return err
	}
	if path == ":memory:" {
		return nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	parent, err := os.Stat(filepath.Dir(abs))
	if err != nil {
		return err
	}
	stat, ok := parent.Sys().(*syscall.Stat_t)
	if !ok || !parent.IsDir() || int(stat.Uid) != r.policy.ControlUID || parent.Mode().Perm()&0077 != 0 {
		return errors.New("isolated native SQLite storage requires a controller-owned private directory (0700); move the database there while stopped")
	}
	return nil
}

// ValidateControlPath rejects a control secret/config path that preparation
// would otherwise make app-readable. Resolve existing symlinks as well.
func (r *SystemdRuntime) ValidateControlPath(path string) error {
	if path == "" || path == ":memory:" {
		return nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	paths := []string{abs}
	if parent, err := filepath.EvalSymlinks(filepath.Dir(abs)); err == nil {
		paths = append(paths, filepath.Join(parent, filepath.Base(abs)))
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		paths = append(paths, resolved)
	}
	for _, a := range r.policy.Apps {
		for _, root := range []string{filepath.Dir(filepath.Dir(a.BundleRoot)), filepath.Dir(a.DataRoot), filepath.Dir(a.CacheRoot)} {
			for _, candidate := range paths {
				rel, err := filepath.Rel(root, candidate)
				if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
					return errors.New("control-plane files must be outside registered app storage")
				}
			}
		}
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if errors.Is(err, os.ErrNotExist) {
		return nil // An absent default config path contains no secret.
	}
	if err != nil {
		return err
	}
	for _, a := range r.policy.Apps {
		needed := os.FileMode(4) // Read the file, traverse each parent.
		for candidate := resolved; ; candidate = filepath.Dir(candidate) {
			info, err := os.Stat(candidate)
			if err != nil {
				return err
			}
			st, ok := info.Sys().(*syscall.Stat_t)
			if !ok {
				return errors.New("cannot verify control file permissions")
			}
			mode := info.Mode().Perm()
			if int(st.Uid) == a.UID {
				mode >>= 6
			} else if int(st.Gid) == a.GID {
				mode >>= 3
			}
			if mode&needed == 0 {
				break
			}
			if candidate == string(filepath.Separator) {
				return errors.New("control config and secret files must not be readable by app users; use mode 0600 or a private controller directory")
			}
			needed = 1
		}
	}
	return nil
}
