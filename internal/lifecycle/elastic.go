package lifecycle

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
)

// defaultElasticHealthTimeout is the readiness deadline allowed for a
// demand-spawned elastic worker. 60 seconds is generous for a pre-built venv
// (no uv sync here) while keeping the loading-page wait bounded.
const defaultElasticHealthTimeout = 60 * time.Second

// ElasticSpawner holds the dependencies for spawning and terminating elastic
// workers on demand. Construct once in main.go and wire its methods into the
// proxy callbacks:
//
//	spawner := &lifecycle.ElasticSpawner{...}
//	prx.SetSpawnFunc(func(slug string, slotID int) { go spawner.Spawn(slug, slotID) })
//	prx.SetTerminateFunc(spawner.Terminate)
type ElasticSpawner struct {
	Store      *db.Store
	Manager    *process.Manager
	Proxy      *proxy.Proxy
	RuntimeCfg config.RuntimeConfig

	// HealthCheck is an optional override for the per-worker readiness probe.
	// When nil the default HTTP poller (waitElasticHealthy) is used.
	// Set in tests to skip real HTTP polling.
	HealthCheck func(endpointURL string, timeout time.Duration, transport http.RoundTripper) error

	// TerminateHook is called at the start of every Terminate invocation. Nil
	// in production; set in tests to count or observe termination calls.
	TerminateHook func(slug string, slotID int)

	// lifetimeTimers holds the armed max_session_lifetime backstop timers,
	// keyed by "slug/slotID". Terminate cancels the timer via Stop so that
	// an early client-disconnect does not leave a goroutine for the remaining
	// lifetime duration.
	lifetimeTimers sync.Map
}

