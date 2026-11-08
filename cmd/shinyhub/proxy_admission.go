package main

import (
	"fmt"
	"log/slog"
	"sync"

	"github.com/rvben/shinyhub/internal/admission"
	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/proxy"
	"github.com/rvben/shinyhub/internal/rungroup"
	gopscpu "github.com/shirou/gopsutil/v4/cpu"
	gopsmem "github.com/shirou/gopsutil/v4/mem"
)

// configureProxyAdmission sizes the proxy's admission controls against the
// host (memory floor, CPU watermark, per-app render pacing), reports the
// detected host capacity to the API server, and installs the per-app lookups
// the proxy consults on a request: access mode, status, display name and the
// served bundle's version-switch policy. The CPU watermark's sampler runs as a
// service of the run group.
func configureProxyAdmission(cfg *config.Config, store *db.Store, srv *api.Server, prx *proxy.Proxy, services *rungroup.Group) error {
	// Host-memory admission floor for elastic pools: while MemAvailable is
	// below the configured floor, new worker allocation is shed (503) instead
	// of letting the kernel OOM killer pick a live worker. Probe errors fail
	// open inside the proxy.
	if minMB := cfg.MinAvailableMemoryMB(); minMB > 0 {
		prx.SetMemoryGuard(minMB, func() (int, bool) {
			vm, err := gopsmem.VirtualMemory()
			if err != nil {
				return 0, false
			}
			return int(vm.Available / (1024 * 1024)), true
		})
		slog.Info("elastic admission memory floor active", "min_available_memory_mb", minMB)
	}

	// Render-aware admission. Detect the cores the pacer sizes against and log
	// which source won so an operator can see the effective number.
	renderCores, coreSource := admission.Detect(cfg.RenderCapacityCores())
	slog.Info("render admission: effective cores", "cores", renderCores, "source", coreSource)

	// Host capacity: the scale Overview measures fleet usage against when no app
	// carries an enforced per-replica limit. Separate override from the pacer's
	// render_capacity_cores, because one is a reporting scale and the other is a
	// pacing budget; the detection behind both is the same.
	hostCores, hostCoreSource := admission.Detect(cfg.HostCapacityCores())
	slog.Info("live-update CPU accounting", "worker_cpu_request_cores", cfg.Server.UpdateCapacity.CPURequest(),
		"counts_idle_and_suspended_workers", true)
	hostMemoryMB, hostMemorySource, hostMemoryOK := admission.DetectMemory(cfg.HostCapacityMemoryMB())
	if !hostMemoryOK {
		hostMemoryMB, hostMemorySource = 0, ""
		slog.Warn("host capacity: memory could not be detected; Overview will report usage without a memory scale",
			"hint", "set server.host_capacity_memory_mb to supply it")
	}
	srv.SetHostCapacity(hostCores, hostCoreSource, hostMemoryMB, hostMemorySource)
	slog.Info("host capacity", "cores", hostCores, "cores_source", hostCoreSource,
		"memory_mb", hostMemoryMB, "memory_source", hostMemorySource)

	// Host CPU watermark, only when enabled. It samples on its own goroutine;
	// cpu.Percent blocks, so it is never called from the admission path. Under
	// runtime.mode docker this is sized against the ShinyHub process, not each
	// worker's container quota (a known limitation).
	if maxCPU := cfg.MaxCPUPercent(); maxCPU > 0 {
		wm := admission.NewWatermark(maxCPU, func() (float64, error) {
			pcts, err := gopscpu.Percent(0, false)
			if err != nil {
				return 0, err
			}
			if len(pcts) == 0 {
				return 0, fmt.Errorf("cpu.Percent returned no samples")
			}
			return pcts[0], nil
		})
		services.Go("cpu watermark", serviceMemoryStopBudget, wm.Run)
		prx.SetCPUWatermark(wm)
	}

	routing := appRoutingLookups(store, srv.DeployInFlight)

	// Size a per-app limiter for every app that has opted into pacing
	// (render_seconds > 0). Apps at the default 0 get no limiter, so this is a
	// no-op today. The access-mode lookup lets per-principal fairness key
	// correctly.
	prx.SetAppAccessLookup(routing.access)
	renderApps, err := store.ListApps(0, 0) // limit <= 0 means all apps (queries.go ListApps)
	if err != nil {
		return fmt.Errorf("list apps for render-admission sizing: %w", err)
	}

	// Build the limiter factory once, capturing the effective host sizing, so a
	// render_seconds change (CLI/API/manifest, or poolsync convergence) can install
	// or clear a limiter live without calling Detect again.
	headroom := cfg.RenderHeadroom()
	divisor := cfg.PrincipalShareDivisor()
	principalBurst := cfg.RenderPrincipalBurst()
	lru := cfg.PrincipalLRUCapacity()
	prx.SetRenderLimiterFactory(func(renderSeconds float64) *admission.AppLimiter {
		return proxy.BuildAppLimiter(renderSeconds, renderCores, headroom, float64(principalBurst), divisor, lru)
	})
	for _, a := range renderApps {
		prx.ApplyRenderPacing(a.Slug, a.RenderSeconds)
	}
	srv.SetRenderPacingCores(renderCores, coreSource)
	prx.SetRenderParkBudget(cfg.RenderParkMaxPerApp(), cfg.RenderParkMaxTotal())
	prx.SetRenderParkTTL(cfg.RenderParkTTL())
	prx.SetRenderPageWait(cfg.RenderParkTTL())

	// Let the proxy render a clear status page (instead of the endless loading
	// spinner) when a request hits an app that is crashed or stopped, and the
	// deploying wait page while a deployment is in flight.
	prx.SetAppStatusLookup(routing.status)
	prx.SetAppNameLookup(routing.name)

	prx.SetVersionSwitchLookup(versionSwitchPolicyLookup(store))

	// Hold a request for a not-yet-routable app while its wake completes, so a
	// warm resume serves inline (no loading-page round-trip). 0 disables it.
	prx.SetWakeHoldTimeout(cfg.Lifecycle.WakeHold)
	return nil
}

