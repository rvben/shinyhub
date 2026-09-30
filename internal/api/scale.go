package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
)

// defaultMaxReplicas is the fallback per-app replica ceiling when the runtime
// config does not specify one. Mirrors the config default.
const defaultMaxReplicas = 32

// ScaleUp boots one additional replica at the next trailing index, growing the
// pool by one without cycling the existing replicas. It returns (true, nil)
// when a replica was added and (false, nil) for the benign no-op cases the
// autoscale controller treats as "already at the ceiling": the app is not
// running, or it is already at the runtime max-replicas limit. Errors are
// reserved for genuine failures (missing deployment, boot failure, persistence
// error). The whole operation is serialized against deploy/restart/rollback/
// redeploy through the per-slug deploy lock.
func (s *Server) ScaleUp(slug string) (bool, error) {
	release := s.acquireDeployLock(slug)
	defer release()
	// A manual settings request may have committed a target before its queued
	// reconciliation takes this lock. Do not scale from that intermediate size.
	if s.isRedeployInFlight(slug) {
		return false, nil
	}

	app, err := s.store.GetAppBySlug(slug)
	if err != nil {
		return false, fmt.Errorf("scale up %s: get app: %w", slug, err)
	}
	if err := s.guardActivationLifecycle(app.ID, "scale up "+slug); err != nil {
		return false, err
	}
	if err := s.guardCompatibilityQuarantine(app.ID, "scale up "+slug); err != nil {
		return false, err
	}
	// Only grow a pool the operator still wants running; a concurrent stop or
	// delete that won the lock first must not be resurrected by a queued scale.
	if app.Status != "running" && app.Status != "degraded" {
		return false, nil
	}
	max := s.cfg.Runtime.MaxReplicas
	if max <= 0 {
		max = defaultMaxReplicas
	}
	if app.Replicas >= max {
		return false, nil
	}
	// Re-enforce the app's own autoscale ceiling under the lock. The controller
	// clamps to this when it decides, but its decision is taken against an
	// unlocked snapshot; re-reading the live row here closes the race where a
	// concurrent config change lowered the cap after the decision was made, so a
	// queued grow can never push an autoscaled pool past its configured maximum.
	if app.AutoscaleEnabled && app.Replicas >= app.AutoscaleMaxReplicas {
		return false, nil
	}
	return s.scaleUpLocked(app, true)
}

