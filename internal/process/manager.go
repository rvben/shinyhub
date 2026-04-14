package process

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
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
	e := &entry{info: info, cmd: cmd}
	m.entries[p.Slug] = e

	// Reap the process in the background and mark it crashed if it exits
	// unexpectedly (i.e. not via Stop).
	go func() {
		cmd.Wait()
		m.mu.Lock()
		defer m.mu.Unlock()
		// Only update if this entry is still the one we started — Stop may have
		// already removed it.
		if cur, ok := m.entries[p.Slug]; ok && cur.cmd == cmd {
			cur.info.Status = StatusCrashed
		}
	}()

	return info, nil
}

// Stop sends SIGTERM to the process group of the named slug and removes it
// from the manager. If the process has already exited, Stop still cleans up
// the entry without returning an error.
func (m *Manager) Stop(slug string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	e, ok := m.entries[slug]
	if !ok {
		return fmt.Errorf("app %s not found", slug)
	}
	if e.cmd.Process != nil {
		// Negate PID to target the entire process group.
		err := syscall.Kill(-e.cmd.Process.Pid, syscall.SIGTERM)
		if err != nil && !errors.Is(err, syscall.ESRCH) {
			// ESRCH means the process already exited; treat as success.
			return fmt.Errorf("sigterm: %w", err)
		}
	}
	e.info.Status = StatusStopped
	delete(m.entries, slug)
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
