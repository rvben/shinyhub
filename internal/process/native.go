package process

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	gops "github.com/shirou/gopsutil/v4/process"
)

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

// compile-time check that NativeRuntime implements Runtime.
var _ Runtime = (*NativeRuntime)(nil)

// NativeRuntime runs app processes as direct OS child processes.
type NativeRuntime struct {
	mu    sync.Mutex
	cmds  map[int]*exec.Cmd
	procs map[int]*gops.Process // cached gopsutil handles for CPU delta computation
}

// NewNativeRuntime returns a ready-to-use NativeRuntime.
func NewNativeRuntime() *NativeRuntime {
	return &NativeRuntime{
		cmds:  make(map[int]*exec.Cmd),
		procs: make(map[int]*gops.Process),
	}
}

func (r *NativeRuntime) Start(_ context.Context, p StartParams, logWriter io.Writer) (RunHandle, error) {
	if len(p.Command) == 0 {
		return RunHandle{}, fmt.Errorf("command must not be empty")
	}
	if err := applySharedMounts(p); err != nil {
		return RunHandle{}, err
	}
	cmd := exec.Command(p.Command[0], p.Command[1:]...)
	cmd.Dir = p.Dir
	cmd.Env = append(filteredEnv(), p.Env...)
	cmd.Stdout = logWriter
	cmd.Stderr = logWriter
	// Place the child in its own process group so signals can be sent to the
	// entire group, avoiding orphaned sub-processes.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return RunHandle{}, fmt.Errorf("start process: %w", err)
	}
	pid := cmd.Process.Pid
	r.mu.Lock()
	r.cmds[pid] = cmd
	r.mu.Unlock()
	// MemoryLimitMB and CPUQuotaPercent are enforced by DockerRuntime only;
	// the native runtime inherits OS scheduling with no additional limits.
	return RunHandle{PID: pid}, nil
}

func (r *NativeRuntime) Signal(handle RunHandle, sig syscall.Signal) error {
	// ESRCH means the process group is already gone — treat as a no-op so
	// Stop remains idempotent when the process exits before SIGTERM arrives.
	if err := syscall.Kill(-handle.PID, sig); err != nil && err != syscall.ESRCH {
		return fmt.Errorf("signal %v to pgid %d: %w", sig, handle.PID, err)
	}
	return nil
}

func (r *NativeRuntime) Wait(_ context.Context, handle RunHandle) error {
	r.mu.Lock()
	cmd, ok := r.cmds[handle.PID]
	if !ok {
		r.mu.Unlock()
		return nil
	}
	delete(r.cmds, handle.PID)
	r.mu.Unlock()

	err := cmd.Wait()

	r.mu.Lock()
	delete(r.procs, handle.PID)
	r.mu.Unlock()
	return err
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
	cmd.Env = append(filteredEnv(), p.Env...)
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