// Spawn boots one native elastic worker for slug at the given slotID.
// On success it registers the worker with the proxy and arms the session
// lifetime backstop (if configured). On any failure it releases the booting
// reservation so the pool capacity is restored. Must be called from a
// goroutine - the proxy's spawn callback dispatches it with
// `go spawn(slug, slotID)`.
func (s *ElasticSpawner) Spawn(slug string, slotID int) {
	app, err := s.Store.GetAppBySlug(slug)
	if err != nil {
		slog.Warn("elastic spawn: get app", "slug", slug, "slotID", slotID, "err", err)
		s.Proxy.ReleaseReservation(slug, slotID)
		return
	}

	// Load the latest ready (non-pending, non-failed) deployment.
	deps, err := s.Store.ListDeployments(app.ID)
	if err != nil || len(deps) == 0 {
		slog.Warn("elastic spawn: no ready deployments", "slug", slug, "slotID", slotID)
		s.Proxy.ReleaseReservation(slug, slotID)
		return
	}
	dep := deps[0]

	// Resolve effective resource limits using the same path as the deploy fn.
	defaultMem, defaultCPU := s.RuntimeCfg.DefaultResourcesForApp(app)
	memMB := deploy.ResolveMemoryLimitMB(app.MemoryLimitMB, defaultMem)
	cpuPct := deploy.ResolveCPUQuotaPercent(app.CPUQuotaPercent, defaultCPU)

	// Resolve the launch plan (command, launch-coupled env, ready path).
	port := deploy.AllocatePort()
	tier := s.RuntimeCfg.DefaultTierName()
	if tier == "" {
		tier = process.DefaultTier
	}
	bindHost := s.Manager.AppBindHostFor(tier)

	plan, err := deploy.ResolveLaunch(dep.BundleDir, deploy.LaunchOptions{
		Port:     port,
		BindHost: bindHost,
		// Do not re-run host dep-prep: the venv was built during the initial
		// deploy and the bundle dir is unchanged between elastic spawns.
		PrepHostDeps:    false,
		CommandHostDeps: s.Manager.HostPreparesDepsFor(tier),
	})
	if err != nil {
		slog.Warn("elastic spawn: resolve launch", "slug", slug, "slotID", slotID, "err", err)
		s.Proxy.ReleaseReservation(slug, slotID)
		return
	}

	// The launch above performs no dependency work, so it needs the environment
	// the deploy built. Elastic workers start on demand, potentially long after
	// that deploy, and anything that removes the environment in between - a host
	// reboot with an ephemeral apps dir, a cache wipe, a manual cleanup - leaves
	// every spawn launching against nothing. Without this the failure surfaces as
	// a readiness timeout, which reads as a slow app rather than a missing venv.
	//
	// It deliberately does NOT rebuild. A burst of demand spawns many workers at
	// once against one shared bundle dir, so a self-healing rebuild here would be
	// concurrent builds racing in the same directory. Refusing with a clear cause
	// costs the same failed request and tells the operator what to do.
	if s.Manager.HostPreparesDepsFor(tier) && !deploy.HostEnvironmentReady(dep.BundleDir, plan.AppType) {
		slog.Error("elastic spawn: the app's built environment is missing; redeploy to rebuild it",
			"slug", slug, "slotID", slotID, "bundle", dep.BundleDir,
			"version", dep.Version, "type", plan.AppType)
		s.Proxy.ReleaseReservation(slug, slotID)
		return
	}

	// Start the worker process. slotID is the replica index so the cgroup is
	// named app-<slug>-<slotID> and the Manager's entry is keyed by it.
	info, err := s.Manager.Start(process.StartParams{
		Slug:            slug,
		AppID:           app.ID,
		Index:           slotID,
		Tier:            tier,
		Dir:             dep.BundleDir,
		Command:         plan.Command,
		Port:            port,
		Env:             plan.Env, // ["PORT=<port>"] - per-app env added by Manager's envResolver
		MemoryLimitMB:   memMB,
		CPUQuotaPercent: cpuPct,
		AppVersion:      dep.Version,
		DeploymentID:    dep.ID,
		ContentDigest:   dep.ContentDigest,
	})
	if err != nil {
		slog.Warn("elastic spawn: start process", "slug", slug, "slotID", slotID, "err", err)
		s.Proxy.ReleaseReservation(slug, slotID)
		return
	}

	transport := s.Manager.TransportForWorker(tier, info.WorkerID)

	// Health-check the started process (fast-fail on crash, bounded timeout).
	hc := s.HealthCheck
	if hc == nil {
		hc = func(url string, timeout time.Duration, tr http.RoundTripper) error {
			return waitElasticHealthy(url, timeout, tr, func() bool {
				inf, ok := s.Manager.GetReplica(slug, slotID)
				return ok && inf.Status == process.StatusRunning
			})
		}
	}
	if healthErr := hc(info.EndpointURL, defaultElasticHealthTimeout, transport); healthErr != nil {
		slog.Warn("elastic spawn: health check failed", "slug", slug, "slotID", slotID, "err", healthErr)
		if stopErr := s.Manager.StopReplica(slug, slotID); stopErr != nil {
			slog.Warn("elastic spawn: stop after health failure", "slug", slug, "slotID", slotID, "err", stopErr)
		}
		s.Proxy.ReleaseReservation(slug, slotID)
		return
	}

	// Register the ready worker with the proxy. slotID ties this process to the
	// reservation created by reserveWorker, and deploymentID is used by the
	// proxy's sticky-pin DeploymentID check.
	if regErr := s.Proxy.RegisterElasticWorker(slug, slotID, info.EndpointURL, transport, dep.ID); regErr != nil {
		slog.Warn("elastic spawn: proxy register failed", "slug", slug, "slotID", slotID, "err", regErr)
		if stopErr := s.Manager.StopReplica(slug, slotID); stopErr != nil {
			slog.Warn("elastic spawn: stop after register failure", "slug", slug, "slotID", slotID, "err", stopErr)
		}
		// The slot is still in workerBooting state; release it so capacity
		// returns to the pool.
		s.Proxy.ReleaseReservation(slug, slotID)
		return
	}

	slog.Info("elastic spawn: worker ready",
		"slug", slug, "slotID", slotID, "endpoint", info.EndpointURL)

	// Arm the absolute session-lifetime backstop. When configured, the worker
	// is terminated after the deadline regardless of active connections, so a
	// misbehaving or long-running session cannot pin the worker indefinitely.
	// The returned timer is stored so Terminate can cancel it on an early exit
	// and avoid a goroutine lingering for the full remaining lifetime.
	if app.WorkerMaxSessionLifetimeSecs > 0 {
		lifetime := time.Duration(app.WorkerMaxSessionLifetimeSecs) * time.Second
		key := slug + "/" + strconv.Itoa(slotID)
		timer := time.AfterFunc(lifetime, func() {
			slog.Info("elastic spawn: max session lifetime reached, terminating worker",
				"slug", slug, "slotID", slotID, "lifetime_s", app.WorkerMaxSessionLifetimeSecs)
			// Remove our own map entry before calling Terminate so that
			// Terminate's LoadAndDelete finds nothing and does not double-Stop.
			s.lifetimeTimers.Delete(key)
			s.Terminate(slug, slotID)
		})
		s.lifetimeTimers.Store(key, timer)
	}
}