// scaleUpLocked adds one trailing slot with the app operation lock held.
// Manual reconciliation supplies the actual pool size in app.Replicas and
// leaves the already-persisted requested size untouched.
func (s *Server) scaleUpLocked(app *db.App, persistSize bool) (bool, error) {
	slug := app.Slug

	deployments, err := s.store.ListRecentDeployments(app.ID, 1)
	if err != nil || len(deployments) == 0 {
		return false, fmt.Errorf("scale up %s: no deployments", slug)
	}
	current := deployments[0]
	if err := s.checkColocatedShared(app.ID, s.tiersForApp(app)); err != nil {
		return false, fmt.Errorf("scale up %s: %w", slug, err)
	}

	// Restore parked rows before adding: thaw frozen slots and cold-boot stopped
	// ones. Autoscaling returns after restoration; a manual resize also adds the
	// requested trailing slot. This keeps the serving pool contiguous.
	reps, err := s.store.ListReplicas(app.ID)
	if err != nil {
		return false, fmt.Errorf("scale up %s: list replicas: %w", slug, err)
	}
	if persistSize && replicaRowSize(reps) != app.Replicas {
		return false, nil // a failed manual resize still needs reconciliation
	}
	var warmVictims []warmVictim
	for _, r := range reps {
		if r.DesiredState == db.ReplicaDesiredWarm && (r.Status == "stopped" || r.Status == "suspended") {
			warmVictims = append(warmVictims, warmVictim{index: r.Index, rep: r})
		}
	}
	if len(warmVictims) > 0 {
		sessionCap := deploy.ResolveMaxSessionsPerReplica(app.MaxSessionsPerReplica, s.cfg.Runtime.DefaultMaxSessionsPerReplica)
		identityEnabled := deploy.ResolveIdentityHeaders(app.IdentityHeaders, s.cfg.Auth.IdentityHeadersEnabled())
		if s.proxy != nil {
			s.proxy.SetPoolCap(slug, sessionCap)
			// SetPoolMode not called here: scale-defrag adjusts capacity,
			// not isolation mode. Mode is set at deploy/recovery/wake time.
			s.proxy.SetPoolIdentityHeaders(slug, identityEnabled)
		}
		defragMem, defragCPU := s.cfg.Runtime.DefaultResourcesForApp(app)
		p := s.withTierPlacement(deploy.Params{
			Slug:                  slug,
			BundleDir:             current.BundleDir,
			Replicas:              app.Replicas,
			Manager:               s.manager,
			Proxy:                 s.proxy,
			MemoryLimitMB:         deploy.ResolveMemoryLimitMB(app.MemoryLimitMB, defragMem),
			CPUQuotaPercent:       deploy.ResolveCPUQuotaPercent(app.CPUQuotaPercent, defragCPU),
			MaxSessionsPerReplica: sessionCap,
			IdentityHeaders:       identityEnabled,
			ContentDigest:         current.ContentDigest,
			DeploymentID:          current.ID,
			AppVersion:            current.Version,
		}, app)
		restored, bootErr := s.bootWarmVictims("scale up", app, current, p, warmVictims)
		if restored > 0 {
			s.store.LogAuditEvent(db.AuditEventParams{
				Action:       "scale_up",
				ResourceType: "app",
				ResourceID:   slug,
				Detail:       db.AuditDetail(map[string]any{"defrag": true, "restored": restored}),
			})
		}
		if persistSize || bootErr != nil {
			return restored > 0, bootErr
		}
		// A manual resize must also reach the requested total, so after
		// restoring parked slots continue to add the trailing slot below.
	}

	newIndex := app.Replicas
	total := newIndex + 1

	// For a tier-placed app the placement map is authoritative for both the
	// per-index tier and the total size, so growing only apps.replicas would
	// land the new index on the default tier and desync the stored placement.
	// Grow the tier that owns the current highest index (the last populated tier
	// in tier order) so the new index extends that tier's contiguous block, and
	// stamp the grown placement onto the app before building the boot params.
	placement := app.PlacementMap()
	tierPlaced := len(placement) > 0
	if tierPlaced {
		tier := lastPopulatedTier(placement, s.cfg.Runtime.TierOrder())
		if tier == "" {
			return false, fmt.Errorf("scale up %s: no populated tier to grow", slug)
		}
		placement[tier]++
		b, err := json.Marshal(placement)
		if err != nil {
			return false, fmt.Errorf("scale up %s: marshal placement: %w", slug, err)
		}
		app.ReplicaPlacement = string(b)
	}

	sessionCap := deploy.ResolveMaxSessionsPerReplica(app.MaxSessionsPerReplica, s.cfg.Runtime.DefaultMaxSessionsPerReplica)
	identityEnabled := deploy.ResolveIdentityHeaders(app.IdentityHeaders, s.cfg.Auth.IdentityHeadersEnabled())
	if s.proxy != nil {
		s.proxy.SetPoolSize(slug, total)
		s.proxy.SetPoolCap(slug, sessionCap)
		// SetPoolMode not called here: scale adjusts capacity, not isolation
		// mode. Mode is set at deploy/recovery/wake time.
		s.proxy.SetPoolIdentityHeaders(slug, identityEnabled)
	}

	scaleDefaultMem, scaleDefaultCPU := s.cfg.Runtime.DefaultResourcesForApp(app)
	p := s.withTierPlacement(deploy.Params{
		Slug:                  slug,
		BundleDir:             current.BundleDir,
		Replicas:              total,
		Manager:               s.manager,
		Proxy:                 s.proxy,
		MemoryLimitMB:         deploy.ResolveMemoryLimitMB(app.MemoryLimitMB, scaleDefaultMem),
		CPUQuotaPercent:       deploy.ResolveCPUQuotaPercent(app.CPUQuotaPercent, scaleDefaultCPU),
		MaxSessionsPerReplica: sessionCap,
		IdentityHeaders:       identityEnabled,
		ContentDigest:         current.ContentDigest,
		DeploymentID:          current.ID,
		AppVersion:            current.Version,
	}, app)
	p = s.guardDeploymentConsumerStart(app, current, p)

	releaseConsumerBoot, gateErr := s.acquireConsumerBootGate(app.ID)
	if gateErr != nil {
		return false, fmt.Errorf("scale up %s: acquire startup-data compatibility fence: %w", slug, gateErr)
	}
	defer releaseConsumerBoot()
	p = s.traceDeploy(context.Background(), p)
	r, err := s.deployReplica(p, newIndex)
	if err != nil {
		// Roll back the optimistic pool growth so a failed boot does not leave a
		// permanently nil trailing slot that the saturation signal would read as
		// a degraded pool.
		if s.proxy != nil {
			s.proxy.SetPoolSize(slug, newIndex)
		}
		return false, fmt.Errorf("scale up %s: boot replica %d: %w", slug, newIndex, err)
	}

	// rollbackStarted undoes a successful boot when a durable write afterwards
	// fails, so a persistence error never leaves the started replica running and
	// routable while the app row still advertises the old size (an orphaned,
	// unmanaged backend that future autoscale decisions would misread). It stops
	// the process and shrinks the proxy pool back to the pre-grow size; when the
	// replica row was already written it is deleted too so no dangling row
	// survives. Every step is best-effort and logged rather than masking the
	// original persistence error returned to the caller.
	rollbackStarted := func(deleteRow bool) {
		if s.manager != nil {
			if err := s.manager.StopReplicaConfirmed(slug, newIndex); err != nil && !errors.Is(err, process.ErrReplicaNotFound) {
				slog.Error("scale up: rollback stop replica", "slug", slug, "index", newIndex, "err", err)
			}
		}
		if s.proxy != nil {
			s.proxy.SetPoolSize(slug, newIndex)
		}
		if deleteRow {
			if err := s.store.DeleteReplica(app.ID, newIndex); err != nil {
				slog.Error("scale up: rollback delete replica row", "slug", slug, "index", newIndex, "err", err)
			}
		}
	}

	depID := current.ID
	pid, port := r.PID, r.Port
	if err := s.store.UpsertReplica(db.UpsertReplicaParams{
		AppID:               app.ID,
		Index:               r.Index,
		PID:                 &pid,
		Port:                &port,
		Status:              "running",
		Provider:            r.Provider,
		Tier:                r.Tier,
		EndpointURL:         r.EndpointURL,
		WorkerID:            r.WorkerID,
		AppVersion:          current.Version,
		DesiredState:        "running",
		DeploymentID:        &depID,
		StartupPeakRSSBytes: r.StartupPeakRSSBytes,
		ConsumerBooted:      true,
	}); err != nil {
		rollbackStarted(false)
		return false, fmt.Errorf("scale up %s: upsert replica %d: %w", slug, r.Index, err)
	}
	if tierPlaced {
		if err := s.store.SetAppPlacement(app.ID, app.ReplicaPlacement, total); err != nil {
			rollbackStarted(true)
			return false, fmt.Errorf("scale up %s: persist placement: %w", slug, err)
		}
	} else if persistSize {
		if err := s.store.UpdateAppReplicas(app.ID, total); err != nil {
			rollbackStarted(true)
			return false, fmt.Errorf("scale up %s: update replica count: %w", slug, err)
		}
	}
	return true, nil
}

