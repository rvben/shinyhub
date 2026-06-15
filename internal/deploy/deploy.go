package deploy

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rvben/shinyhub/internal/bundle"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
)

var portCounter atomic.Int64

func init() {
	portCounter.Store(20000)
}

// pythonSyncFn / rSyncFn are package-level indirections so tests can observe
// (or replace) host-side dependency installation. Production code always
// goes through process.Sync / process.SyncR.
var (
	pythonSyncFn    = process.Sync
	rSyncFn         = process.SyncR
	ensureProjectFn = process.EnsureProject
)

// autoInstrumentPackages is the overlay layered into a Python app's
// environment (uv run --with) when auto-instrumentation is on. distro wires
// the SDK and OTLP auto-configuration from the injected OTEL_* env;
// opentelemetry-instrument activates the installed instrumentors at startup.
// Coverage is the transport layer: inbound ASGI requests (Shiny for Python
// runs on Starlette) and outbound requests/httpx calls. The reactive graph is
// not a library boundary and is not covered; apps add manual spans for that
// (docs/tracing.md). The overlay never touches the app's venv or lockfile.
var autoInstrumentPackages = []string{
	"opentelemetry-distro",
	"opentelemetry-exporter-otlp",
	"opentelemetry-instrumentation-starlette",
	"opentelemetry-instrumentation-requests",
	"opentelemetry-instrumentation-httpx",
}

// buildCommandFn is a package-level indirection so tests can observe the
// auto-instrument decision and substitute runnable commands.
var buildCommandFn = buildCommand

// AllocatePort returns an unused TCP port in the 20000–60000 range.
//
// Each candidate is verified with a short-lived bind on 127.0.0.1 so we never
// hand back a port already held by a survivor process from a prior shinyhub
// run (the counter resets to 20000 on every startup; without the probe a
// restart could happily re-issue an in-use port and the spawned app would
// bind-fail). On range exhaustion the counter wraps; if no probed port in
// the range is bindable within maxAllocateProbes attempts, the OS is asked
// for any free port via :0.
func AllocatePort() int {
	for attempt := 0; attempt < maxAllocateProbes; attempt++ {
		p := portCounter.Add(1)
		if p > 60000 {
			// Another goroutine may have already reset; use CompareAndSwap to
			// let exactly one resetter win and avoid a thundering-herd reset.
			portCounter.CompareAndSwap(p, 20000)
			continue
		}
		if portIsBindable(int(p)) {
			return int(p)
		}
	}
	// Range fully saturated or every probe lost a race: defer to the kernel.
	if p := osAssignedPort(); p > 0 {
		return p
	}
	// Last resort: surface whatever the counter is on so the spawned process
	// fails loudly instead of this loop spinning forever.
	return int(portCounter.Load())
}

// maxAllocateProbes caps AllocatePort's bind-probe loop. The 20000–60000
// range holds 40 000 candidate ports, so 1 000 attempts is generous for any
// realistic deploy load and bounds the worst-case latency.
const maxAllocateProbes = 1000

// portIsBindable returns true if a TCP listener can be opened on
// 127.0.0.1:port right now. The probe listener is closed immediately; the
// caller is responsible for reserving the port via the actual app process
// before another concurrent allocation re-probes the same value.
func portIsBindable(port int) bool {
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	_ = l.Close()
	return true
}

// osAssignedPort asks the kernel for any free TCP port on 127.0.0.1.
// Returns 0 if even that fails (host is out of ephemeral ports).
func osAssignedPort() int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0
	}
	defer l.Close()
	addr, ok := l.Addr().(*net.TCPAddr)
	if !ok {
		return 0
	}
	return addr.Port
}

