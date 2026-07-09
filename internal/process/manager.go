package process

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/rvben/shinyhub/internal/storage"
)

// EnvResolver returns the per-app environment for the given slug as two
// "KEY=VALUE" slices: env holds non-secret values, secretEnv holds decrypted
// secret values. They are kept separate so a runtime can deliver secrets out of
// band (e.g. the Fargate task definition's secrets block) instead of as
// plaintext. It is called during Start to inject per-app env before launch.
type EnvResolver func(slug string) (env []string, secretEnv []string, err error)

// dropReservedKeys returns env with every "KEY=VALUE" entry whose key also
// appears in reserved removed. It is used to stop a per-app secret from
// shadowing an authoritative deploy/platform-supplied variable (the reserved
// set) that is injected later in the child environment.
func dropReservedKeys(env, reserved []string) []string {
	if len(env) == 0 || len(reserved) == 0 {
		return env
	}
	reservedKeys := make(map[string]struct{}, len(reserved))
	for _, kv := range reserved {
		if i := strings.IndexByte(kv, '='); i > 0 {
			reservedKeys[kv[:i]] = struct{}{}
		}
	}
	out := env[:0:0]
	for _, kv := range env {
		i := strings.IndexByte(kv, '=')
		if i > 0 {
			if _, ok := reservedKeys[kv[:i]]; ok {
				continue
			}
		}
		out = append(out, kv)
	}
	return out
}

// PlatformDefaultEnvResolver returns "KEY=VALUE" platform defaults that should
// be set BEFORE the user's per-app env, so user values win on duplicate keys.
// This is the slot for OTEL_* env vars that the operator configures
// platform-wide but each app may still override per-app via the env-var UI.
// Returning nil disables the hook.
type PlatformDefaultEnvResolver func(slug string, replica int) []string

// SharedMountResolver returns the shared mounts for a slug. Empty slice means
// no mounts. Called once per Start; failures abort the start.
type SharedMountResolver func(slug string) ([]SharedMount, error)

type Status string

const (
	StatusRunning   Status = "running"
	StatusStopped   Status = "stopped"
	StatusCrashed   Status = "crashed"
	StatusUnknown   Status = "unknown"
	StatusSuspended Status = "suspended"
)

// DefaultTier is the tier name a replica runs under when StartParams.Tier is
// empty. Single-node deployments use exactly this one tier.
const DefaultTier = "local"

type ProcessInfo struct {
	Slug         string
	Index        int
	PID          int
	Port         int
	Status       Status
	Tier         string
	Provider     string
	EndpointURL  string
	WorkerID     string
	AppVersion   string
	DeploymentID int64
	// OOMKilled is set when this replica's most recent exit was a kernel
	// OOM-kill (it exceeded its memory limit). Used to surface a crash reason
	// that names the limit rather than a generic crash.
	OOMKilled bool
}

// ExitVerdict records how a replica's most recent process exited. It survives
// the entry being replaced on restart so the watcher can still name an OOM-kill
// after the crash-restart budget is spent.
type ExitVerdict struct {
	OOMKilled bool
	// MemoryLimitMB is the enforced per-replica limit at the time of the exit
	// (0 when unknown, e.g. an adopted process). The watcher falls back to the
	// app's stored limit when this is 0.
	MemoryLimitMB int
	At            time.Time
}

type StartParams struct {
	Slug string
	// AppID is the owning app's numeric DB id. It is used to namespace per-app
	// external resources (e.g. Fargate secret store names and task-definition
	// families) so a delete-then-recreate of the same slug never collides.
	// Zero when unknown (paths that do not touch per-app external resources).
	AppID   int64
	Index   int
	Tier    string // runtime tier; empty => DefaultTier
	Dir     string
	Command []string
	Port    int
	// HostPublishPort, when non-zero, is the host port to publish the
	// in-container bind Port to. The control plane allocates Port (baked into
	// the command and PORT env); a remote worker allocates HostPublishPort on
	// its own host. Zero means publish to the same port as Port (local case).
	HostPublishPort int
	Env             []string
	// SecretEnv carries decrypted secret env vars ("KEY=VALUE"), kept in a slice
	// separate from Env. Every runtime currently injects SecretEnv as plaintext
	// alongside Env (the native, Docker, and Fargate runtimes concatenate the
	// two; a key is either secret or not, so the order is immaterial). The slice
	// is kept distinct so the Fargate runtime can later route these values
	// through the task definition's secrets block instead of plaintext task
	// overrides; until then secret values are NOT hidden from ecs:DescribeTasks.
	SecretEnv       []string
	AppDataPath     string        // host path to per-app data dir; empty disables data-dir wiring in runtime
	MemoryLimitMB   int           // 0 = no limit
	CPUQuotaPercent int           // 0 = no limit; 100 = 1 full core
	SharedMounts    []SharedMount // resolved by caller before Start/RunOnce
	AppVersion      string        // app version stamped onto labels/metadata
	DeploymentID    int64         // owning deployment; 0 when unknown
	ContentDigest   string        // bundle content digest; "" when unknown (remote runtime pulls by this)
	// TargetWorker pins this replica to a specific worker node id. Deploy
	// pre-plans a multi-replica pool's worker assignments up front (so a
	// concurrent batch spreads instead of every replica self-placing onto the
	// same least-loaded worker against an identical pre-deploy snapshot) and
	// stamps the chosen worker here. Empty means the runtime self-places against
	// live load, which is correct for a single-replica boot (e.g. a watchdog
	// restart). Runtimes that do not route to workers ignore it.
	TargetWorker string
	// MaxSessions is the per-replica active-connection hard cap enforced at the
	// worker data plane. 0 means no cap. Persisted as a Docker label so re-adoption
	// after an agent restart restores the same limit.
	MaxSessions int
	// JobRunID, when non-zero, marks this as a one-shot scheduled-job run (via
	// RunOnce, not Start). It namespaces the job's own cgroup (job-<slug>-<runID>)
	// so a capped job never shares replica 0's app-<slug>-0 cgroup.
	JobRunID int64
}

