package process

import (
	"context"
	"errors"
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

// SharedMountResolver returns the shared mounts for a slug. Empty slice means
// no mounts. Called once per Start; failures abort the start.
type SharedMountResolver func(slug string) ([]SharedMount, error)

type Status string

const (
	StatusRunning Status = "running"
	StatusStopped Status = "stopped"
	StatusCrashed Status = "crashed"
	StatusUnknown Status = "unknown"
)

type ProcessInfo struct {
	Slug   string
	Index  int
	PID    int
	Port   int
	Status Status
}

type StartParams struct {
	Slug            string
	Index           int
	Dir             string
	Command         []string
	Port            int
	Env             []string
	AppDataPath     string        // host path to per-app data dir; empty disables data-dir wiring in runtime
	MemoryLimitMB   int           // 0 = no limit
	CPUQuotaPercent int           // 0 = no limit; 100 = 1 full core
	SharedMounts    []SharedMount // resolved by caller before Start/RunOnce
}

type entry struct {
	info    *ProcessInfo
	handle  RunHandle
	done    chan struct{}
	stopped bool
}

// replicaKey identifies a specific replica by slug and index.
type replicaKey struct {
	Slug  string
	Index int
}

// Manager tracks running app processes as a pool of replicas per slug.
// entries maps slug → slice indexed by replica index; nil means that slot is down.
type Manager struct {
	mu              sync.Mutex
	entries         map[string][]*entry
	logFiles        map[replicaKey]*LogFile
	appsDir         string
	runtime         Runtime
	envResolver     EnvResolver
	mountResolver   SharedMountResolver
	appDataRoot     string
}

// SetEnvResolver sets the function used to inject per-app environment variables
// during Start. Must be called before the manager begins starting processes; it
// is not safe to call concurrently with Start.
func (m *Manager) SetEnvResolver(r EnvResolver) { m.envResolver = r }

// SetSharedMountResolver sets the function used to resolve shared mounts during
// Start. Must be called before the manager begins starting processes; not safe
// to call concurrently with Start.
func (m *Manager) SetSharedMountResolver(r SharedMountResolver) { m.mountResolver = r }

// SetAppDataRoot sets the root directory under which per-app persistent data
// directories live. Each Start resolves <root>/<slug>, ensures it exists,
// injects SHINYHUB_APP_DATA, and symlinks <bundle_dir>/data to it. An empty
// root disables the feature. Must be called before the manager begins
// starting processes; not safe to call concurrently with Start.
func (m *Manager) SetAppDataRoot(root string) { m.appDataRoot = root }

// HostPreparesDeps proxies to the underlying Runtime so deploy code can ask
// whether host-side dependency installation (uv sync, renv::restore) is
// expected before Start. See Runtime.HostPreparesDeps for the contract.
func (m *Manager) HostPreparesDeps() bool { return m.runtime.HostPreparesDeps() }

// AppBindHost proxies to the underlying Runtime so deploy code can construct
// the per-replica command with the right listen address. See
// Runtime.AppBindHost for the contract.
func (m *Manager) AppBindHost() string { return m.runtime.AppBindHost() }

// NewManager returns an initialized Manager using the given Runtime.
func NewManager(appsDir string, rt Runtime) *Manager {
	return &Manager{
		entries:  make(map[string][]*entry),
		logFiles: make(map[replicaKey]*LogFile),
		appsDir:  appsDir,
		runtime:  rt,
	}
}

// Start spawns a new process for the given slug and replica index.
// Returns an error if that replica is already running.
func (m *Manager) Start(p StartParams) (*ProcessInfo, error) {
	if len(p.Command) == 0 {
		return nil, fmt.Errorf("start: command must not be empty")
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	pool := m.entries[p.Slug]
	for len(pool) <= p.Index {
		pool = append(pool, nil)
	}
	if existing := pool[p.Index]; existing != nil && existing.info.Status == StatusRunning {
		return nil, fmt.Errorf("app %s replica %d already running", p.Slug, p.Index)
	}

	key := replicaKey{p.Slug, p.Index}
	if prev, ok := m.logFiles[key]; ok {
		prev.Close()
		delete(m.logFiles, key)
	}

	var appDataPath string
	if m.appDataRoot != "" {
		appDataPath = filepath.Join(m.appDataRoot, p.Slug)
		if err := os.MkdirAll(appDataPath, 0o750); err != nil {
			return nil, fmt.Errorf("ensure app data dir: %w", err)
		}
		p.AppDataPath = appDataPath
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

	if m.mountResolver != nil {
		mounts, err := m.mountResolver(p.Slug)
		if err != nil {
			return nil, fmt.Errorf("resolve shared mounts: %w", err)
		}
		p.SharedMounts = mounts
	}

	logPath := filepath.Join(m.appsDir, p.Slug, fmt.Sprintf("app-%d.log", p.Index))
	lf, err := OpenLogFile(logPath, DefaultLogMaxSize)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}
	m.logFiles[key] = lf

	handle, err := m.runtime.Start(context.Background(), p, lf)
	if err != nil {
		lf.Close()
		delete(m.logFiles, key)
		return nil, fmt.Errorf("start process: %w", err)
	}

	info := &ProcessInfo{
		Slug:   p.Slug,
		Index:  p.Index,
		PID:    handle.PID,
		Port:   p.Port,
		Status: StatusRunning,
	}
	done := make(chan struct{})
	pool[p.Index] = &entry{info: info, handle: handle, done: done}
	m.entries[p.Slug] = pool

	go func() {
		m.runtime.Wait(context.Background(), handle)
		m.mu.Lock()
		if pool := m.entries[p.Slug]; p.Index < len(pool) {
			if e := pool[p.Index]; e != nil && e.handle == handle {
				if e.stopped {
					e.info.Status = StatusStopped
				} else {
					e.info.Status = StatusCrashed
				}
			}
		}
		key := replicaKey{p.Slug, p.Index}
		if lf := m.logFiles[key]; lf != nil {
			lf.Close()
			delete(m.logFiles, key)
		}
		m.mu.Unlock()
		close(done)
	}()

	return info, nil
}

// StopReplica signals a single replica to stop and waits for it to exit.
// If the process does not exit within 5 seconds, SIGKILL is sent.
func (m *Manager) StopReplica(slug string, index int) error {
	m.mu.Lock()
	pool := m.entries[slug]
	if index >= len(pool) || pool[index] == nil {
		m.mu.Unlock()
		return fmt.Errorf("app %s replica %d not found", slug, index)
	}
	e := pool[index]
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
	pool = m.entries[slug]
	if index < len(pool) {
		pool[index] = nil
	}
	for len(pool) > 0 && pool[len(pool)-1] == nil {
		pool = pool[:len(pool)-1]
	}
	if len(pool) == 0 {
		delete(m.entries, slug)
	} else {
		m.entries[slug] = pool
	}
	m.mu.Unlock()
	return nil
}

// Stop signals all replicas for a slug to stop in parallel and waits for all to exit.
func (m *Manager) Stop(slug string) error {
	m.mu.Lock()
	pool := m.entries[slug]
	indices := make([]int, 0, len(pool))
	for i, e := range pool {
		if e != nil {
			indices = append(indices, i)
		}
	}
	m.mu.Unlock()

	if len(indices) == 0 {
		return fmt.Errorf("app %s not running", slug)
	}

	var wg sync.WaitGroup
	errs := make(chan error, len(indices))
	for _, i := range indices {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := m.StopReplica(slug, i); err != nil {
				errs <- fmt.Errorf("replica %d: %w", i, err)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	var combined []error
	for e := range errs {
		combined = append(combined, e)
	}
	if len(combined) > 0 {
		return errors.Join(combined...)
	}
	return nil
}

// Status returns the first running replica, or a synthetic stopped record.
// Callers that need per-replica info should use AllForSlug.
func (m *Manager) Status(slug string) (*ProcessInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.entries[slug] {
		if e != nil && e.info.Status == StatusRunning {
			snap := *e.info
			return &snap, nil
		}
	}
	return &ProcessInfo{Slug: slug, Status: StatusStopped}, nil
}

// All returns a snapshot of all tracked ProcessInfo entries across all slugs.
func (m *Manager) All() []*ProcessInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []*ProcessInfo{}
	for _, pool := range m.entries {
		for _, e := range pool {
			if e != nil {
				snap := *e.info
				out = append(out, &snap)
			}
		}
	}
	return out
}

// AllForSlug returns per-replica info for one slug, preserving index order.
// Slots for down replicas are nil.
func (m *Manager) AllForSlug(slug string) []*ProcessInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	pool := m.entries[slug]
	out := make([]*ProcessInfo, len(pool))
	for i, e := range pool {
		if e != nil {
			snap := *e.info
			out[i] = &snap
		}
	}
	return out
}

// GetReplica returns a snapshot of the ProcessInfo for a specific replica.
func (m *Manager) GetReplica(slug string, index int) (*ProcessInfo, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	pool := m.entries[slug]
	if index >= len(pool) || pool[index] == nil {
		return nil, false
	}
	snap := *pool[index].info
	return &snap, true
}

// HandleReplica returns the RunHandle for a specific replica, or false if not tracked.
func (m *Manager) HandleReplica(slug string, index int) (RunHandle, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	pool := m.entries[slug]
	if index >= len(pool) || pool[index] == nil {
		return RunHandle{}, false
	}
	return pool[index].handle, true
}

// Adopt re-registers a process that was not started by this Manager instance
// (e.g. recovered after a server restart). It starts the exit-monitoring
// goroutine so crashed processes are detected normally.
func (m *Manager) Adopt(slug string, info ProcessInfo, handle RunHandle) {
	m.mu.Lock()
	defer m.mu.Unlock()
	pool := m.entries[slug]
	for len(pool) <= info.Index {
		pool = append(pool, nil)
	}
	done := make(chan struct{})
	pool[info.Index] = &entry{info: &info, handle: handle, done: done}
	m.entries[slug] = pool
	go func() {
		m.runtime.Wait(context.Background(), handle)
		m.mu.Lock()
		if p := m.entries[slug]; info.Index < len(p) {
			if e := p[info.Index]; e != nil && e.handle == handle && !e.stopped {
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
	pool := m.entries[slug]
	for len(pool) <= info.Index {
		pool = append(pool, nil)
	}
	pool[info.Index] = &entry{info: &info, handle: RunHandle{PID: info.PID}, done: make(chan struct{})}
	m.entries[slug] = pool
}

// LogReader returns a LogReader for a specific replica's log file.
// Returns false if no log file exists yet (replica has never been started).
func (m *Manager) LogReader(slug string, index int) (*LogReader, bool) {
	path := filepath.Join(m.appsDir, slug, fmt.Sprintf("app-%d.log", index))
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