// Params controls a deploy operation.
type Params struct {
	Slug string
	// AppID is the owning app's numeric DB id, threaded onto each replica's
	// StartParams so a runtime can namespace per-app external resources (e.g.
	// Fargate secret store names and task-def families) without a slug collision
	// across a delete-then-recreate. Zero is allowed for local-only deploys.
	AppID     int64
	BundleDir string
	// Command overrides auto-detection. If empty, the app type is detected from
	// the bundle and the appropriate runtime command is built per replica.
	Command  []string
	Env      []string
	Workers  int
	Replicas int // 0 → 1 (single-replica fallback); also the fallback total when Placement is empty
	// Placement maps tier name → replica count. Empty means "all Replicas on the
	// default tier", reproducing single-tier behavior. When set, the sum of its
	// counts is the authoritative replica total and Replicas is ignored.
	Placement map[string]int
	// TierOrder is the config-declared tier order used to lay out placement
	// counts deterministically over a single global replica index space. Empty
	// is treated as just the default tier.
	TierOrder []string
	// DefaultTier is the tier a replica runs under when Placement is empty or a
	// tier is otherwise unresolved. Empty falls back to process.DefaultTier.
	DefaultTier     string
	Manager         *process.Manager
	Proxy           *proxy.Proxy
	HealthTimeout   time.Duration // 0 means the 120 s default
	MemoryLimitMB   int           // 0 = no limit
	CPUQuotaPercent int           // 0 = no limit; 100 = 1 full core
	// MaxSessionsPerReplica caps the per-replica active connection count the
	// proxy will route cookie-less requests to; saturated pools shed with
	// 503 + Retry-After. 0 = unlimited (caller should resolve the runtime
	// default before calling).
	MaxSessionsPerReplica int
	// IdentityHeaders is the app's resolved effective identity-forwarding
	// flag (ResolveIdentityHeaders over the app column + global config).
	// Run pushes it to the proxy pool alongside the session cap.
	IdentityHeaders bool
	// HealthCheck is called after each replica starts to verify it is ready.
	// It receives the runtime-returned endpoint URL (e.g. http://127.0.0.1:PORT).
	// If nil, the default HTTP health poller (waitHealthy) is used.
	HealthCheck func(endpointURL string, timeout time.Duration, transport http.RoundTripper) error
	// ContentDigest, DeploymentID, and AppVersion travel with the launch so a
	// remote runtime can pull-by-digest and stamp recovery labels. Empty/zero is
	// allowed for local-only deploys but every API launch path now populates them.
	ContentDigest string
	DeploymentID  int64
	AppVersion    string
	// ColocateWorkers pins every replica of this pool to one of the named worker
	// node ids, overriding least-loaded placement. The control plane sets it for
	// a shared-mount consumer so each replica lands on a worker that also hosts
	// its source's provisioned data. Empty means unconstrained placement. Only
	// worker-routing tiers honor it; a tier whose runtime ignores TargetWorker
	// (the native local tier) is unaffected.
	ColocateWorkers []string
}

// Result contains identifiers for a single successfully deployed replica.
type Result struct {
	Index       int
	PID         int
	Port        int
	EndpointURL string
	Tier        string
	Provider    string
	WorkerID    string
}

// PoolResult contains the full set of replicas that were successfully booted.
// Failed lists the indices whose boot failed in a partial-success deploy, so
// the caller can persist them as crashed and let the watcher reconcile the
// pool back up to the desired replica count.
type PoolResult struct {
	Replicas []Result
	Failed   []int
	// HooksSkipped counts post-deploy hooks declared in the manifest that were
	// not run because the runtime prepares dependencies inside a container
	// (the host has no view of the app's environment). 0 when hooks ran or none
	// were declared. The API relays this so the developer learns their hooks
	// did not execute instead of finding out only from the server log.
	HooksSkipped int
}

// distinctTiers returns the unique tiers present in an assignment set, in first
// appearance order.
func distinctTiers(asn []process.TierAssignment) []string {
	seen := make(map[string]struct{}, len(asn))
	var out []string
	for _, a := range asn {
		if _, ok := seen[a.Tier]; ok {
			continue
		}
		seen[a.Tier] = struct{}{}
		out = append(out, a.Tier)
	}
	return out
}

// effectiveDefaultTier returns the tier a replica runs under when placement is
// empty or a tier is unresolved.
func (p Params) effectiveDefaultTier() string {
	if p.DefaultTier != "" {
		return p.DefaultTier
	}
	return process.DefaultTier
}

// assignments expands this deploy's placement into deterministic (index, tier)
// assignments over a single global index space. An empty placement assigns
// max(Replicas, 1) replicas to the default tier, reproducing single-tier
// behavior exactly.
func (p Params) assignments() ([]process.TierAssignment, error) {
	fallback := p.Replicas
	if fallback <= 0 {
		fallback = 1
	}
	return process.ExpandPlacement(p.Placement, p.TierOrder, fallback, p.effectiveDefaultTier())
}

// tierForIndex returns the tier assigned to the given global replica index.
// Indices outside the expanded assignment set (e.g. a watchdog restarting a
// replica beyond the current placement total) fall back to the default tier so
// recovery never wedges.
func (p Params) tierForIndex(index int) string {
	asn, err := p.assignments()
	if err != nil {
		return p.effectiveDefaultTier()
	}
	for _, a := range asn {
		if a.Index == index {
			return a.Tier
		}
	}
	return p.effectiveDefaultTier()
}

// hostPreparesDeps reports whether host-side dependency installation should run
// for a boot touching the given tiers. It returns true if the Manager is nil
// (test/no-runtime path) or if any of the named tiers prepares deps on the
// host: the bundle's host venv is shared, so a single host sync serves every
// native replica while container replicas ignore it.
func (p Params) hostPreparesDeps(tiers ...string) bool {
	if p.Manager == nil {
		return true
	}
	for _, t := range tiers {
		if p.Manager.HostPreparesDepsFor(t) {
			return true
		}
	}
	return false
}