// ScaleDown gracefully removes the highest-index replica. It marks the proxy
// slot draining (the least-connections picker stops routing new cookie-less
// sessions to it while sticky-cookie sessions keep flowing), waits up to grace
// for active sessions to finish, then stops the replica, shrinks the proxy
// pool, deletes the replica row, and decrements the app's replica count. It
// returns (false, nil) when the app is already at one replica (the floor) and
// (true, nil) when a replica was removed. Serialized via the per-slug deploy
// lock. When grace elapses with sessions still active the replica is stopped
// anyway: the operation is deadline-bounded so the controller never stalls.
func (s *Server) ScaleDown(slug string, grace time.Duration) (bool, error) {
	release := s.acquireDeployLock(slug)
	defer release()
	if s.isRedeployInFlight(slug) {
		return false, nil
	}

	app, err := s.store.GetAppBySlug(slug)
	if err != nil {
		return false, fmt.Errorf("scale down %s: get app: %w", slug, err)
	}
	if err := s.guardActivationLifecycle(app.ID, "scale down "+slug); err != nil {
		return false, err
	}
	// Honour a concurrent stop/delete that won the lock first: a torn-down app
	// must not have its DB rows mutated or a phantom proxy pool fabricated by
	// the SetPoolSize shrink below.
	if app.Status != "running" && app.Status != "degraded" {
		return false, nil
	}
	// Unified floor: max(autoscale min when enabled, min_warm_replicas, 1).
	// Re-enforced under the lock (same reason ScaleUp re-checks the ceiling):
	// the controller clamps against an unlocked snapshot; re-reading the live row
	// closes the race where a concurrent config change raised the floor after the
	// decision was made, so a queued shrink can never take the pool below any of
	// the three lower bounds.
	floor := 1
	if app.AutoscaleEnabled && app.AutoscaleMinReplicas > floor {
		floor = app.AutoscaleMinReplicas
	}
	if app.MinWarmReplicas > floor {
		floor = app.MinWarmReplicas
	}
	if app.Replicas <= floor {
		return false, nil
	}
	rows, err := s.store.ListReplicas(app.ID)
	if err != nil {
		return false, err
	}
	if replicaRowSize(rows) != app.Replicas {
		return false, nil
	}
	return s.scaleDownLocked(app, grace, true)
}