type entry struct {
	info    *ProcessInfo
	handle  RunHandle
	tier    string
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
	mu            sync.Mutex
	entries       map[string][]*entry
	logFiles      map[replicaKey]*LogFile
	lastExit      map[replicaKey]ExitVerdict
	appsDir       string
	runtimesMu    sync.RWMutex
	runtimes      map[string]Runtime
	defaultTier   string
	envResolver   EnvResolver
	platformEnv   PlatformDefaultEnvResolver
	mountResolver SharedMountResolver
	appDataRoot   string
	stopGrace     time.Duration

	autoInstrumentApps bool
}

// SetStopGrace sets how long StopReplica waits after SIGTERM before escalating
// to SIGKILL. Must be called before the manager begins stopping processes; it
// is not safe to call concurrently with StopReplica.
func (m *Manager) SetStopGrace(d time.Duration) {
	if d > 0 {
		m.stopGrace = d
	}
}

// SetEnvResolver sets the function used to inject per-app environment variables
// during Start. Must be called before the manager begins starting processes; it
// is not safe to call concurrently with Start.
func (m *Manager) SetEnvResolver(r EnvResolver) { m.envResolver = r }

// SetPlatformDefaultEnvResolver sets the function that supplies platform-wide
// default env vars (currently OTEL_* tracing config). The returned values are
// prepended to the env so user-supplied per-app env wins on duplicate keys.
// Must be called before Start; not safe to call concurrently with Start.
func (m *Manager) SetPlatformDefaultEnvResolver(r PlatformDefaultEnvResolver) {
	m.platformEnv = r
}

// SetAutoInstrumentAppsDefault sets the fleet-wide default for launching
// Python apps under opentelemetry-instrument. Wired once at startup from
// tracing.auto_instrument_apps, before any deploys run, like the platform
// default env resolver. Must be called before Start; not safe to call
// concurrently with boots.
func (m *Manager) SetAutoInstrumentAppsDefault(v bool) {
	m.autoInstrumentApps = v
}

// AutoInstrumentAppsDefault reports the fleet-wide auto-instrumentation
// default. Per-app shinyhub.toml [tracing] auto overrides it at boot time.
func (m *Manager) AutoInstrumentAppsDefault() bool {
	return m.autoInstrumentApps
}

// SetSharedMountResolver sets the function used to resolve shared mounts during
// Start. Must be called before the manager begins starting processes; not safe
// to call concurrently with Start.
func (m *Manager) SetSharedMountResolver(r SharedMountResolver) { m.mountResolver = r }

// SetAppDataRoot sets the root directory under which per-app persistent data
// directories live. Each Start resolves <root>/<slug>, ensures it exists,
// stamps it onto StartParams.AppDataPath, and symlinks <bundle_dir>/data to
// it. Injection of SHINYHUB_APP_DATA into the child env is the Runtime's
// responsibility (NativeRuntime uses the host path; DockerRuntime translates
// to the in-container mount path) — the Manager only owns the dir + symlink.
// An empty root disables the feature. Must be called before the manager
// begins starting processes; not safe to call concurrently with Start.
func (m *Manager) SetAppDataRoot(root string) error {
	if root == "" {
		m.appDataRoot = ""
		return nil
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve app data root: %w", err)
	}
	m.appDataRoot = abs
	return nil
}

// HostPreparesDepsFor proxies to the runtime registered for the named tier so
// deploy code can ask whether host-side dependency installation (uv sync,
// renv::restore) is expected before Start. An empty or unregistered tier falls
// back to the default tier. See Runtime.HostPreparesDeps for the contract.
func (m *Manager) HostPreparesDepsFor(tier string) bool {
	return m.runtimeFor(tier).HostPreparesDeps()
}

// AppBindHostFor proxies to the runtime registered for the named tier so deploy
// code can construct the per-replica command with the right listen address. An
// empty or unregistered tier falls back to the default tier. See
// Runtime.AppBindHost for the contract.
func (m *Manager) AppBindHostFor(tier string) string {
	return m.runtimeFor(tier).AppBindHost()
}

// TierHasDurableDataFor reports whether app-data on the named tier survives task
// restart/hibernation and is shared across replicas. A runtime that does not
// implement DurableDataReporter is treated as durable (native/docker/remote all
// back the data dir with a persistent host directory); only Fargate reports
// ephemeral storage when no durable backend is configured. An empty or
// unregistered tier falls back to the default tier. The durable-data guard uses
// this to block deploying a data-using app onto a tier that would lose its data.
func (m *Manager) TierHasDurableDataFor(tier string) bool {
	if r, ok := m.runtimeFor(tier).(DurableDataReporter); ok {
		return r.TierHasDurableData()
	}
	return true
}

// TransportForWorker returns the HTTP transport a tier's runtime requires for
// reaching replicas hosted by the named worker, or nil to use the default
// transport. Runtimes opt in by implementing ReplicaTransporter; routes are
// keyed per-worker so a replica is always dialed with its host worker's
// transport even when several workers serve the tier.
func (m *Manager) TransportForWorker(tier, nodeID string) http.RoundTripper {
	rt := m.runtimeFor(tier)
	if tr, ok := rt.(ReplicaTransporter); ok {
		return tr.ReplicaTransportForWorker(nodeID)
	}
	return nil
}

// ReplicaPlacer is the optional capability a worker-routing Runtime implements
// to plan where a batch of replicas should land. PlanPlacement returns one
// target worker node id per replica, in assignment order, spreading the batch
// across the tier's workers. Runtimes that do not route to workers (the native
// local tier) do not implement it.
type ReplicaPlacer interface {
	PlanPlacement(slug string, count int) []string
}

