package process

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// EnvResolver returns a slice of "KEY=VALUE" strings for the given app slug.
// It is called during Start to inject per-app environment variables into the
// process before launch.
type EnvResolver func(slug string) ([]string, error)

type Status string

const (
	StatusRunning Status = "running"
	StatusStopped Status = "stopped"
	StatusCrashed Status = "crashed"
	StatusUnknown Status = "unknown"
)

type ProcessInfo struct {
	Slug   string
	PID    int
	Port   int
	Status Status
}

type StartParams struct {
	Slug            string
	Dir             string
	Command         []string
	Port            int
	Env             []string
	MemoryLimitMB   int // 0 = no limit
	CPUQuotaPercent int // 0 = no limit; 100 = 1 full core
}

type entry struct {
	info    *ProcessInfo
	handle  RunHandle
	done    chan struct{}
	stopped bool
}

// Manager tracks running app processes by slug.
type Manager struct {
	mu          sync.Mutex
	entries     map[string]*entry
	logFiles    map[string]*LogFile
	appsDir     string
	runtime     Runtime
	envResolver EnvResolver
	appDataRoot string
}

// SetEnvResolver sets the function used to inject per-app environment variables
// during Start. Must be called before the manager begins starting processes; it
// is not safe to call concurrently with Start.
func (m *Manager) SetEnvResolver(r EnvResolver) { m.envResolver = r }

// SetAppDataRoot sets the root directory under which per-app persistent data
// directories live. Each Start resolves <root>/<slug>, ensures it exists,
// injects SHINYHUB_APP_DATA, and symlinks <bundle_dir>/data to it. An empty
// root disables the feature. Must be called before the manager begins
// starting processes; not safe to call concurrently with Start.
func (m *Manager) SetAppDataRoot(root string) { m.appDataRoot = root }

// NewManager returns an initialized Manager using the given Runtime.
func NewManager(appsDir string, rt Runtime) *Manager {
	return &Manager{
		entries:  make(map[string]*entry),
		logFiles: make(map[string]*LogFile),
		appsDir:  appsDir,
		runtime:  rt,
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

	if existing, ok := m.logFiles[p.Slug]; ok {
		existing.Close()
		delete(m.logFiles, p.Slug)
	}

	var appDataPath string
	if m.appDataRoot != "" {
		appDataPath = filepath.Join(m.appDataRoot, p.Slug)
		if err := os.MkdirAll(appDataPath, 0o750); err != nil {
			return nil, fmt.Errorf("ensure app data dir: %w", err)
		}
		p.Env = append(p.Env, "SHINYHUB_APP_DATA="+appDataPath)

		linkPath := filepath.Join(p.Dir, "data")
		switch info, err := os.Lstat(linkPath); {
		case err == nil:
			// Something is already at <bundle>/data. Accept only if it's a symlink
			// pointing to the correct target — that's the idempotent restart case.
			if info.Mode()&os.ModeSymlink != 0 {
				existing, readErr := os.Readlink(linkPath)
				if readErr == nil && existing == appDataPath {
					break // already correct, nothing to do
				}
			}
			return nil, fmt.Errorf("bundle %s already contains a 'data' entry (%s); the platform reserves that path", p.Slug, info.Mode())
		case !os.IsNotExist(err):
			return nil, fmt.Errorf("stat %s: %w", linkPath, err)
		default:
			// Path does not exist — create the symlink.
			if err := os.Symlink(appDataPath, linkPath); err != nil {
				return nil, fmt.Errorf("symlink data: %w", err)
			}
		}
	}

	if m.envResolver != nil {
		userEnv, err := m.envResolver(p.Slug)
		if err != nil {
			return nil, fmt.Errorf("resolve env: %w", err)
		}
		// Build user env first, then append platform env so platform values win
		// on duplicate keys (os/exec uses last-occurrence-wins).
		p.Env = append(userEnv, p.Env...)
	}

	logPath := filepath.Join(m.appsDir, p.Slug, "app.log")
	lf, err := OpenLogFile(logPath, DefaultLogMaxSize)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}
	m.logFiles[p.Slug] = lf

	handle, err := m.runtime.Start(context.Background(), p, lf)
	if err != nil {
		lf.Close()
		delete(m.logFiles, p.Slug)
		return nil, fmt.Errorf("start process: %w", err)
	}

	info := &ProcessInfo{
		Slug:   p.Slug,
		PID:    handle.PID,
		Port:   p.Port,
		Status: StatusRunning,
	}
	done := make(chan struct{})
	m.entries[p.Slug] = &entry{info: info, handle: handle, done: done}

	go func() {
		m.runtime.Wait(context.Background(), handle)
		m.mu.Lock()
		if e, ok := m.entries[p.Slug]; ok && e.handle == handle {
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

// Stop signals the process to stop and waits for it to exit.
// If the process does not exit within 5 seconds, SIGKILL is sent.
func (m *Manager) Stop(slug string) error {
	m.mu.Lock()
	e, ok := m.entries[slug]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("app %s not found", slug)
	}
	done := e.done
	handle := e.handle
	e.stopped = true
	m.mu.Unlock()

	if err := m.runtime.Signal(handle, syscall.SIGTERM); err != nil {
		return fmt.Errorf("sigterm: %w", err)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		m.runtime.Signal(handle, syscall.SIGKILL) //nolint:errcheck
		<-done
	}

	m.mu.Lock()
	delete(m.entries, slug)
	m.mu.Unlock()
	return nil
}

// Status returns a snapshot of the ProcessInfo for the given slug.
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

// Get returns a snapshot of the ProcessInfo for slug, or false if not tracked.
func (m *Manager) Get(slug string) (*ProcessInfo, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[slug]
	if !ok {
		return nil, false
	}
	snapshot := *e.info
	return &snapshot, true
}

// Adopt re-registers a process that was not started by this Manager instance
// (e.g. recovered after a server restart). It starts the exit-monitoring
// goroutine so crashed processes are detected normally.
func (m *Manager) Adopt(slug string, info ProcessInfo, handle RunHandle) {
	m.mu.Lock()
	defer m.mu.Unlock()
	done := make(chan struct{})
	m.entries[slug] = &entry{info: &info, handle: handle, done: done}
	go func() {
		m.runtime.Wait(context.Background(), handle)
		m.mu.Lock()
		if e, ok := m.entries[slug]; ok && e.handle == handle {
			if !e.stopped {
				e.info.Status = StatusCrashed
			}
		}
		m.mu.Unlock()
		close(done)
	}()
}

// ForceEntry directly inserts a ProcessInfo without starting an exit-monitoring
// goroutine. Used in tests to inject state without starting a real process.
// For production recovery use Adopt, which starts the monitoring goroutine.
func (m *Manager) ForceEntry(slug string, info ProcessInfo) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[slug] = &entry{info: &info, handle: RunHandle{PID: info.PID}, done: make(chan struct{})}
}

// Handle returns the RunHandle for a running slug, or false if not tracked.
func (m *Manager) Handle(slug string) (RunHandle, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[slug]
	if !ok {
		return RunHandle{}, false
	}
	return e.handle, true
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

// filteredEnv returns the current process environment with all SHINYHUB_*
// variables removed, preventing server secrets from leaking into app processes.
func filteredEnv() []string {
	raw := os.Environ()
	filtered := make([]string, 0, len(raw))
	for _, e := range raw {
		if !strings.HasPrefix(e, "SHINYHUB_") {
			filtered = append(filtered, e)
		}
	}
	return filtered
}
