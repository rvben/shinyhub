package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/process"
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

	app, err := s.store.GetAppBySlug(slug)
	if err != nil {
		return false, fmt.Errorf("scale up %s: get app: %w", slug, err)
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

	deployments, err := s.store.ListDeployments(app.ID)
	if err != nil || len(deployments) == 0 {
		return false, fmt.Errorf("scale up %s: no deployments", slug)
	}
	current := deployments[0]
	if err := s.checkColocatedShared(app.ID, s.tiersForApp(app)); err != nil {
		return false, fmt.Errorf("scale up %s: %w", slug, err)
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
	if s.proxy != nil {
		s.proxy.SetPoolSize(slug, total)
		s.proxy.SetPoolCap(slug, sessionCap)
	}

	p := s.withTierPlacement(deploy.Params{
		Slug:                  slug,
		BundleDir:             current.BundleDir,
		Replicas:              total,
		Manager:               s.manager,
		Proxy:                 s.proxy,
		MemoryLimitMB:         deploy.ResolveMemoryLimitMB(app.MemoryLimitMB, s.cfg.Runtime.Docker.DefaultMemoryMB),
		CPUQuotaPercent:       deploy.ResolveCPUQuotaPercent(app.CPUQuotaPercent, s.cfg.Runtime.Docker.DefaultCPUPercent),
		MaxSessionsPerReplica: sessionCap,
		ContentDigest:         current.ContentDigest,
		DeploymentID:          current.ID,
		AppVersion:            current.Version,
	}, app)

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

	depID := current.ID
	pid, port := r.PID, r.Port
	if err := s.store.UpsertReplica(db.UpsertReplicaParams{
		AppID:        app.ID,
		Index:        r.Index,
		PID:          &pid,
		Port:         &port,
		Status:       "running",
		Provider:     r.Provider,
		Tier:         r.Tier,
		EndpointURL:  r.EndpointURL,
		WorkerID:     r.WorkerID,
		AppVersion:   current.Version,
		DesiredState: "running",
		DeploymentID: &depID,
	}); err != nil {
		return false, fmt.Errorf("scale up %s: upsert replica %d: %w", slug, r.Index, err)
	}
	if tierPlaced {
		if err := s.store.SetAppPlacement(app.ID, app.ReplicaPlacement, total); err != nil {
			return false, fmt.Errorf("scale up %s: persist placement: %w", slug, err)
		}
	} else if err := s.store.UpdateAppReplicas(app.ID, total); err != nil {
		return false, fmt.Errorf("scale up %s: update replica count: %w", slug, err)
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

	app, err := s.store.GetAppBySlug(slug)
	if err != nil {
		return false, fmt.Errorf("scale down %s: get app: %w", slug, err)
	}
	// Honour a concurrent stop/delete that won the lock first: a torn-down app
	// must not have its DB rows mutated or a phantom proxy pool fabricated by
	// the SetPoolSize shrink below.
	if app.Status != "running" && app.Status != "degraded" {
		return false, nil
	}
	if app.Replicas <= 1 {
		return false, nil
	}
	// Re-enforce the app's own autoscale floor under the lock, for the same
	// reason ScaleUp re-checks the ceiling: the controller clamps to this when it
	// decides, but against an unlocked snapshot. Re-reading the live row closes
	// the race where a concurrent config change raised the minimum after the
	// decision was made, so a queued shrink can never take an autoscaled pool
	// below its configured minimum.
	if app.AutoscaleEnabled && app.Replicas <= app.AutoscaleMinReplicas {
		return false, nil
	}
	victim := app.Replicas - 1

	if s.proxy != nil {
		s.proxy.DrainReplica(slug, victim)
		s.waitForDrain(slug, victim, grace)
	}
	if s.manager != nil {
		if err := s.manager.StopReplica(slug, victim); err != nil {
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
				// control plane believes capacity was removed. Roll back the drain
				// mark so the still-running replica resumes full service, and abort
				// with state intact.
				if s.proxy != nil {
					s.proxy.UndrainReplica(slug, victim)
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
	} else if err := s.store.UpdateAppReplicas(app.ID, victim); err != nil {
		return false, fmt.Errorf("scale down %s: update replica count: %w", slug, err)
	}
	return true, nil
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
// (count -1) is treated as already drained.
func (s *Server) waitForDrain(slug string, index int, grace time.Duration) {
	deadline := time.Now().Add(grace)
	for {
		counts := s.proxy.ReplicaSessionCounts(slug)
		if index >= len(counts) || counts[index] <= 0 {
			return
		}
		if !time.Now().Before(deadline) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}