// PlanPlacement asks the tier's runtime to plan worker assignments for count
// replicas of slug, returning one target worker node id per replica in
// assignment order. It returns nil when the tier's runtime does not route to
// workers (native local tier), in which case replicas have no target worker and
// the runtime places them itself. Deploy calls this once up front so a
// concurrent pool boot spreads across workers instead of each replica
// self-placing against the same pre-deploy load snapshot.
func (m *Manager) PlanPlacement(tier, slug string, count int) []string {
	rt := m.runtimeFor(tier)
	if pl, ok := rt.(ReplicaPlacer); ok {
		return pl.PlanPlacement(slug, count)
	}
	return nil
}

// NewManager returns an initialized Manager using the given Runtime as the
// default ("local") tier. Additional tiers are added via RegisterRuntime.
func NewManager(appsDir string, rt Runtime) *Manager {
	return &Manager{
		entries:     make(map[string][]*entry),
		logFiles:    make(map[replicaKey]*LogFile),
		lastExit:    make(map[replicaKey]ExitVerdict),
		appsDir:     appsDir,
		runtimes:    map[string]Runtime{DefaultTier: rt},
		defaultTier: DefaultTier,
		stopGrace:   defaultStopGrace,
	}
}

// defaultStopGrace is the SIGTERM-to-SIGKILL window for a single replica. It is
// generous enough for a Shiny/R or Python app to flush session state and close
// its on-disk stores on shutdown; operators with slower cleanup can raise it via
// SetStopGrace (server.stop_grace).
const defaultStopGrace = 10 * time.Second

// RegisterRuntime adds or replaces the runtime for the named tier. Safe to
// call concurrently with RuntimeForTier lookups.
func (m *Manager) RegisterRuntime(tier string, rt Runtime) {
	m.runtimesMu.Lock()
	defer m.runtimesMu.Unlock()
	m.runtimes[tier] = rt
}

// removeRuntime drops the runtime registered for a tier. Lookups for that tier
// fall back to the default runtime afterward.
func (m *Manager) removeRuntime(tier string) {
	m.runtimesMu.Lock()
	defer m.runtimesMu.Unlock()
	delete(m.runtimes, tier)
}

// SetDefaultTier renames the default tier and rekeys the seed runtime under
// that name. NewManager registers the seed runtime under DefaultTier ("local");
// when the config's first tier is named differently, call this once at startup
// so empty/unknown tiers still resolve to the seed runtime. A no-op when name
// is empty or already the default. Must be called before the manager begins
// starting processes; it is not safe to call concurrently with Start.
func (m *Manager) SetDefaultTier(name string) {
	if name == "" || name == m.defaultTier {
		return
	}
	m.runtimesMu.Lock()
	defer m.runtimesMu.Unlock()
	rt := m.runtimes[m.defaultTier]
	delete(m.runtimes, m.defaultTier)
	m.runtimes[name] = rt
	m.defaultTier = name
}

// RuntimeForTier returns the runtime backing the named tier, falling back to
// the default tier when tier is empty or unregistered. Exposed for recovery,
// which routes each replica's re-adoption to its tier's runtime (so one app's
// replicas can span a native default tier and a container-backed burst tier).
func (m *Manager) RuntimeForTier(tier string) Runtime { return m.runtimeFor(tier) }

