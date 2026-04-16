package process

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"syscall"

	gops "github.com/shirou/gopsutil/v4/process"
)

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