// resolveBootParams resolves Command defaults, HealthCheck defaults, and
// HealthTimeout defaults for a pool/replica boot. hostDeps reports whether
// host-side dependency installation should run (false under container-only
// tiers). Returns the resolved base command, detected app type, the effective
// auto-instrumentation setting (meaningful only for inferred-command Python
// boots), the effective health-check func, and the effective timeout.
//
// Command resolution order:
//  1. Params.Command (API-supplied override) — skip manifest and type detection.
//  2. shinyhub.toml [app] command — manifest absent = nil manifest, inferred path;
//     manifest present but unparseable = fatal (deploys already reject bad manifests,
//     so an unreadable on-disk one is a pre-validation bundle or a hand-edit, and
//     silently ignoring a declared command could boot the wrong server).
//  3. Type detection (app.py / app.R) — falls through when no manifest command.
func resolveBootParams(p Params, hostDeps bool) (baseCmd []string, appType string, autoInstrument bool, hc func(string, time.Duration, http.RoundTripper) error, timeout time.Duration, err error) {
	if len(p.Command) > 0 {
		baseCmd = p.Command
	} else {
		// One manifest load per boot, shared by the command override and the
		// [tracing] auto override. File absent = nil manifest (inferred
		// path, fleet defaults). Present but unparseable = fatal: deploys
		// already reject bad manifests, so an unreadable on-disk manifest is
		// a pre-validation bundle or a hand-edit, and silently ignoring a
		// declared command could boot the wrong server.
		m, merr := LoadManifest(p.BundleDir)
		if merr != nil {
			return nil, "", false, nil, 0, fmt.Errorf("read manifest: %w", merr)
		}
		if m != nil && len(m.App.Command) > 0 {
			// Boot-time re-validation covers rollbacks to bundles deployed
			// before stricter rules. The template is stored UNSUBSTITUTED:
			// {port} is per-replica and substituted in bootReplicaAttempt.
			if verr := validateCommandTemplate(m.App.Command); verr != nil {
				return nil, "", false, nil, 0, fmt.Errorf("manifest [app] command: %w", verr)
			}
			baseCmd = m.App.Command
		} else {
			appType = DetectAppType(p.BundleDir)
			// Container runtimes prepare dependencies inside the image/container, so
			// running uv sync / renv::restore on the host would leak host state into
			// what is supposed to be an isolated boot path (and fail outright on
			// hosts where uv/Rscript aren't installed). hostDeps is resolved by the
			// caller from the tiers this boot touches.
			switch appType {
			case "python":
				if hostDeps {
					// Convert a requirements.txt-only app into a uv project on first
					// prep so it locks via the native uv.lock - one reproducibility
					// mechanism for every Python app. Best-effort: a failed
					// conversion cleans itself up and the app falls back to
					// requirements mode, never blocking the boot.
					if cerr := ensureProjectFn(p.BundleDir); cerr != nil {
						slog.Warn("deploy: project conversion failed; using requirements.txt",
							"slug", p.Slug, "err", cerr)
					}
					if err = pythonSyncFn(p.BundleDir); err != nil {
						return nil, "", false, nil, 0, fmt.Errorf("uv sync: %w", err)
					}
				}
				// Only inferred-command Python boots resolve auto-instrumentation:
				// opentelemetry-instrument is Python-only, and user-supplied
				// commands are never rewritten.
				autoInstrument = resolveAutoInstrument(p, m)
			case "r":
				if hostDeps {
					if err = rSyncFn(p.BundleDir); err != nil {
						return nil, "", false, nil, 0, fmt.Errorf("renv restore: %w", err)
					}
				}
			default:
				return nil, "", false, nil, 0, fmt.Errorf("no app.py or app.R found in %s (add one, or declare [app] command in shinyhub.toml)", p.BundleDir)
			}
		}
		// baseCmd remains nil for inferred-command boots — bootReplica constructs
		// the per-replica command using the real port once it is allocated.
	}

	hc = p.HealthCheck
	if hc == nil {
		hc = waitHealthy
	}
	timeout = p.HealthTimeout
	if timeout == 0 {
		timeout = 120 * time.Second
	}
	return baseCmd, appType, autoInstrument, hc, timeout, nil
}

// resolveAutoInstrument resolves the effective auto-instrumentation setting
// for a boot: the fleet default carried on the Manager, overridden in either
// direction by the bundle manifest's [tracing] auto. The caller supplies the
// already-loaded manifest (nil when the bundle has none).
func resolveAutoInstrument(p Params, m *Manifest) bool {
	auto := false
	if p.Manager != nil {
		auto = p.Manager.AutoInstrumentAppsDefault()
	}
	if m != nil && m.Tracing.Auto != nil {
		auto = *m.Tracing.Auto
	}
	return auto
}