// runtimeFor returns the runtime for the named tier, falling back to the
// default tier when tier is empty or unregistered.
func (m *Manager) runtimeFor(tier string) Runtime {
	m.runtimesMu.RLock()
	defer m.runtimesMu.RUnlock()
	if rt, ok := m.runtimes[tier]; ok {
		return rt
	}
	return m.runtimes[m.defaultTier]
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
		return nil, fmt.Errorf("app %s replica %d: %w", p.Slug, p.Index, ErrReplicaAlreadyRunning)
	}

	key := replicaKey{p.Slug, p.Index}
	if prev, ok := m.logFiles[key]; ok {
		prev.Close()
		delete(m.logFiles, key)
	}

	tier := p.Tier
	if tier == "" {
		tier = m.defaultTier
	}
	rt := m.runtimeFor(tier)

	if rt.HostProvidesAppData() {
		var appDataPath string
		if m.appDataRoot != "" {
			ref, err := storage.LocalVolume{Root: m.appDataRoot}.Provision(p.Slug)
			if err != nil {
				return nil, fmt.Errorf("provision app data: %w", err)
			}
			appDataPath = ref.Path
			p.AppDataPath = appDataPath

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
	}

	if m.envResolver != nil {
		userEnv, userSecretEnv, err := m.envResolver(p.Slug)
		if err != nil {
			return nil, fmt.Errorf("resolve env: %w", err)
		}
		// The env passed into Start (e.g. the allocated PORT from deploy) is
		// authoritative and must win over per-app env. Non-secret user env is
		// prepended below, so the deploy values already beat it under
		// last-occurrence-wins. SecretEnv, however, is injected AFTER Env by the
		// runtimes, so a per-app secret keyed the same as a deploy var would
		// shadow it; drop such secrets to preserve the deploy-wins precedence.
		userSecretEnv = dropReservedKeys(userSecretEnv, p.Env)
		// Prepend the resolved user env; the deploy-supplied env passed into
		// Start stays at the tail so it wins on duplicate keys (os/exec uses
		// last-occurrence-wins).
		p.Env = append(userEnv, p.Env...)
		// Secret env stays in its own slice so a runtime can deliver it out of
		// band; no platform/deploy source contributes secrets, so resolver
		// values are authoritative.
		p.SecretEnv = append(userSecretEnv, p.SecretEnv...)
	}
	if m.platformEnv != nil {
		// Platform defaults (e.g. OTEL_*) go BEFORE the user env above so the
		// user's per-app override wins on duplicate keys. We rebuild p.Env in
		// the order: [defaults, user env, deploy-supplied p.Env] — last write
		// wins, so deploy env beats user env beats defaults.
		if defaults := m.platformEnv(p.Slug, p.Index); len(defaults) > 0 {
			p.Env = append(defaults, p.Env...)
		}
	}

	if m.mountResolver != nil {
		mounts, err := m.mountResolver(p.Slug)
		if err != nil {
			return nil, fmt.Errorf("resolve shared mounts: %w", err)
		}
		p.SharedMounts = mounts
	}

	if !rt.HostProvidesAppData() {
		for i := range p.SharedMounts {
			p.SharedMounts[i].HostPath = ""
		}
	}

	logPath := filepath.Join(m.appsDir, p.Slug, fmt.Sprintf("app-%d.log", p.Index))
	lf, err := OpenLogFile(logPath, DefaultLogMaxSize)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}
	m.logFiles[key] = lf

	ep, err := rt.Start(context.Background(), p, lf)
	if err != nil {
		lf.Close()
		delete(m.logFiles, key)
		return nil, fmt.Errorf("start process: %w", err)
	}
	handle := ep.Handle

	info := &ProcessInfo{
		Slug:         p.Slug,
		Index:        p.Index,
		PID:          handle.PID,
		Port:         p.Port,
		Status:       StatusRunning,
		Tier:         tier,
		Provider:     ep.Provider,
		EndpointURL:  ep.URL,
		WorkerID:     ep.WorkerID,
		AppVersion:   p.AppVersion,
		DeploymentID: p.DeploymentID,
	}
	done := make(chan struct{})
	pool[p.Index] = &entry{info: info, handle: handle, tier: tier, done: done}
	m.entries[p.Slug] = pool

	go func() {
		// close(done) is deferred so it always fires - even on a panic below -
		// so a concurrent StopReplica waiting on done never hangs. The recover
		// keeps a fault in Wait or the bookkeeping from crashing the whole
		// server. defers run LIFO, so the locked section's Unlock runs first
		// (releasing the mutex even on panic), then recover, then close.
		defer close(done)
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("manager: exit-monitor goroutine panicked",
					"slug", p.Slug, "idx", p.Index, "panic", rec)
			}
		}()
		rt.Wait(context.Background(), handle)
		// Consume the OOM verdict from the runtime BEFORE taking m.mu: the
		// runtime read takes its own lock, and the verdict was stashed by Wait
		// before it tore the cgroup down.
		oom := consumeOOM(rt, handle.PID)
		m.mu.Lock()
		defer m.mu.Unlock()
		// Only the entry's current owner reacts to this exit. After an eviction or
		// a replacement Start at the same key, a stale Wait sees a nil or different
		// entry and must touch neither status nor the log file — otherwise it would
		// close the replacement replica's log out from under it.
		if pool := m.entries[p.Slug]; p.Index < len(pool) {
			if e := pool[p.Index]; e != nil && e.handle == handle {
				key := replicaKey{p.Slug, p.Index}
				// Record the exit verdict (and OOM flag) BEFORE flipping Status,
				// so no reader under m.mu can observe the crash without it. Every
				// exit overwrites the verdict, so a non-OOM exit clears a stale OOM.
				e.info.OOMKilled = oom
				m.lastExit[key] = ExitVerdict{OOMKilled: oom, MemoryLimitMB: p.MemoryLimitMB, At: time.Now()}
				if e.stopped {
					e.info.Status = StatusStopped
				} else {
					e.info.Status = StatusCrashed
				}
				if lf := m.logFiles[key]; lf != nil {
					lf.Close()
					delete(m.logFiles, key)
				}
			}
		}
	}()

	return info, nil
}

// StopReplica signals a single replica to stop and waits for it to exit.
// If the process does not exit within the stop grace (default defaultStopGrace,
// configurable via SetStopGrace), SIGKILL is sent.
func (m *Manager) StopReplica(slug string, index int) error {
	m.mu.Lock()
	pool := m.entries[slug]
	if index >= len(pool) || pool[index] == nil {
		m.mu.Unlock()
		return fmt.Errorf("app %s replica %d: %w", slug, index, ErrReplicaNotFound)
	}
	e := pool[index]
	done := e.done
	handle := e.handle
	e.stopped = true
	tier := e.tier
	m.mu.Unlock()

	rt := m.runtimeFor(tier)
	// A container may be frozen - either intentionally suspended, or left paused
	// by a suspend whose unpause failed. SIGTERM (and `docker kill`) do not reach
	// a frozen resource until it is thawed. Resume is idempotent (a no-op on a
	// running, non-paused container), so unconditionally thaw before signalling:
	// this avoids both a hung stop and a leaked paused container, regardless of
	// the entry's recorded status.
	if sn, ok := rt.(Snapshotter); ok {
		if _, err := sn.Resume(context.Background(), handle); err != nil {
			slog.Warn("manager: unfreeze before stop failed", "slug", slug, "idx", index, "err", err)
		}
	}
	if err := rt.Signal(handle, syscall.SIGTERM); err != nil {
		// The signal was not delivered, so this replica is still running. Undo
		// the intentional-stop mark set above so that if it later exits on its
		// own the monitor classifies it as crashed (and the watchdog restarts
		// it) rather than as an intentional stop left dead.
		m.mu.Lock()
		e.stopped = false
		m.mu.Unlock()
		return fmt.Errorf("sigterm: %w", err)
	}
	grace := m.stopGrace
	if grace <= 0 {
		grace = defaultStopGrace
	}
	select {
	case <-done:
	case <-time.After(grace):
		slog.Warn("manager: replica did not exit within grace; sending SIGKILL",
			"slug", slug, "idx", index, "grace", grace)
		rt.Signal(handle, syscall.SIGKILL) //nolint:errcheck
		select {
		case <-done:
		case <-time.After(grace):
			// SIGKILL did not take effect within a second grace window: the
			// process is likely in uninterruptible sleep (e.g. a hung NFS /
			// shared-mount app-data backend). Proceed instead of blocking the
			// caller forever - a blocked Stop here freezes the watchdog and
			// stalls crash-restart/hibernation for the whole fleet. The exit
			// monitor reconciles the entry if the process ever does exit.
			slog.Error("manager: replica did not exit after SIGKILL within grace; proceeding (process may be in uninterruptible sleep)",
				"slug", slug, "idx", index, "grace", grace)
		}
	}

	m.mu.Lock()
	// A clean stop is not a crash: drop any exit verdict so a later unrelated
	// crash never reads a stale OOM flag from this incarnation.
	delete(m.lastExit, replicaKey{slug, index})
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

	// Container runtimes keep the stopped container around (no AutoRemove on
	// long-running apps) so recovery can inspect a crash. Now that the Manager
	// has confirmed this replica exited on an intentional stop/replace, drop
	// it so stopped containers do not accumulate. Native runtime does not
	// implement this capability; the assertion simply fails and is skipped.
	if cr, ok := rt.(containerRemover); ok {
		if err := cr.RemoveHandle(handle); err != nil {
			slog.Warn("manager: remove container after stop", "slug", slug, "idx", index, "err", err)
		}
	}
	return nil
}