// scaleDownLocked drains only the trailing slot with the operation lock held.
func (s *Server) scaleDownLocked(app *db.App, grace time.Duration, persistSize bool) (bool, error) {
	slug := app.Slug
	victim := app.Replicas - 1

	if s.proxy != nil {
		s.proxy.DrainReplica(slug, victim)
	}
	// Persist intent before waiting so the dashboard observes draining even on
	// a single node, and other instances stop admitting new sessions to the slot.
	if err := s.store.SetReplicaDesiredState(app.ID, victim, "draining"); err != nil {
		// The local drain remains active if the advisory write fails.
		slog.Warn("scale down: set desired_state draining", "slug", slug, "index", victim, "err", err)
	}
	if s.proxy != nil {
		s.waitForDrain(slug, victim, grace, s.clusteredFleetWait(app.ID, victim))
	}
	if s.manager != nil {
		if err := s.manager.StopReplicaConfirmed(slug, victim); err != nil {
			switch {
			case errors.Is(err, process.ErrReplicaNotFound):
				// A missing entry is benign (the replica may already be gone);
				// log and proceed so the routing table and DB still converge on
				// the new size.
				slog.Warn("scale down: stop replica", "slug", slug, "index", victim, "err", err)
			default:
				// Any other failure means the replica may still be running (e.g. a
				// remote worker rejected the SIGTERM). Shrinking the proxy and
				// deleting the row now would orphan a live replica while the
				// control plane believes capacity was removed. Roll back both the
				// local drain mark and, in clustered mode, the desired_state row
				// so the still-running replica resumes full service everywhere.
				if s.proxy != nil {
					s.proxy.UndrainReplica(slug, victim)
				}
				if rerr := s.store.SetReplicaDesiredState(app.ID, victim, "running"); rerr != nil {
					slog.Warn("scale down: revert desired_state running", "slug", slug, "index", victim, "err", rerr)
				}
				return false, fmt.Errorf("scale down %s: stop replica %d: %w", slug, victim, err)
			}
		}
	}
	if s.proxy != nil {
		s.proxy.SetPoolSize(slug, victim)
	}
	if err := s.store.DeleteReplica(app.ID, victim); err != nil {
		return false, fmt.Errorf("scale down %s: delete replica %d: %w", slug, victim, err)
	}

	// Keep tier placement authoritative: shrink the tier that owns the highest
	// index (the last populated tier in tier order) so a later full deploy does
	// not expand from a stale placement map and recreate the removed replica.
	// victim >= 1 guarantees at least one tier still has a positive count.
	placement := app.PlacementMap()
	if len(placement) > 0 {
		tier := lastPopulatedTier(placement, s.cfg.Runtime.TierOrder())
		if tier == "" {
			return false, fmt.Errorf("scale down %s: no populated tier to shrink", slug)
		}
		placement[tier]--
		if placement[tier] <= 0 {
			delete(placement, tier)
		}
		b, err := json.Marshal(placement)
		if err != nil {
			return false, fmt.Errorf("scale down %s: marshal placement: %w", slug, err)
		}
		if err := s.store.SetAppPlacement(app.ID, string(b), victim); err != nil {
			return false, fmt.Errorf("scale down %s: persist placement: %w", slug, err)
		}
	} else if persistSize {
		if err := s.store.UpdateAppReplicas(app.ID, victim); err != nil {
			return false, fmt.Errorf("scale down %s: update replica count: %w", slug, err)
		}
	}
	return true, nil
}