// Run orchestrates a parallel pool deploy: spawns N replicas concurrently,
// health-checks each, and registers surviving replicas with the reverse proxy.
// Partial failure (some replicas healthy, some not) is accepted and logged.
// All-fail returns an error.
func Run(p Params) (*PoolResult, error) {
	asn, err := p.assignments()
	if err != nil {
		return nil, fmt.Errorf("expand placement: %w", err)
	}
	total := len(asn)

	p.Proxy.SetPoolSize(p.Slug, total)
	p.Proxy.SetPoolCap(p.Slug, p.MaxSessionsPerReplica)
	p.Proxy.SetPoolAppID(p.Slug, p.AppID)
	p.Proxy.SetPoolIdentityHeaders(p.Slug, p.IdentityHeaders)

	// Host-side dep prep and post-deploy hooks are pool-wide: run them once if
	// any assigned tier prepares deps on the host.
	hostDeps := p.hostPreparesDeps(distinctTiers(asn)...)

	baseCmd, appType, autoInstrument, hc, timeout, err := resolveBootParams(p, hostDeps)
	if err != nil {
		return nil, err
	}

	hooksSkipped, err := runManifestPostDeployHooks(p, hostDeps)
	if err != nil {
		return nil, err
	}

	// Pre-plan each replica's target worker before launching the pool so a
	// concurrent boot spreads across a tier's workers. Planning per-replica
	// inside the goroutines would have every replica read the same pre-deploy
	// load snapshot and stack onto the lowest-loaded worker; planning the whole
	// batch up front folds each assignment into the next pick.
	targets := planPoolWorkers(p, asn)

	type bootResult struct {
		idx int
		res Result
		err error
	}
	results := make(chan bootResult, total)
	var wg sync.WaitGroup

	for _, a := range asn {
		wg.Add(1)
		go func(a process.TierAssignment) {
			defer wg.Done()
			r, err := bootReplica(p, a.Index, a.Tier, targets[a.Index], baseCmd, appType, autoInstrument, hc, timeout)
			results <- bootResult{idx: a.Index, res: r, err: err}
		}(a)
	}
	wg.Wait()
	close(results)

	ok := make([]Result, 0, total)
	var failed []int
	var bootErrs []error
	for br := range results {
		if br.err != nil {
			bootErrs = append(bootErrs, fmt.Errorf("replica %d: %w", br.idx, br.err))
			failed = append(failed, br.idx)
			continue
		}
		ok = append(ok, br.res)
	}
	sort.Slice(ok, func(a, b int) bool { return ok[a].Index < ok[b].Index })
	sort.Ints(failed)

	if len(ok) == 0 {
		return nil, fmt.Errorf("all replicas failed health check: %w", errors.Join(bootErrs...))
	}
	for _, e := range bootErrs {
		slog.Warn("replica boot failed", "slug", p.Slug, "err", e)
	}
	return &PoolResult{Replicas: ok, Failed: failed, HooksSkipped: hooksSkipped}, nil
}

// runManifestPostDeployHooks loads shinyhub.toml from the bundle and runs any
// post-deploy hooks before the replicas start. Hooks run only when the
// runtime prepares dependencies on the host: in docker mode, dependency
// installation happens inside the container and the host has no view of the
// app's venv, so running hooks here would observe the wrong environment.
// Docker-runtime users should bake setup steps into their image entrypoint
// instead.
//
// Hook output is written to a per-app deploy log under the bundle dir
// (./deploy-hooks.log) so operators can inspect what ran without needing
// the parent process's stdout. A best-effort tail is also slog-emitted on
// failure to make the cause obvious in the server log.
// runManifestPostDeployHooks returns the number of declared hooks it skipped
// (non-zero only under a container runtime) so the caller can surface it to the
// developer; a returned error means an executed hook failed.
func runManifestPostDeployHooks(p Params, hostDeps bool) (int, error) {
	manifest, err := LoadManifest(p.BundleDir)
	if err != nil {
		return 0, err
	}
	hooks := manifest.PostDeploy()
	if len(hooks) == 0 {
		return 0, nil
	}
	if !hostDeps {
		slog.Warn("skipping post-deploy hooks under non-host runtime; bake them into the image entrypoint instead",
			"slug", p.Slug, "hooks", len(hooks))
		return len(hooks), nil
	}

	logPath := filepath.Join(p.BundleDir, "deploy-hooks.log")
	logFile, ferr := os.Create(logPath)
	if ferr != nil {
		return 0, fmt.Errorf("create %s: %w", logPath, ferr)
	}
	defer logFile.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := RunPostDeployHooks(ctx, p.BundleDir, hooks, p.Env, logFile); err != nil {
		slog.Warn("post-deploy hook failed", "slug", p.Slug, "log", logPath, "err", err)
		return 0, err
	}
	return 0, nil
}

// planPoolWorkers maps each assignment's replica index to the worker the
// Manager planned for it, grouping the pool by tier (in first-appearance order)
// so each tier's batch is planned together and spreads across that tier's
// workers. Tiers whose runtime does not route to workers (the native local
// tier) return no targets and their replicas self-place, which is a no-op for a
// runtime that ignores TargetWorker. A nil Manager (test/no-runtime path) yields
// no targets.
func planPoolWorkers(p Params, asn []process.TierAssignment) map[int]string {
	out := map[int]string{}
	if p.Manager == nil {
		return out
	}
	byTier := map[string][]int{}
	var tierOrder []string
	for _, a := range asn {
		if _, seen := byTier[a.Tier]; !seen {
			tierOrder = append(tierOrder, a.Tier)
		}
		byTier[a.Tier] = append(byTier[a.Tier], a.Index)
	}
	for _, tier := range tierOrder {
		indices := byTier[tier]
		// A shared-mount consumer is confined to the workers hosting its source
		// data; pin its replicas across that set (round-robin) instead of letting
		// the runtime spread them least-loaded across the whole tier. Tiers whose
		// runtime ignores TargetWorker (native local) drop the pin harmlessly.
		if len(p.ColocateWorkers) > 0 {
			for i, idx := range indices {
				out[idx] = p.ColocateWorkers[i%len(p.ColocateWorkers)]
			}
			continue
		}
		nodes := p.Manager.PlanPlacement(tier, p.Slug, len(indices))
		for i, idx := range indices {
			if i < len(nodes) {
				out[idx] = nodes[i]
			}
		}
	}
	return out
}