// containerRemover is the optional capability a container Runtime implements
// to delete the backing container once a replica has stopped. NativeRuntime
// does not implement it.
type containerRemover interface {
	RemoveHandle(RunHandle) error
}

// EvictReplicaIfWorker removes a replica from the manager's view without
// signaling its runtime, freeing the slug+index slot so a re-placement Start
// succeeds. It is used when the backing worker is already gone (heartbeat
// down-sweep or admin revoke): unlike StopReplica it sends no signal, because
// dialing a dead worker would hang. The evicted entry's log file is closed under
// the lock; the entry's own (now stale) exit-monitor goroutine sees the slot nil
// and is a no-op.
//
// Eviction is gated on the entry still being owned by workerID: a worker-loss
// pass can race a redeploy that already re-placed this slot onto a healthy worker
// (registering its route and starting a new manager entry before persisting the
// new replica row). Evicting unconditionally would drop that live replacement; so
// an entry owned by a different worker is left untouched. A no-op when the slot is
// already empty.
func (m *Manager) EvictReplicaIfWorker(slug string, index int, workerID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	pool := m.entries[slug]
	if index >= len(pool) || pool[index] == nil {
		return
	}
	if pool[index].info.WorkerID != workerID {
		return
	}
	pool[index] = nil
	for len(pool) > 0 && pool[len(pool)-1] == nil {
		pool = pool[:len(pool)-1]
	}
	if len(pool) == 0 {
		delete(m.entries, slug)
	} else {
		m.entries[slug] = pool
	}
	key := replicaKey{slug, index}
	if lf := m.logFiles[key]; lf != nil {
		lf.Close()
		delete(m.logFiles, key)
	}
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

// Suspend freezes every running replica of slug via the tier runtime's
// Snapshotter capability, releasing host RAM. It returns freed=true ONLY when
// every replica's warmed memory was released; in that case each frozen replica's
// in-memory status becomes StatusSuspended (the entry is kept - the process/
// container is paused, not gone). If the runtime is not a Snapshotter, or any
// replica could not be freed, Suspend restores any replicas it had frozen (so
// the whole pool is back to a normal running state the caller can Stop) and
// returns freed=false, so the caller falls back to Stop (which always frees RAM).
func (m *Manager) Suspend(slug string) (bool, error) {
	type target struct {
		index  int
		handle RunHandle
		tier   string
	}
	m.mu.Lock()
	pool := m.entries[slug]
	targets := make([]target, 0, len(pool))
	for i, e := range pool {
		if e != nil && e.info.Status == StatusRunning {
			targets = append(targets, target{i, e.handle, e.tier})
		}
	}
	m.mu.Unlock()
	if len(targets) == 0 {
		return false, fmt.Errorf("app %s not running", slug)
	}

	frozen := make([]target, 0, len(targets))
	var firstErr error
	allFreed := true
	for _, t := range targets {
		sn, ok := m.runtimeFor(t.tier).(Snapshotter)
		if !ok {
			allFreed = false
			firstErr = ErrRuntimeNotSnapshotter
			break
		}
		freed, err := sn.Suspend(context.Background(), t.handle)
		switch {
		case err != nil:
			allFreed = false
			if firstErr == nil {
				firstErr = err
			}
		case !freed:
			allFreed = false
		default:
			frozen = append(frozen, t)
		}
		if !allFreed {
			// Abort early: we are falling back to Stop for the whole pool, so
			// freezing the remaining replicas would only be undone below.
			break
		}
	}

	if allFreed {
		m.mu.Lock()
		pool := m.entries[slug]
		for _, t := range frozen {
			// Re-check identity under the lock: a concurrent stop/replace may have
			// niled or replaced this slot while Suspend ran unlocked. Only flip the
			// status of the entry we actually froze, never a fresh replacement.
			if t.index < len(pool) && pool[t.index] != nil && pool[t.index].handle == t.handle {
				pool[t.index].info.Status = StatusSuspended
			}
		}
		m.mu.Unlock()
		return true, nil
	}

	// Partial/failed: restore any replicas we froze (Resume is idempotent) so the
	// whole pool is back to a normal running state and the caller's Stop path
	// works without hitting a frozen cgroup.
	for _, t := range frozen {
		if sn, ok := m.runtimeFor(t.tier).(Snapshotter); ok {
			if _, rerr := sn.Resume(context.Background(), t.handle); rerr != nil {
				slog.Warn("manager: restore after partial suspend failed", "slug", slug, "idx", t.index, "err", rerr)
			}
		}
	}
	return false, firstErr
}

// SuspendReplica freezes a single running replica via the tier runtime's
// Snapshotter, releasing its host RAM, and flips the in-memory entry to
// StatusSuspended on success. It is the per-replica analogue of Suspend, mirroring
// Resume's index-addressed shape, used by the warm pool to freeze drained replicas
// while the floor keeps serving.
//
// Returns freed=true ONLY when the warmed memory was actually released. On any
// other result the driver has already restored the replica to a normally stoppable
// state (per the Snapshotter contract), so the entry is left StatusRunning and the
// caller falls back to StopReplica: (false, ErrRuntimeNotSnapshotter) when the tier
// runtime cannot snapshot, (false, nil) when too little was reclaimed, (false, err)
// on a driver error.
func (m *Manager) SuspendReplica(slug string, index int) (bool, error) {
	m.mu.Lock()
	pool := m.entries[slug]
	if index >= len(pool) || pool[index] == nil {
		m.mu.Unlock()
		return false, fmt.Errorf("app %s replica %d: %w", slug, index, ErrReplicaNotFound)
	}
	e := pool[index]
	if e.info.Status != StatusRunning {
		m.mu.Unlock()
		return false, fmt.Errorf("app %s replica %d: not running", slug, index)
	}
	handle, tier := e.handle, e.tier
	m.mu.Unlock()

	sn, ok := m.runtimeFor(tier).(Snapshotter)
	if !ok {
		return false, fmt.Errorf("app %s replica %d: %w", slug, index, ErrRuntimeNotSnapshotter)
	}
	freed, err := sn.Suspend(context.Background(), handle)
	if err != nil || !freed {
		// The driver restored the replica per contract; leave it StatusRunning so
		// the caller's StopReplica path operates on a normal, stoppable replica.
		return false, err
	}

	m.mu.Lock()
	// Re-check identity under the lock: a concurrent stop/replace may have niled or
	// replaced this slot while Suspend ran unlocked. Only flip the entry we froze.
	if pool := m.entries[slug]; index < len(pool) && pool[index] != nil && pool[index].handle == handle {
		pool[index].info.Status = StatusSuspended
	}
	m.mu.Unlock()
	return true, nil
}

// Resume restores a single suspended replica via the tier runtime's Snapshotter
// capability and returns its (possibly updated) route endpoint. The in-memory
// entry returns to StatusRunning with the resumed endpoint's URL/WorkerID/handle.
// Returns a wrapped ErrRuntimeNotSnapshotter, ErrReplicaNotSuspended, or
// ErrReplicaNotFound sentinel when the slot cannot be resumed, so the caller
// cold-boots it instead.
func (m *Manager) Resume(slug string, index int) (ReplicaEndpoint, error) {
	m.mu.Lock()
	pool := m.entries[slug]
	if index >= len(pool) || pool[index] == nil {
		m.mu.Unlock()
		return ReplicaEndpoint{}, fmt.Errorf("app %s replica %d: %w", slug, index, ErrReplicaNotFound)
	}
	e := pool[index]
	if e.info.Status != StatusSuspended {
		m.mu.Unlock()
		return ReplicaEndpoint{}, fmt.Errorf("app %s replica %d: %w", slug, index, ErrReplicaNotSuspended)
	}
	handle, tier := e.handle, e.tier
	m.mu.Unlock()

	sn, ok := m.runtimeFor(tier).(Snapshotter)
	if !ok {
		return ReplicaEndpoint{}, fmt.Errorf("app %s replica %d: %w", slug, index, ErrRuntimeNotSnapshotter)
	}
	ep, err := sn.Resume(context.Background(), handle)
	if err != nil {
		// The driver has torn down the stale resource per contract; the entry is
		// left for the caller's cold-boot (Start) to replace.
		return ReplicaEndpoint{}, fmt.Errorf("resume replica %d: %w", index, err)
	}

	m.mu.Lock()
	// Re-check identity under the lock: only update the entry we resumed, never a
	// fresh replacement created by a concurrent stop/start while Resume ran
	// unlocked (matches the Start/StopReplica handle-equality idiom).
	if pool := m.entries[slug]; index < len(pool) && pool[index] != nil && pool[index].handle == handle {
		if ep.URL == "" {
			// In-place resume (e.g. docker unpause) preserves the route; keep the
			// known endpoint URL rather than clobbering it with an empty one. A
			// driver that restores under a new identity returns a non-empty URL.
			ep.URL = pool[index].info.EndpointURL
		}
		pool[index].info.Status = StatusRunning
		pool[index].info.EndpointURL = ep.URL
		pool[index].info.WorkerID = ep.WorkerID
		pool[index].handle = ep.Handle
	}
	m.mu.Unlock()
	return ep, nil
}

// StopAll gracefully stops every tracked app across all slugs, concurrently.
// Used on server shutdown when server.shutdown_apps is "stop" so the host is
// left clean instead of with orphaned subprocesses/containers. Errors are
// aggregated; a failure to stop one app does not block the others.
func (m *Manager) StopAll() error {
	m.mu.Lock()
	slugs := make([]string, 0, len(m.entries))
	for slug, pool := range m.entries {
		for _, e := range pool {
			if e != nil {
				slugs = append(slugs, slug)
				break
			}
		}
	}
	m.mu.Unlock()

	var wg sync.WaitGroup
	errs := make(chan error, len(slugs))
	for _, slug := range slugs {
		wg.Add(1)
		go func(slug string) {
			defer wg.Done()
			if err := m.Stop(slug); err != nil {
				errs <- fmt.Errorf("%s: %w", slug, err)
			}
		}(slug)
	}
	wg.Wait()
	close(errs)
	var combined []error
	for e := range errs {
		combined = append(combined, e)
	}
	return errors.Join(combined...)
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

// RunningContainerIDs returns the set of container IDs the Manager currently
// has adopted across all slugs. Empty for native runtime (handles carry a PID,
// not a container ID). Used by the startup orphan-container sweep to decide
// which ShinyHub-labeled containers have no live owner.
func (m *Manager) RunningContainerIDs() map[string]bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make(map[string]bool)
	for _, pool := range m.entries {
		for _, e := range pool {
			if e != nil && e.handle.ContainerID != "" {
				ids[e.handle.ContainerID] = true
			}
		}
	}
	return ids
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
	tier := info.Tier
	if tier == "" {
		tier = m.defaultTier
	}
	info.Tier = tier
	done := make(chan struct{})

	// Register the pool slot under the lock, then release it BEFORE the warm
	// re-adopt (file I/O) and the monitoring goroutine. The locked section is a
	// scoped func with its own defer so a panic cannot leak the manager lock.
	var rt Runtime
	func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		rt = m.runtimeFor(tier)
		pool := m.entries[slug]
		for len(pool) <= info.Index {
			pool = append(pool, nil)
		}
		pool[info.Index] = &entry{info: &info, handle: handle, tier: tier, done: done}
		m.entries[slug] = pool
	}()

	// Re-register warm-wake state for an adopted-after-restart replica so it can
	// be warm-frozen and warm-resumed again rather than cold-booting on its next
	// hibernate (Start does this via placeInAppCgroup; Adopt must do the
	// equivalent). Done outside the lock - it touches cgroup files - and
	// best-effort: ErrRuntimeNotSnapshotter (warm-wake off) is silent, and any
	// other error means the warm state is gone, so the replica hibernates via
	// Stop, exactly as before this re-registration existed.
	if rd, ok := rt.(WarmReadopter); ok {
		if err := rd.ReadoptWarm(slug, info.Index, handle.PID); err != nil && !errors.Is(err, ErrRuntimeNotSnapshotter) {
			slog.Warn("manager: warm re-adopt failed; replica will hibernate via stop",
				"slug", slug, "idx", info.Index, "err", err)
		}
	}
	// Re-register the resource-limit cgroup independent of warm-wake so an adopted
	// limited replica can still be torn down and have OOM-kills detected.
	if cr, ok := rt.(CgroupReadopter); ok {
		if err := cr.ReadoptCgroup(slug, info.Index, handle.PID); err != nil {
			slog.Warn("manager: resource-limit cgroup re-adopt failed",
				"slug", slug, "idx", info.Index, "err", err)
		}
	}

	go func() {
		// See the Start monitor: deferred close keeps StopReplica from hanging on
		// a panic, recover keeps the fault from crashing the server, and the
		// locked section's Unlock runs first (mutex released even on panic).
		defer close(done)
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("manager: adopt exit-monitor goroutine panicked",
					"slug", slug, "idx", info.Index, "panic", rec)
			}
		}()
		rt.Wait(context.Background(), handle)
		oom := consumeOOM(rt, handle.PID)
		m.mu.Lock()
		defer m.mu.Unlock()
		if p := m.entries[slug]; info.Index < len(p) {
			if e := p[info.Index]; e != nil && e.handle == handle && !e.stopped {
				key := replicaKey{slug, info.Index}
				// MemoryLimitMB is unknown for an adopted process (no StartParams);
				// the watcher falls back to the app's stored limit when it is 0.
				e.info.OOMKilled = oom
				m.lastExit[key] = ExitVerdict{OOMKilled: oom, MemoryLimitMB: 0, At: time.Now()}
				e.info.Status = StatusCrashed
			}
		}
	}()
}

