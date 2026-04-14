package process

import (
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

type Status string

const (
	StatusRunning Status = "running"
	StatusStopped Status = "stopped"
	StatusCrashed Status = "crashed"
)

type ProcessInfo struct {
	Slug   string
	PID    int
	Port   int
	Status Status
}

type StartParams struct {
	Slug    string
	Dir     string
	Command []string
	Port    int
	Env     []string
}

type entry struct {
	info *ProcessInfo
	cmd  *exec.Cmd
	done chan struct{} // closed when the process exits
}

// Manager tracks running app processes by slug.
type Manager struct {
	mu      sync.Mutex
	entries map[string]*entry
}

// NewManager returns an initialized Manager.
func NewManager() *Manager {
	return &Manager{entries: make(map[string]*entry)}
}

// Start spawns a new process for the given slug. Returns an error if the slug
// is already running.
func (m *Manager) Start(p StartParams) (*ProcessInfo, error) {
	if len(p.Command) == 0 {
		return nil, fmt.Errorf("start: command must not be empty")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if e, ok := m.entries[p.Slug]; ok && e.info.Status == StatusRunning {
		return nil, fmt.Errorf("app %s is already running", p.Slug)
	}

	cmd := exec.Command(p.Command[0], p.Command[1:]...)
	cmd.Dir = p.Dir
	cmd.Env = append(os.Environ(), p.Env...)
	// Place the child in its own process group so SIGTERM can be sent to the
	// entire group, avoiding orphaned sub-processes.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start process: %w", err)
	}

	info := &ProcessInfo{
		Slug:   p.Slug,
		PID:    cmd.Process.Pid,
		Port:   p.Port,
		Status: StatusRunning,
	}
	done := make(chan struct{})
	m.entries[p.Slug] = &entry{info: info, cmd: cmd, done: done}

	// Reap the process in the background and mark it crashed if it exits
	// unexpectedly (i.e. not via Stop).
	go func() {
		cmd.Wait()
		m.mu.Lock()
		if e, ok := m.entries[p.Slug]; ok && e.cmd == cmd {
			e.info.Status = StatusCrashed
		}
		m.mu.Unlock()
		close(done)
	}()

	return info, nil
}

// Stop sends SIGTERM to the process group of the named slug and waits for the
// process to exit before removing the entry. If the process does not exit
// within 5 seconds, SIGKILL is sent. This guarantees the port is free when
// Stop returns.
func (m *Manager) Stop(slug string) error {
	m.mu.Lock()
	e, ok := m.entries[slug]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("app %s not found", slug)
	}
	if e.cmd.Process == nil {
		m.mu.Unlock()
		return nil
	}
	done := e.done
	pid := e.cmd.Process.Pid
	m.mu.Unlock() // release lock before blocking ops

	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil && err != syscall.ESRCH {
		return fmt.Errorf("sigterm: %w", err)
	}

	// Wait for the process to exit (reaper goroutine closes done).
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		// SIGKILL the process group if it didn't exit gracefully.
		syscall.Kill(-pid, syscall.SIGKILL) //nolint:errcheck
		<-done
	}

	m.mu.Lock()
	delete(m.entries, slug)
	m.mu.Unlock()
	return nil
}

// Status returns a snapshot of the ProcessInfo for the given slug. If the slug
// is not known, it returns a stopped ProcessInfo without an error.
func (m *Manager) Status(slug string) (*ProcessInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	e, ok := m.entries[slug]
	if !ok {
		return &ProcessInfo{Slug: slug, Status: StatusStopped}, nil
	}
	snapshot := *e.info
	return &snapshot, nil
}

// All returns a snapshot of all tracked ProcessInfo entries.
func (m *Manager) All() []*ProcessInfo {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]*ProcessInfo, 0, len(m.entries))
	for _, e := range m.entries {
		snapshot := *e.info
		out = append(out, &snapshot)
	}
	return out
}