// bootReplica starts a single replica, retrying once without
// auto-instrumentation if the instrumented launch fails to start or pass its
// health check. A bad overlay (dependency conflict with the app's pins, a
// broken instrumentor) is an observability regression, not an outage: the uv
// resolution error is visible in the app's own log, and the fallback is
// flagged in the server log.
func bootReplica(p Params, idx int, tier, targetWorker string, baseCmd []string, appType string, autoInstrument bool, hc func(string, time.Duration, http.RoundTripper) error, timeout time.Duration) (Result, error) {
	instrumented := autoInstrument && baseCmd == nil && appType == "python"
	res, err := bootReplicaAttempt(p, idx, tier, targetWorker, baseCmd, appType, instrumented, hc, timeout)
	if err != nil && instrumented {
		slog.Warn("deploy: instrumented launch failed; retrying without auto-instrumentation",
			"slug", p.Slug, "index", idx, "err", err)
		res, err = bootReplicaAttempt(p, idx, tier, targetWorker, baseCmd, appType, false, hc, timeout)
	}
	return res, err
}

// bootReplicaAttempt starts a single replica: allocates a port, starts the
// process, health-checks it, and registers it with the proxy. baseCmd == nil
// signals that the command should be built from appType using the allocated
// port; instrument wraps that inferred command in the OTEL overlay.
// targetWorker pins the replica to a pre-planned worker (empty means the runtime
// self-places, e.g. a single-replica watchdog restart or a worker-agnostic tier).
func bootReplicaAttempt(p Params, idx int, tier, targetWorker string, baseCmd []string, appType string, instrument bool, hc func(string, time.Duration, http.RoundTripper) error, timeout time.Duration) (Result, error) {
	port := AllocatePort()

	var cmd []string
	bindHost := "127.0.0.1"
	if p.Manager != nil {
		bindHost = p.Manager.AppBindHostFor(tier)
	}
	if baseCmd != nil {
		// Per-replica substitution on a FRESH slice: the template is shared
		// across replica goroutines and each replica gets its own port.
		// A no-placeholder command (e.g. an API-supplied Params.Command)
		// passes through unchanged but still gets its own copy.
		cmd = substituteCommand(baseCmd, port, bindHost)
	} else {
		switch appType {
		case "python":
			workers := p.Workers
			if workers <= 0 {
				workers = 1
			}
			// hostDeps gates project mode for a SYNTHESIZED project: a
			// container/worker replica gets the bundle but not this host's synced
			// .venv, so it falls back to requirements.txt. An author-shipped
			// pyproject is project mode regardless.
			cmd = buildCommandFn(p.BundleDir, port, workers, bindHost, instrument, p.hostPreparesDeps(tier))
		case "r":
			cmd = BuildRCommand(p.BundleDir, port, bindHost)
		default:
			return Result{}, fmt.Errorf("no app.py or app.R found in %s", p.BundleDir)
		}
	}

	env := append(append([]string{}, p.Env...), fmt.Sprintf("PORT=%d", port))

	info, err := p.Manager.Start(process.StartParams{
		Slug:            p.Slug,
		AppID:           p.AppID,
		Index:           idx,
		Tier:            tier,
		Dir:             p.BundleDir,
		Command:         cmd,
		Port:            port,
		Env:             env,
		MemoryLimitMB:   p.MemoryLimitMB,
		CPUQuotaPercent: p.CPUQuotaPercent,
		AppVersion:      p.AppVersion,
		DeploymentID:    p.DeploymentID,
		ContentDigest:   p.ContentDigest,
		TargetWorker:    targetWorker,
		MaxSessions:     p.MaxSessionsPerReplica,
	})
	if err != nil {
		return Result{}, fmt.Errorf("start: %w", err)
	}

	// Resolve the route transport for the worker Start actually placed on, so a
	// replica on any of a tier's workers is dialed with that worker's mTLS
	// transport. Empty for the local tier (no remote transport).
	transport := p.Manager.TransportForWorker(tier, info.WorkerID)

	if err := hc(info.EndpointURL, timeout, transport); err != nil {
		if serr := p.Manager.StopReplica(p.Slug, idx); serr != nil {
			slog.Warn("deploy: stop replica after failed health check", "slug", p.Slug, "index", idx, "err", serr)
		}
		return Result{}, fmt.Errorf("health: %w", err)
	}

	if err := p.Proxy.RegisterReplica(p.Slug, idx, info.EndpointURL, transport, p.DeploymentID); err != nil {
		if serr := p.Manager.StopReplica(p.Slug, idx); serr != nil {
			slog.Warn("deploy: stop replica after failed proxy register", "slug", p.Slug, "index", idx, "err", serr)
		}
		return Result{}, fmt.Errorf("register: %w", err)
	}
	return Result{
		Index:       idx,
		PID:         info.PID,
		Port:        port,
		EndpointURL: info.EndpointURL,
		Tier:        info.Tier,
		Provider:    info.Provider,
		WorkerID:    info.WorkerID,
	}, nil
}