// Terminate stops the elastic worker at slotID and removes it from the proxy
// pool. It is idempotent: stop errors (e.g. the process already exited) are
// logged at DEBUG level rather than propagated, so a double-terminate from a
// grace-window race and a lifetime backstop is harmless.
func (s *ElasticSpawner) Terminate(slug string, slotID int) {
	if s.TerminateHook != nil {
		s.TerminateHook(slug, slotID)
	}
	// Cancel the max_session_lifetime backstop timer if it is still armed.
	// This prevents the timer goroutine from lingering after an early exit.
	// A missing entry (already fired or never armed) is a no-op.
	key := slug + "/" + strconv.Itoa(slotID)
	if v, ok := s.lifetimeTimers.LoadAndDelete(key); ok {
		v.(*time.Timer).Stop()
	}
	if err := s.Manager.StopReplica(slug, slotID); err != nil {
		slog.Debug("elastic terminate: stop replica (may already be stopped)",
			"slug", slug, "slotID", slotID, "err", err)
	}
	s.Proxy.DeregisterElasticWorker(slug, slotID)
}

// ReapElasticOrphans stops any processes the Manager knows about that belong
// to elastic-mode apps. Elastic workers are ephemeral and are not persisted to
// the replicas table, so on a restart the new Manager starts fresh. If any
// process was wrongly adopted during RecoverProcesses (or left from a prior
// crash) it must be cleaned up so the elastic pool starts empty and clients
// trigger fresh spawns on their next request.
//
// Call once on startup after RecoverProcesses.
func ReapElasticOrphans(store *db.Store, mgr *process.Manager) {
	// Load all apps so we know which slugs are in elastic mode.
	apps, err := store.ListApps(0, 0)
	if err != nil {
		slog.Error("elastic orphan reap: list apps", "err", err)
		return
	}
	elasticSlugs := make(map[string]bool, len(apps))
	for _, app := range apps {
		if isElasticIsolation(app.WorkerIsolation) {
			elasticSlugs[app.Slug] = true
		}
	}
	if len(elasticSlugs) == 0 {
		return
	}

	// Stop all Manager-known replicas that belong to elastic apps. These must
	// not be re-used: the elastic pool starts empty and clients re-allocate on
	// their next request.
	var reaped int
	for _, info := range mgr.All() {
		if !elasticSlugs[info.Slug] {
			continue
		}
		slog.Warn("elastic orphan reap: stopping leftover worker",
			"slug", info.Slug, "index", info.Index, "pid", info.PID)
		if err := mgr.StopReplica(info.Slug, info.Index); err != nil {
			slog.Warn("elastic orphan reap: stop failed",
				"slug", info.Slug, "index", info.Index, "err", err)
		}
		reaped++
	}
	if reaped > 0 {
		slog.Info("elastic orphan reap: done", "reaped", reaped)
	}
}

// isElasticIsolation reports whether the WorkerIsolation string represents a
// demand-driven (elastic) mode rather than the default multiplex mode.
// Must stay in sync with proxy.poolIsElastic if a new isolation mode is added.
func isElasticIsolation(mode string) bool {
	return mode == string(config.IsolationGrouped) || mode == string(config.IsolationPerSession)
}

// waitElasticHealthy polls endpointURL until a non-5xx status is received,
// the timeout expires, or alive() returns false (process exited). It mirrors
// deploy.waitHealthyOrExit but is an unexported lifecycle-package helper so
// the elastic spawn path reuses the same probe logic without importing a
// private deploy function.
func waitElasticHealthy(endpointURL string, timeout time.Duration, transport http.RoundTripper, alive func() bool) error {
	client := &http.Client{Timeout: 5 * time.Second}
	if transport != nil {
		client.Transport = transport
	}
	healthURL := endpointURL
	if len(healthURL) > 0 && healthURL[len(healthURL)-1] != '/' {
		healthURL += "/"
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithDeadline(context.Background(), deadline)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
		if err != nil {
			cancel()
			return fmt.Errorf("build health request for %s: %w", healthURL, err)
		}
		resp, err := client.Do(req)
		cancel()
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode < 500 {
				return nil
			}
		}
		if alive != nil && !alive() {
			return fmt.Errorf("elastic worker at %s crashed before becoming healthy", endpointURL)
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("elastic worker at %s did not become healthy within %s", endpointURL, timeout)
}