// oomReporter is the optional runtime capability to report whether a just-exited
// process was OOM-killed (exceeded its cgroup memory limit). Only the native
// runtime implements it; container/remote runtimes report false here (Docker
// surfaces OOM through its own container state).
type oomReporter interface {
	ConsumeOOMKill(pid int) bool
}

// consumeOOM reads-and-clears the OOM verdict for a just-exited pid from the
// runtime, or false when the runtime does not track OOM.
func consumeOOM(rt Runtime, pid int) bool {
	if r, ok := rt.(oomReporter); ok {
		return r.ConsumeOOMKill(pid)
	}
	return false
}

// ResourceEnforcement reports whether per-app memory/CPU limits are actually
// enforced across the given tiers (an app may span several). A limit is reported
// enforced only when enforced on EVERY tier (AND), so an app that runs partly on
// a native host without cgroup delegation is correctly flagged. The native
// runtime is best-effort (gated on cgroup v2 delegation); container/remote
// runtimes apply hard limits, so a runtime that does not implement
// ResourceEnforcer is treated as enforcing both. With no tiers, the default tier
// is used.
func (m *Manager) ResourceEnforcement(tiers ...string) (memory, cpu bool) {
	if len(tiers) == 0 {
		tiers = []string{m.defaultTier}
	}
	memory, cpu = true, true
	for _, tier := range tiers {
		if re, ok := m.runtimeFor(tier).(ResourceEnforcer); ok {
			tm, tc := re.ResourceEnforcement()
			memory = memory && tm
			cpu = cpu && tc
		}
	}
	return memory, cpu
}