// RunReplica boots a single replica at the given index. The proxy pool size
// must already be set to at least index+1 before calling this function.
// Used by the watchdog's per-replica crash-restart path.
func RunReplica(p Params, index int) (*Result, error) {
	tier := p.tierForIndex(index)
	baseCmd, appType, autoInstrument, hc, timeout, err := resolveBootParams(p, p.hostPreparesDeps(tier))
	if err != nil {
		return nil, err
	}
	// A shared-mount consumer is confined to the workers hosting its source data,
	// so a restarted replica must pin to one of them; index-keyed selection keeps
	// the choice deterministic and spreads restarts across the set. Without a
	// colocation pin the restart self-places against live load (empty target), so
	// the runtime picks the least-loaded worker.
	target := ""
	if len(p.ColocateWorkers) > 0 {
		target = p.ColocateWorkers[index%len(p.ColocateWorkers)]
	}
	r, err := bootReplica(p, index, tier, target, baseCmd, appType, autoInstrument, hc, timeout)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// resumeProbeTimeout bounds the abbreviated readiness probe after a resume. A
// resumed process may briefly fault its working set back from swap/zram; a short
// check avoids routing into a fault storm without paying the full cold-boot
// health timeout.
const resumeProbeTimeout = 15 * time.Second

// ResumeReplica restores a single suspended replica via the Manager's Snapshotter
// path, runs an abbreviated readiness probe, and registers the route. It mirrors
// RunReplica's post-start steps (health check + proxy register) but skips the
// cold boot and dependency prep. It returns a wrapped sentinel
// (ErrRuntimeNotSnapshotter / ErrReplicaNotSuspended / ErrReplicaNotFound) when
// the slot cannot be resumed, so the caller falls back to RunReplica.
func ResumeReplica(p Params, index int) (*Result, error) {
	ep, err := p.Manager.Resume(p.Slug, index)
	if err != nil {
		return nil, err
	}
	// Prefer the tier the replica actually runs on (from the live entry) over the
	// placement-derived tier, so the route transport matches the resumed worker.
	tier := p.tierForIndex(index)
	port := 0
	if info, ok := p.Manager.GetReplica(p.Slug, index); ok {
		if info.Tier != "" {
			tier = info.Tier
		}
		port = info.Port
	}
	transport := p.Manager.TransportForWorker(tier, ep.WorkerID)

	hc := p.HealthCheck
	if hc == nil {
		hc = waitHealthy
	}
	if err := hc(ep.URL, resumeProbeTimeout, transport); err != nil {
		return nil, fmt.Errorf("resume readiness: %w", err)
	}
	if err := p.Proxy.RegisterReplica(p.Slug, index, ep.URL, transport, p.DeploymentID); err != nil {
		return nil, fmt.Errorf("register: %w", err)
	}
	return &Result{
		Index:       index,
		PID:         ep.Handle.PID,
		Port:        port,
		EndpointURL: ep.URL,
		Tier:        tier,
		Provider:    ep.Provider,
		WorkerID:    ep.WorkerID,
	}, nil
}

// DetectAppType returns "python" if app.py exists, "r" if app.R exists, or ""
// if neither is found.
func DetectAppType(bundleDir string) string {
	if _, err := os.Stat(filepath.Join(bundleDir, "app.py")); err == nil {
		return "python"
	}
	if _, err := os.Stat(filepath.Join(bundleDir, "app.R")); err == nil {
		return "r"
	}
	return ""
}

// BuildRCommand returns the command to start an R Shiny app on the given port.
// bindHost is the address the app listens on inside its execution environment
// (the host for native, the container for Docker bridge mode).
func BuildRCommand(bundleDir string, port int, bindHost string) []string {
	expr := fmt.Sprintf(
		`shiny::runApp('.', host='%s', port=%d, launch.browser=FALSE)`, bindHost, port)
	return []string{"Rscript", "--vanilla", "-e", expr}
}

// useProjectMode reports whether to launch in uv project mode. An author-shipped
// pyproject.toml is project mode everywhere (it ships with the bundle). A
// pyproject SYNTHESIZED from requirements by this host is project mode only where
// this host prepared the deps and synced the .venv - a container/worker replica
// gets the bundle but not the .venv, so it falls back to requirements.txt.
func useProjectMode(bundleDir string, hostDeps bool) bool {
	if _, err := os.Stat(filepath.Join(bundleDir, "pyproject.toml")); err != nil {
		return false
	}
	if process.IsSynthesizedProject(bundleDir) {
		return hostDeps
	}
	return true
}

// buildCommand constructs the uv launch command for a bundle directory.
// In project mode (a pyproject.toml, author-shipped or synthesized by
// EnsureProject) uv sync has prepared a locked .venv and we use plain `uv run`.
// Otherwise we pass --with-requirements so uv installs deps into an ephemeral
// environment. When autoInstrument is set, the OTEL overlay is layered in via
// --with and the entrypoint is wrapped with opentelemetry-instrument; the app's
// own environment is never modified. bindHost has the same meaning as in
// BuildRCommand. hostDeps gates project mode for a synthesized project (see
// useProjectMode).
func buildCommand(bundleDir string, port, workers int, bindHost string, autoInstrument, hostDeps bool) []string {
	base := []string{"uv", "run", "--no-project"}
	if useProjectMode(bundleDir, hostDeps) {
		base = []string{"uv", "run"}
	} else if _, err := os.Stat(filepath.Join(bundleDir, "requirements.txt")); err == nil {
		base = append(base, "--with-requirements", "requirements.txt")
	}
	if autoInstrument {
		for _, pkg := range autoInstrumentPackages {
			base = append(base, "--with", pkg)
		}
		base = append(base, "opentelemetry-instrument")
	}
	return append(base,
		"shiny", "run", "app.py",
		"--host", bindHost,
		"--port", fmt.Sprintf("%d", port),
	)
}

// waitHealthy polls the app's root endpoint until it responds with a non-5xx
// status or the deadline is exceeded. Each HTTP attempt is capped at 5 seconds.
// When transport is non-nil it is installed on the client (required for mTLS endpoints).
func waitHealthy(endpointURL string, timeout time.Duration, transport http.RoundTripper) error {
	client := &http.Client{Timeout: 5 * time.Second}
	if transport != nil {
		client.Transport = transport
	}
	healthURL := strings.TrimSuffix(endpointURL, "/") + "/"
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithDeadline(context.Background(), deadline)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
		if err != nil {
			cancel()
			return fmt.Errorf("build request for %s: %w", healthURL, err)
		}
		resp, err := client.Do(req)
		cancel()
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode < 500 {
				return nil
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("app at %s did not become healthy within %s", endpointURL, timeout)
}

// ErrBundleTooLarge is returned by ExtractBundle when a single entry, or the
// combined size of all entries, exceeds the configured limits. Zip-bomb
// protection: uncompressed sizes in the zip header are attacker-controlled, so
// we also enforce the caps while streaming bytes to disk.
var ErrBundleTooLarge = errors.New("bundle exceeds extracted size limit")

// ErrBundleRejected is returned by ExtractBundle when a bundle entry violates
// the content policy (data dirs, forbidden extensions, etc.). Callers can use
// errors.Is to map this to a 422 Unprocessable Entity response.
var ErrBundleRejected = errors.New("bundle rejected")

const (
	// DefaultMaxEntrySize caps the extracted size of a single file inside the
	// bundle. Matches the upload size cap — a single file can never be larger
	// than the full archive.
	DefaultMaxEntrySize int64 = 128 << 20
	// DefaultMaxBundleSize caps the combined extracted size of all entries.
	DefaultMaxBundleSize int64 = 512 << 20
	// maxBundleEntries caps the number of entries in a bundle. The size caps
	// bound total bytes but not entry count; a bundle of hundreds of thousands
	// of tiny entries would still explode inodes on the destination filesystem.
	maxBundleEntries = 10000
)

// safeFileMode strips group/other write bits and any setuid/setgid/sticky bits
// from a zip-declared file mode, preserving only the read/execute intent.
// Extracted bundle files are never group- or world-writable.
func safeFileMode(m os.FileMode) os.FileMode {
	if m.Perm()&0o100 != 0 {
		return 0o755
	}
	return 0o644
}

// ExtractBundle unzips src into destDir with the default size limits.
func ExtractBundle(src, destDir string) error {
	return ExtractBundleWithLimits(src, destDir, DefaultMaxEntrySize, DefaultMaxBundleSize)
}

// ExtractBundleWithLimits unzips src into destDir, rejecting any entry whose
// resolved path would escape destDir (zip-slip protection) and enforcing both
// a per-entry and aggregate size cap (zip-bomb protection). A zero or negative
// limit means unlimited.
func ExtractBundleWithLimits(src, destDir string, maxEntrySize, maxTotalSize int64) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}
	defer r.Close()

	if len(r.File) > maxBundleEntries {
		return fmt.Errorf("%w: bundle has %d entries (limit %d)", ErrBundleTooLarge, len(r.File), maxBundleEntries)
	}

	if err := os.MkdirAll(destDir, 0755); err != nil {
		return err
	}

	// Resolve destDir to its real absolute path once so comparisons are stable.
	absDestDir, err := filepath.Abs(destDir)
	if err != nil {
		return err
	}

	rules := bundle.DefaultRules()

	var total int64
	for _, f := range r.File {
		// filepath.Join cleans the path, which resolves any ".." components.
		target := filepath.Join(absDestDir, filepath.Clean(f.Name))

		// Verify the resolved path is still inside destDir.
		// filepath.Rel returns a path starting with ".." when target is outside
		// absDestDir. The separator-aware check catches both ".." and "../foo".
		rel, err := filepath.Rel(absDestDir, target)
		if err != nil || strings.HasPrefix(rel, "..") {
			return fmt.Errorf("zip-slip detected in %q: entry escapes destination", f.Name)
		}

		// Reject symlink entries outright: a bundle is application code, not a
		// place for links. Even though the path check above already blocks
		// traversal via the entry name, a materialized symlink could later be
		// followed to escape the bundle dir.
		if f.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: bundle entry %q is a symlink; symlinks are not allowed", ErrBundleRejected, f.Name)
		}

		// Apply bundle filter rules before any disk side effects. Cache dirs are
		// silently skipped; data dirs and disallowed extensions are hard errors.
		decision := rules.Inspect(f.Name, int64(f.UncompressedSize64))
		switch decision {
		case bundle.FilterAccept:
			// proceed with extraction
		case bundle.FilterSkipCacheDir:
			continue
		case bundle.FilterRejectDataDir,
			bundle.FilterRejectDatasetDir,
			bundle.FilterRejectExtension,
			bundle.FilterRejectFileSize:
			return fmt.Errorf("%w: bundle entry %q: %s", ErrBundleRejected, f.Name, decision)
		default:
			return fmt.Errorf("bundle entry %q: unhandled filter decision %v", f.Name, decision)
		}

		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
			continue
		}

		// Trust-but-verify: reject up front when the declared size is already
		// over budget so we avoid any extraction work for obviously malicious
		// archives.
		if maxEntrySize > 0 && int64(f.UncompressedSize64) > maxEntrySize {
			return fmt.Errorf("%w: %q declared %d bytes", ErrBundleTooLarge, f.Name, f.UncompressedSize64)
		}

		written, err := extractFile(f, target, maxEntrySize)
		if err != nil {
			return err
		}
		total += written
		if maxTotalSize > 0 && total > maxTotalSize {
			return fmt.Errorf("%w: extracted %d bytes exceeds %d", ErrBundleTooLarge, total, maxTotalSize)
		}
	}
	return nil
}

