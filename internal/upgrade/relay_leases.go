package upgrade

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"
)

// RelayBackend names an exact backend, including its endpoint so a rolling
// replacement reusing a replica index cannot inherit an older process's lease.
type RelayBackend struct {
	Slug         string `json:"slug"`
	DeploymentID int64  `json:"deployment_id"`
	Index        int    `json:"index"`
	TargetURL    string `json:"target_url"`
	Sessions     int64  `json:"sessions"`
}

type RetiredGeneration struct {
	PID       int       `json:"pid,omitempty"`
	RetiredAt time.Time `json:"retired_at,omitempty"`
	Sessions  int64     `json:"sessions"`
}

type RetiredStatus struct {
	Generations []RetiredGeneration `json:"generations"`
	Count       int                 `json:"count"`
	Sessions    int64               `json:"sessions"`
	Unknown     bool                `json:"unknown"`
}

type relaySnapshot struct {
	PID       int            `json:"pid,omitempty"`
	RetiredAt time.Time      `json:"retired_at,omitempty"`
	Protocol  int            `json:"protocol"`
	Backends  []RelayBackend `json:"backends"`
}

// RelayLease holds a kernel lock for its entire lifetime. A missing or invalid
// snapshot behind a held lock is unknown, never idle. Published counts can only
// shrink after sealing, so delayed writes conservatively overcount activity.
type RelayLease struct {
	mu        sync.Mutex
	file      *os.File
	path      string
	sealed    map[RelayBackend]int64
	retiredAt time.Time
}

func NewRelayLease(dir string) (*RelayLease, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("relay directory must be a private directory")
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "relay-"+hex.EncodeToString(id[:])+".lock")
	file, err := os.CreateTemp(dir, ".relay-lock-*")
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		file.Close()
		os.Remove(file.Name())
		return nil, err
	}
	// Publish only after acquiring the lock; a scanner cannot collect an
	// unlocked inode between creation and flock.
	if err := os.Rename(file.Name(), path); err != nil {
		file.Close()
		os.Remove(file.Name())
		return nil, err
	}
	return &RelayLease{file: file, path: path, retiredAt: time.Now().UTC()}, nil
}

func backendKey(backend RelayBackend) RelayBackend { backend.Sessions = 0; return backend }

// Publish seals the initial set and rejects any later increase or new backend.
// Call only after HTTP handlers have quiesced and no new session can be admitted.
func (l *RelayLease) Publish(backends []RelayBackend) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return errors.New("relay lease is closed")
	}
	counts := make(map[RelayBackend]int64, len(backends))
	for _, backend := range backends {
		if backend.Slug == "" || backend.TargetURL == "" || backend.DeploymentID <= 0 || backend.Index < 0 || backend.Sessions < 0 {
			return errors.New("invalid relay backend")
		}
		key := backendKey(backend)
		if backend.Sessions > math.MaxInt64-counts[key] {
			return errors.New("relay session count overflow")
		}
		counts[key] += backend.Sessions
	}
	if l.sealed != nil {
		for key, count := range counts {
			if count > l.sealed[key] {
				return errors.New("sealed relay sessions cannot increase")
			}
		}
	}
	if l.sealed != nil && maps.Equal(counts, l.sealed) {
		return nil
	}
	data, err := json.Marshal(relaySnapshot{Protocol: 1, Backends: backends, PID: os.Getpid(), RetiredAt: l.retiredAt})
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(l.path), ".relay-snapshot-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Rename(file.Name(), strings.TrimSuffix(l.path, ".lock")+".json"); err != nil {
		return err
	}
	l.sealed = counts
	return nil
}

func (l *RelayLease) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	// Release the lock before removing the files. A concurrent reader can see
	// the lock become free, but can never interpret a live relay as absent.
	_ = os.Remove(l.path)
	_ = os.Remove(strings.TrimSuffix(l.path, ".lock") + ".json")
	return err
}

// RelayLeases scans the private local registry. Every read validates lock
// ownership instead of inferring liveness from a PID or a heartbeat timeout.
type RelayLeases struct {
	Dir       string
	view      atomic.Pointer[relayView]
	refreshMu sync.Mutex
}
type relayView struct {
	backends []RelayBackend
	unknown  bool
	// released maps a slug to when its relayed sessions were last seen to
	// reach zero; unknownCleared is when unverified leases last resolved,
	// which releases every slug at once.
	released       map[string]time.Time
	unknownCleared time.Time
}

// relayReleaseRetention bounds how long a release is remembered. It only has
// to outlast the short windows callers measure against it.
const relayReleaseRetention = time.Minute

// Unknown reports whether the watched view has unverified live leases.
func (r *RelayLeases) Unknown() bool {
	if view := r.view.Load(); view != nil {
		return view.unknown
	}
	_, _, unknown := r.Snapshot()
	return unknown
}