// cycleResize serves a replica-count-only settings redeploy under the caller's
// deploy lock: it grows or shrinks the live pool to the stored count without
// cycling surviving slots, and reports the outcome the way cycleRedeploy does.
// A failure marks the app degraded with the error, so the next replica edit
// retries the resize. A panic is recovered and reported as a failure.
func (s *Server) cycleResize(slug string) (outcome, reason string) {
	defer func() {
		if p := recover(); p != nil {
			slog.Error("resize app: panic", "slug", slug, "panic", p, "stack", string(debug.Stack()))
			outcome, reason = db.RedeployFailed, fmt.Sprintf("internal error: %v", p)
		}
	}()
	outcome, reason, err := s.resizeAppLocked(slug)
	if err != nil {
		slog.Error("resize app", "slug", slug, "err", err)
		if updateErr := s.store.UpdateAppStatus(db.UpdateAppStatusParams{
			Slug: slug, Status: "degraded", LastError: err.Error(),
		}); updateErr != nil {
			slog.Error("resize app: persist failure", "slug", slug, "err", updateErr)
		}
		return db.RedeployFailed, err.Error()
	}
	return outcome, reason
}

// resizeAppLocked reconciles a manual replica edit without cycling surviving
// slots. The DB app count is the requested size; replica rows describe the
// current size. Both are read under the deploy lock, so queued edits converge
// on the latest request and a stop or delete is not undone by a queued resize.
// A returned error is a failure; otherwise the outcome says whether the pool
// reached the requested size with every slot healthy.
func (s *Server) resizeAppLocked(slug string) (outcome, reason string, err error) {
	app, err := s.store.GetAppBySlug(slug)
	if err != nil {
		return "", "", fmt.Errorf("read app: %w", err)
	}
	if app.Status != "running" && app.Status != "degraded" {
		return db.RedeploySkipped, "not_running", nil
	}
	// A guard that could not read its state is a failure, not a skip: only a
	// condition the guard actually observed may report the benign outcome.
	if err := s.guardActivationLifecycle(app.ID, "resize "+slug); err != nil {
		if errors.Is(err, errScheduleActivationInFlight) {
			return db.RedeploySkipped, "activation_deferred", nil
		}
		return "", "", err
	}
	if err := s.guardCompatibilityQuarantine(app.ID, "resize "+slug); err != nil {
		if errors.Is(err, errCompatibilityQuarantined) {
			return db.RedeploySkipped, "quarantined", nil
		}
		return "", "", err
	}
	// Replica count is inert in elastic modes, whose workers are demand-driven.
	if deploy.ResolveWorkerIsolation(app.WorkerIsolation, s.cfg.Runtime.DefaultWorkerIsolation) != "multiplex" {
		return db.RedeployCompleted, "", nil
	}
	if len(app.PlacementMap()) > 0 {
		return "", "", fmt.Errorf("resize %s: explicit placement requires a topology change", slug)
	}
	rows, err := s.store.ListReplicas(app.ID)
	if err != nil {
		return "", "", err
	}
	actual := replicaRowSize(rows)
	target := app.Replicas
	grace := s.cfg.Server.DrainTimeout
	if grace <= 0 {
		grace = 60 * time.Second
	}
	for actual != target {
		app.Replicas = actual
		var changed bool
		if actual < target {
			changed, err = s.scaleUpLocked(app, false)
		} else {
			changed, err = s.scaleDownLocked(app, grace, false)
		}
		if err != nil {
			return "", "", err
		}
		if !changed {
			return "", "", fmt.Errorf("resize %s: pool did not converge", slug)
		}
		if actual < target {
			actual++
		} else {
			actual--
		}
	}
	// Clear a previous resize failure only when all requested slots are healthy.
	rows, err = s.store.ListReplicas(app.ID)
	if err != nil {
		return "", "", err
	}
	unhealthy := 0
	for _, row := range rows {
		if row.Status != db.ReplicaStatusRunning &&
			!(row.DesiredState == db.ReplicaDesiredWarm && (row.Status == "stopped" || row.Status == "suspended")) {
			unhealthy++
		}
	}
	if unhealthy > 0 {
		return db.RedeployPartial, fmt.Sprintf("%d of %d replicas are not running", unhealthy, len(rows)), nil
	}
	if app.Status == "degraded" {
		if err := s.store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: slug, Status: "running"}); err != nil {
			return "", "", err
		}
	}
	return db.RedeployCompleted, "", nil
}

