package process

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	info    *ProcessInfo
	cmd     *exec.Cmd
	done    chan struct{}
	stopped bool
}

// Manager tracks running app processes by slug.
type Manager struct {
	mu       sync.Mutex
	entries  map[string]*entry
	logFiles map[string]*LogFile
	appsDir  string
}

// NewManager returns an initialized Manager. appsDir is the root directory
// where per-app log files are stored as <appsDir>/<slug>/app.log.
func NewManager(appsDir string) *Manager {
	return &Manager{
		entries:  make(map[string]*entry),
		logFiles: make(map[string]*LogFile),
		appsDir:  appsDir,
	}
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

	// Close any existing write handle before opening a fresh one.
	if existing, ok := m.logFiles[p.Slug]; ok {
		existing.Close()
		delete(m.logFiles, p.Slug)
	}

	logPath := filepath.Join(m.appsDir, p.Slug, "app.log")
	lf, err := OpenLogFile(logPath, DefaultLogMaxSize)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}
	m.logFiles[p.Slug] = lf

	cmd := exec.Command(p.Command[0], p.Command[1:]...)
	cmd.Dir = p.Dir
	cmd.Env = append(os.Environ(), p.Env...)
	cmd.Stdout = lf
	cmd.Stderr = lf
	// Place the child in its own process group so SIGTERM can be sent to the
	// entire group, avoiding orphaned sub-processes.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		lf.Close()
		delete(m.logFiles, p.Slug)
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
			if e.stopped {
				e.info.Status = StatusStopped
			} else {
				e.info.Status = StatusCrashed
			}
		}
		if lf, ok := m.logFiles[p.Slug]; ok {
			lf.Close()
			delete(m.logFiles, p.Slug)
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
	e.stopped = true
	m.mu.Unlock()

	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil && err != syscall.ESRCH {
		return fmt.Errorf("sigterm: %w", err)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
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

// LogReader returns a LogReader for the app's log file. Returns false if no
// log file exists yet (app has never been started).
func (m *Manager) LogReader(slug string) (*LogReader, bool) {
	path := filepath.Join(m.appsDir, slug, "app.log")
	if _, err := os.Stat(path); err != nil {
		return nil, false
	}
	return NewLogReader(path), true
}