// Refresh updates a conservative immutable view. After parent retirement no
// relay can gain sessions, so delayed refreshes only overcount live work.
func (r *RelayLeases) Refresh() {
	r.refreshMu.Lock()
	defer r.refreshMu.Unlock()
	backends, _, unknown := r.Snapshot()
	next := &relayView{backends: backends, unknown: unknown}
	if prev := r.view.Load(); prev != nil {
		now := time.Now()
		next.unknownCleared = prev.unknownCleared
		if prev.unknown && !unknown {
			next.unknownCleared = now
		}
		held := make(map[string]bool, len(backends))
		for _, backend := range backends {
			if backend.Sessions > 0 {
				held[backend.Slug] = true
			}
		}
		next.released = make(map[string]time.Time)
		for slug, at := range prev.released {
			if !held[slug] && now.Sub(at) < relayReleaseRetention {
				next.released[slug] = at
			}
		}
		for _, backend := range prev.backends {
			if backend.Sessions > 0 && !held[backend.Slug] {
				next.released[backend.Slug] = now
			}
		}
	}
	r.view.Store(next)
}

// ReleasedAt reports when slug's relayed sessions were last observed to end,
// or the zero time when no recent release is known. A relayed connection
// closes in another process, so this observation is the only close a caller
// in this process can see. It is tracked across Refresh calls only.
func (r *RelayLeases) ReleasedAt(slug string) time.Time {
	view := r.view.Load()
	if view == nil {
		return time.Time{}
	}
	at := view.released[slug]
	if view.unknownCleared.After(at) {
		at = view.unknownCleared
	}
	return at
}

// Watch keeps filesystem scans and orphan collection off the request path.
// The parent-retirement handshake must Refresh before releasing its fence.
func (r *RelayLeases) Watch(ctx context.Context) func() {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	r.Refresh()
	go func() {
		defer close(done)
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				r.Refresh()
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { cancel(); <-done }) }
}

func (r *RelayLeases) Snapshot() (backends []RelayBackend, live int, unknown bool) {
	backends, status := r.inspect()
	return backends, status.Count, status.Unknown
}

// Status uses the same held-lock evidence as session admission and recovery.
func (r *RelayLeases) Status() RetiredStatus { _, status := r.inspect(); return status }

func (r *RelayLeases) inspect() (backends []RelayBackend, status RetiredStatus) {
	status.Generations = []RetiredGeneration{}

	entries, err := os.ReadDir(r.Dir)
	if os.IsNotExist(err) {
		return nil, status
	}
	if err != nil {
		status.Unknown = true
		return nil, status
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "relay-") || !strings.HasSuffix(entry.Name(), ".lock") {
			continue
		}
		path := filepath.Join(r.Dir, entry.Name())
		fd, err := unix.Open(path, unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			status.Unknown = true
			continue
		}
		file := os.NewFile(uintptr(fd), path)
		lockErr := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if lockErr == nil {
			// Dead snapshots may be collected without weakening a live lease.
			_ = os.Remove(strings.TrimSuffix(path, ".lock") + ".json")
			_ = os.Remove(path)
			file.Close()
			continue
		}
		file.Close()
		if !errors.Is(lockErr, unix.EWOULDBLOCK) && !errors.Is(lockErr, unix.EAGAIN) {
			status.Unknown = true
			continue
		}
		status.Count++
		snapshotFD, err := unix.Open(strings.TrimSuffix(path, ".lock")+".json", unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		var data []byte
		if err == nil {
			snapshotFile := os.NewFile(uintptr(snapshotFD), "relay-snapshot")
			data, err = io.ReadAll(io.LimitReader(snapshotFile, (8<<20)+1))
			snapshotFile.Close()
		}
		if err != nil {
			status.Unknown = true
			continue
		}
		var snapshot relaySnapshot
		if len(data) > 8<<20 || json.Unmarshal(data, &snapshot) != nil || snapshot.Protocol != 1 {
			status.Unknown = true
			continue
		}
		valid := true
		for _, backend := range snapshot.Backends {
			if backend.Slug == "" || backend.TargetURL == "" || backend.DeploymentID <= 0 || backend.Index < 0 || backend.Sessions < 0 {
				valid = false
				break
			}
		}
		if !valid {
			status.Unknown = true
			continue
		}
		backends = append(backends, snapshot.Backends...)
		generation := RetiredGeneration{PID: snapshot.PID, RetiredAt: snapshot.RetiredAt}
		for _, backend := range snapshot.Backends {
			if backend.Sessions > math.MaxInt64-generation.Sessions {
				status.Unknown = true
				generation.Sessions = math.MaxInt64
				break
			}
			generation.Sessions += backend.Sessions
		}
		status.Generations = append(status.Generations, generation)
		if generation.Sessions > math.MaxInt64-status.Sessions {
			status.Unknown = true
			status.Sessions = math.MaxInt64
		} else {
			status.Sessions += generation.Sessions
		}

	}
	return
}

func (r *RelayLeases) Sessions(slug string, deploymentID int64, index int, targetURL string) (int64, bool) {
	var backends []RelayBackend
	var unknown bool
	if view := r.view.Load(); view != nil {
		backends, unknown = view.backends, view.unknown
	} else {
		backends, _, unknown = r.Snapshot()
	}
	var total int64
	for _, backend := range backends {
		if backend.Slug == slug && (deploymentID == 0 || deploymentID == backend.DeploymentID) && (index < 0 || index == backend.Index) && (targetURL == "" || targetURL == backend.TargetURL) {
			if backend.Sessions > math.MaxInt64-total {
				return math.MaxInt64, true
			}
			total += backend.Sessions
		}
	}
	return total, unknown
}