// LastExit returns the most recent exit verdict for a replica (whether it was
// OOM-killed and the limit in force), or ok=false when none is recorded.
func (m *Manager) LastExit(slug string, index int) (ExitVerdict, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.lastExit[replicaKey{slug, index}]
	return v, ok
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
	pool[info.Index] = &entry{info: &info, handle: RunHandle{PID: info.PID}, tier: m.defaultTier, done: make(chan struct{})}
	m.entries[slug] = pool
}

// LogTail returns the last n lines of a replica's log file joined by newlines,
// or "" when the log cannot be read. Used to capture a crash diagnostic (e.g. a
// Python traceback) when an app transitions to "crashed".
func (m *Manager) LogTail(slug string, index, n int) string {
	r, ok := m.LogReader(slug, index)
	if !ok {
		return ""
	}
	lines, err := r.Tail(n)
	if err != nil {
		return ""
	}
	return strings.Join(lines, "\n")
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

// appEnvAllowExact / appEnvAllowPrefixes are the allow-list of server-process
// environment variables that may pass through to app-controlled code. Everything
// else (AWS/GCP/Azure credentials, tokens, or any other secret the operator
// happens to have in ShinyHub's own environment) is dropped. Operators whose
// apps need an additional inherited variable name it in SHINYHUB_APP_ENV_ALLOW
// (comma-separated) rather than exposing the whole environment; the intended
// mechanism for app-specific values remains per-app env vars and the manifest.
var appEnvAllowExact = map[string]struct{}{
	"PATH": {}, "HOME": {}, "USER": {}, "LOGNAME": {}, "SHELL": {}, "PWD": {},
	"LANG": {}, "LANGUAGE": {}, "TERM": {}, "TZ": {},
	"TMPDIR": {}, "TMP": {}, "TEMP": {},
	"SSL_CERT_FILE": {}, "SSL_CERT_DIR": {}, "CURL_CA_BUNDLE": {},
	"REQUESTS_CA_BUNDLE": {}, "NODE_EXTRA_CA_CERTS": {},
	"HTTP_PROXY": {}, "HTTPS_PROXY": {}, "NO_PROXY": {}, "ALL_PROXY": {},
	"http_proxy": {}, "https_proxy": {}, "no_proxy": {}, "all_proxy": {},
	"XDG_CACHE_HOME": {}, "XDG_DATA_HOME": {}, "XDG_CONFIG_HOME": {}, "XDG_RUNTIME_DIR": {},
	"UV_CACHE_DIR": {}, "UV_PYTHON_INSTALL_DIR": {}, "PIP_CACHE_DIR": {},
	// Package-index configuration (private registries). Same class as the
	// proxy and TLS-trust vars above: its sole purpose is to be consumed by
	// dependency resolution in builds, unlike the control-plane secrets this
	// allow-list blocks. Index credentials set here are server-wide and
	// necessarily visible to every build that uses them (builds execute
	// deployer-controlled code).
	"UV_DEFAULT_INDEX": {}, "UV_INDEX": {}, "UV_INDEX_URL": {}, "UV_EXTRA_INDEX_URL": {},
	"UV_INDEX_STRATEGY": {}, "UV_FIND_LINKS": {},
	"PIP_INDEX_URL": {}, "PIP_EXTRA_INDEX_URL": {},
	"R_LIBS": {}, "R_LIBS_USER": {}, "R_LIBS_SITE": {}, "RENV_PATHS_CACHE": {},
	"RENV_CONFIG_REPOS_OVERRIDE": {},
}

var appEnvAllowPrefixes = []string{"LC_"}

// isUvIndexCredential matches uv's per-named-index credential variables,
// UV_INDEX_{NAME}_USERNAME / UV_INDEX_{NAME}_PASSWORD, without admitting the
// whole UV_INDEX_ namespace: an unrelated server secret that merely shares
// the prefix (e.g. UV_INDEX_TOKEN) must stay blocked.
func isUvIndexCredential(name string) bool {
	rest, ok := strings.CutPrefix(name, "UV_INDEX_")
	if !ok || rest == "" {
		return false
	}
	return strings.HasSuffix(rest, "_USERNAME") || strings.HasSuffix(rest, "_PASSWORD")
}

// SanitizedEnv returns an allow-listed subset of the current process environment.
// It is the single source of truth for the env base of every app-controlled code
// path: app processes, dependency installation (uv/renv), and post-deploy hooks.
// Server secrets (SHINYHUB_AUTH_SECRET, the deploy token, OAuth/OIDC client
// secrets, and cloud credentials such as AWS_SECRET_ACCESS_KEY) must never reach
// code that a deployer can influence, so only known-safe variables pass through.
func SanitizedEnv() []string {
	extra := parseAppEnvAllow(os.Getenv("SHINYHUB_APP_ENV_ALLOW"))
	raw := os.Environ()
	filtered := make([]string, 0, len(raw))
	for _, e := range raw {
		name, _, ok := strings.Cut(e, "=")
		if !ok {
			continue
		}
		if appEnvVarAllowed(name, extra) {
			filtered = append(filtered, e)
		}
	}
	return filtered
}

// appEnvVarAllowed reports whether an environment variable name is safe to pass
// through to app-controlled code: on the exact allow-list, matching an allowed
// prefix, or explicitly permitted by the operator via SHINYHUB_APP_ENV_ALLOW.
func appEnvVarAllowed(name string, extra map[string]struct{}) bool {
	if _, ok := appEnvAllowExact[name]; ok {
		return true
	}
	if _, ok := extra[name]; ok {
		return true
	}
	for _, p := range appEnvAllowPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return isUvIndexCredential(name)
}

// parseAppEnvAllow parses the comma-separated SHINYHUB_APP_ENV_ALLOW operator
// extension into a set of exact variable names.
func parseAppEnvAllow(v string) map[string]struct{} {
	if v == "" {
		return nil
	}
	out := make(map[string]struct{})
	for _, name := range strings.Split(v, ",") {
		if name = strings.TrimSpace(name); name != "" {
			out[name] = struct{}{}
		}
	}
	return out
}

// filteredEnv is the package-internal alias for SanitizedEnv.
func filteredEnv() []string { return SanitizedEnv() }