func replicaRowSize(rows []*db.Replica) int {
	size := 0
	for _, row := range rows {
		if row.Index >= size {
			size = row.Index + 1
		}
	}
	return size
}

// lastPopulatedTier returns the last tier in tierOrder with a positive replica
// count in placement, i.e. the tier that owns the highest global replica index.
// Growing this tier appends the next index to its contiguous block, and
// shrinking it removes the highest index, both without shifting the tier of any
// existing index. Returns "" when no tier has a positive count.
func lastPopulatedTier(placement map[string]int, tierOrder []string) string {
	last := ""
	for _, t := range tierOrder {
		if placement[t] > 0 {
			last = t
		}
	}
	return last
}

// waitForDrain blocks until the active session count for slug's slot at index
// reaches zero or grace elapses, whichever comes first. A nil or absent slot
// (count -1) is treated as already drained. An optional fleetDrained predicate
// (non-nil in clustered mode) must also return true for the drain to complete
// early; when absent the local count alone governs early exit.
func (s *Server) waitForDrain(slug string, index int, grace time.Duration, fleetDrained func() bool) {
	deadline := time.Now().Add(grace)
	for {
		counts := s.proxy.ReplicaSessionCounts(slug)
		localDrained := index >= len(counts) || counts[index] <= 0
		fleetOK := fleetDrained == nil || fleetDrained()
		if localDrained && fleetOK {
			return
		}
		if !time.Now().Before(deadline) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// clusteredFleetWait returns a predicate that reports whether the fleet-wide
// session count for the given app's replica index (excluding this instance)
// has reached zero. Returns nil in single-node mode so waitForDrain skips the
// fleet check. The predicate is deadline-bounded by the caller: waitForDrain
// exits when its grace elapses regardless of what this predicate returns.
func (s *Server) clusteredFleetWait(appID int64, idx int) func() bool {
	if !s.clustered {
		return nil
	}
	return func() bool {
		staleWindowSec := int64(proxy.ReplicaSessionStaleCutoff.Seconds())
		active, _, err := s.store.AppFleetLoad(appID, staleWindowSec, s.instanceID)
		if err != nil {
			// Treat a store error as not-yet-drained (conservative); the
			// deadline in waitForDrain ensures we do not block forever.
			return false
		}
		if idx >= len(active) {
			return true // no rows for this index = no sessions on other instances
		}
		return active[idx] <= 0
	}
}