// extractFile streams f into dest, capped at maxEntrySize bytes. Returns the
// number of bytes written. If the entry produces more bytes than the cap, the
// copy is aborted and ErrBundleTooLarge is returned; the partially-written
// file is removed so caller cleanup logic isn't needed.
func extractFile(f *zip.File, dest string, maxEntrySize int64) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return 0, err
	}
	rc, err := f.Open()
	if err != nil {
		return 0, err
	}
	defer rc.Close()
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, safeFileMode(f.Mode()))
	if err != nil {
		return 0, err
	}

	var src io.Reader = rc
	if maxEntrySize > 0 {
		// Read one extra byte so we can detect an overflow.
		src = io.LimitReader(rc, maxEntrySize+1)
	}
	n, copyErr := io.Copy(out, src)
	closeErr := out.Close()
	if copyErr != nil {
		os.Remove(dest)
		return 0, copyErr
	}
	if closeErr != nil {
		os.Remove(dest)
		return 0, closeErr
	}
	if maxEntrySize > 0 && n > maxEntrySize {
		os.Remove(dest)
		return 0, fmt.Errorf("%w: %q expanded past %d bytes", ErrBundleTooLarge, f.Name, maxEntrySize)
	}
	return n, nil
}

// ResolveMemoryLimitMB returns perAppMB if non-nil, otherwise defaultMB.
// Zero means no limit in both cases.
func ResolveMemoryLimitMB(perAppMB *int, defaultMB int) int {
	if perAppMB != nil {
		return *perAppMB
	}
	return defaultMB
}

// ResolveCPUQuotaPercent returns perAppPct if non-nil, otherwise defaultPct.
// Zero means no limit in both cases.
func ResolveCPUQuotaPercent(perAppPct *int, defaultPct int) int {
	if perAppPct != nil {
		return *perAppPct
	}
	return defaultPct
}

// ResolveIdentityHeaders resolves an app's effective identity-forwarding
// flag: the global config false is a hard kill switch a manifest cannot
// override; otherwise the per-app column applies (nil = inherit = on).
func ResolveIdentityHeaders(col *bool, globalEnabled bool) bool {
	return globalEnabled && (col == nil || *col)
}

// ResolveMaxSessionsPerReplica returns perApp if non-zero, otherwise defaultVal.
// Unlike the memory/CPU helpers, perApp is a plain int because the DB column is
// NOT NULL DEFAULT 0 and 0 explicitly means "fall back to the runtime default".
func ResolveMaxSessionsPerReplica(perApp, defaultVal int) int {
	if perApp > 0 {
		return perApp
	}
	return defaultVal
}