// appRoutingSource is the only app read the per-request proxy lookups may
// make. It excludes the full app lookup on purpose: that one counts the app's
// deployment history, a cost that grows with every release.
type appRoutingSource interface {
	GetAppRoutingBySlug(slug string) (*db.AppRouting, error)
}

// routingLookups are the per-request answers the proxy asks of the store. Each
// answers empty when the app cannot be read, which the proxy treats as unknown.
type routingLookups struct {
	access func(slug string) string
	name   func(slug string) string
	status func(slug string) (status, reason string)
}

// appRoutingLookups builds the proxy's per-request lookups. The status lookup
// reports "deploying" from a pending deployment row, and asks deployInFlight
// (a map read) so a stale pending row cannot mask a stopped or crashed app.
func appRoutingLookups(routes appRoutingSource, deployInFlight func(slug string) bool) routingLookups {
	return routingLookups{
		access: func(slug string) string {
			a, err := routes.GetAppRoutingBySlug(slug)
			if err != nil {
				return "" // unknown: the charge-point plan treats this as the conservative case
			}
			return a.Access
		},
		name: func(slug string) string {
			a, err := routes.GetAppRoutingBySlug(slug)
			if err != nil {
				return ""
			}
			return a.Name
		},
		status: func(slug string) (string, string) {
			a, err := routes.GetAppRoutingBySlug(slug)
			if err != nil {
				return "", ""
			}
			return a.MissStatus(deployInFlight(slug))
		},
	}
}

// versionSwitchPolicyLookup answers the version-switch policy declared by the
// bundle a deployment serves. A served bundle is immutable, so each
// deployment's manifest is read once and remembered together with the app it
// belongs to. Anything that cannot be resolved, including a deployment of a
// different app, answers "manual", the policy that never switches a page by
// itself.
func versionSwitchPolicyLookup(store *db.Store) func(slug string, deploymentID int64) string {
	type servedPolicy struct{ slug, policy string }
	var policies sync.Map
	return func(slug string, deploymentID int64) string {
		if cached, ok := policies.Load(deploymentID); ok {
			entry := cached.(servedPolicy)
			if entry.slug == slug {
				return entry.policy
			}
			return "manual"
		}
		app, err := store.GetAppBySlug(slug)
		if err != nil {
			return "manual"
		}
		served, err := store.GetDeploymentByID(deploymentID)
		if err != nil || served.AppID != app.ID {
			return "manual"
		}
		manifest, err := deploy.LoadManifest(served.BundleDir)
		if err != nil || manifest == nil {
			return "manual"
		}
		policies.Store(deploymentID, servedPolicy{slug: slug, policy: manifest.App.VersionSwitch})
		return manifest.App.VersionSwitch
	}
}
