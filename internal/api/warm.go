package api

import (
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/process"
)

// WarmShrink drains and stops every running replica above floor, marking the
// rows desired_state='warm' so reconcile, recovery, and warm-expansion can
// distinguish them from crash-stopped and manually-stopped replicas.
// app.Replicas is not touched: configured capacity is immutable here; only
// runtime state changes. Runs under the per-slug deploy lock, serializing
// against deploys, ScaleDown, and restarts.
//
// Returns (false, nil) when the app is not running/degraded, or when nothing
// above the (replica-clamped) floor is running. On a partial failure the loop
// stops and returns the error; rows already written as stopped/warm survive so
// a re-run can complete idempotently.
func (s *Server) WarmShrink(slug string, floor int, grace time.Duration) (bool, error) {
	release := s.acquireDeployLock(slug)
	defer release()

	app, err := s.store.GetAppBySlug(slug)
	if err != nil {
		return false, fmt.Errorf("warm shrink %s: get app: %w", slug, err)
	}
	// Honour a concurrent stop/delete that won the lock first: a torn-down app
	// must not have its replica rows mutated.
	if app.Status != "running" && app.Status != "degraded" {
		return false, nil
	}

	// Clamp the floor to the app's configured replica count so a stale or
	// over-large floor value never stops more than what is currently configured.
	effectiveFloor := floor
	if effectiveFloor > app.Replicas {
		effectiveFloor = app.Replicas
	}

	reps, err := s.store.ListReplicas(app.ID)
	if err != nil {
		return false, fmt.Errorf("warm shrink %s: list replicas: %w", slug, err)
	}

	// Collect running victims: indices >= effectiveFloor whose current status is
	// running. Already-stopped/warm rows from a prior cycle are left untouched.
	type victim struct {
		index int
		rep   *db.Replica
	}
	var victims []victim
	for _, r := range reps {
		if r.Index >= effectiveFloor && r.Status == "running" {
			victims = append(victims, victim{index: r.Index, rep: r})
		}
	}
	if len(victims) == 0 {
		return false, nil
	}

	// Drain and stop in descending order so the highest index goes first.
	// Descending order mirrors ScaleDown's single-victim pattern and ensures
	// the proxy pool drains from the trailing end toward the floor.
	for i := len(victims) - 1; i >= 0; i-- {
		v := victims[i]
		idx := v.index

		if s.proxy != nil {
			s.proxy.DrainReplica(slug, idx)
		}
		// In clustered mode, persist the drain intent to the DB immediately after
		// the local CAS so remote pool syncers observe it and stop routing new
		// sessions to this slot before the drain wait completes. Mirrors
		// ScaleDown's pre-drain write exactly.
		if s.clustered {
			if err := s.store.SetReplicaDesiredState(app.ID, idx, "draining"); err != nil {
				// Advisory for remote instances; local drain and stop proceed.
				slog.Warn("warm shrink: set desired_state draining", "slug", slug, "index", idx, "err", err)
			}
		}
		if s.proxy != nil {
			s.waitForDrain(slug, idx, grace, s.clusteredFleetWait(app.ID, idx))
		}

		if s.manager != nil {
			if stopErr := s.manager.StopReplica(slug, idx); stopErr != nil {
				if !errors.Is(stopErr, process.ErrReplicaNotFound) {
					// A genuine stop failure: the replica may still be running.
					// Undrain it so it resumes serving, and in clustered mode revert
					// the desired_state row so remote syncers restore full routing.
					// Rows already written as stopped/warm in earlier loop iterations
					// survive; a re-run skips them and retries the remaining victims.
					if s.proxy != nil {
						s.proxy.UndrainReplica(slug, idx)
					}
					if s.clustered {
						if rerr := s.store.SetReplicaDesiredState(app.ID, idx, "running"); rerr != nil {
							slog.Warn("warm shrink: revert desired_state running", "slug", slug, "index", idx, "err", rerr)
						}
					}
					return false, fmt.Errorf("warm shrink %s: stop replica %d: %w", slug, idx, stopErr)
				}
				// ErrReplicaNotFound is benign: the process was already gone.
				slog.Warn("warm shrink: stop replica not found", "slug", slug, "index", idx, "err", stopErr)
			}
		}

		// Deregister the proxy slot for this victim. ScaleDown relies on
		// SetPoolSize to implicitly truncate trailing slots, but WarmShrink does
		// not shrink the pool size (app.Replicas is unchanged, and the pool must
		// keep its slots so a later warm-expansion can register new backends at
		// the same indices without resizing). We use DeregisterReplicaIfTarget to
		// null out only the slot that belonged to this victim.
		if s.proxy != nil {
			url := s.proxy.ReplicaTargetURL(slug, idx)
			if url != "" {
				s.proxy.DeregisterReplicaIfTarget(slug, idx, url)
			}
		}

		// Persist the stopped/warm state. Preserve the row's existing metadata
		// (provider, tier, version, deployment ID) so the row remains a faithful
		// record of what was running and warm-expansion can boot the same binary.
		if err := s.store.UpsertReplica(db.UpsertReplicaParams{
			AppID:        app.ID,
			Index:        idx,
			PID:          v.rep.PID,
			Port:         v.rep.Port,
			Status:       "stopped",
			Provider:     v.rep.Provider,
			Tier:         v.rep.Tier,
			EndpointURL:  v.rep.EndpointURL,
			WorkerID:     v.rep.WorkerID,
			AppVersion:   v.rep.AppVersion,
			DesiredState: db.ReplicaDesiredWarm,
			DeploymentID: v.rep.DeploymentID,
		}); err != nil {
			return false, fmt.Errorf("warm shrink %s: upsert replica %d: %w", slug, idx, err)
		}
	}

	// Single audit event after the whole batch completes.
	s.store.LogAuditEvent(db.AuditEventParams{
		Action:       "warm_shrink",
		ResourceType: "app",
		ResourceID:   slug,
		Detail:       fmt.Sprintf(`{"from":%d,"to":%d}`, app.Replicas, effectiveFloor),
	})

	return true, nil
}

// WarmExpand boots every warm-parked replica (desired_state='warm') back to
// running, restoring full configured capacity after a warm shrink. Runs
// under the per-slug deploy lock. Manual stops (desired_state='stopped')
// are never touched. A victim that fails to boot is handed to the watchdog
// (row marked crashed/running) and the error is returned alongside any
// successfully restored capacity. Returns (false, nil) when no warm rows
// exist.
func (s *Server) WarmExpand(slug string) (bool, error) {
	release := s.acquireDeployLock(slug)
	defer release()

	app, err := s.store.GetAppBySlug(slug)
	if err != nil {
		return false, fmt.Errorf("warm expand %s: get app: %w", slug, err)
	}

	reps, err := s.store.ListReplicas(app.ID)
	if err != nil {
		return false, fmt.Errorf("warm expand %s: list replicas: %w", slug, err)
	}

	// Collect warm victims: stopped rows that were parked by WarmShrink.
	// Manual stops (desired_state='stopped') are deliberately excluded.
	type victim struct {
		index int
		rep   *db.Replica
	}
	var victims []victim
	for _, r := range reps {
		if r.DesiredState == db.ReplicaDesiredWarm && r.Status == "stopped" {
			victims = append(victims, victim{index: r.Index, rep: r})
		}
	}
	if len(victims) == 0 {
		return false, nil
	}

	// Count live replicas before expansion for the audit detail.
	liveBefore := 0
	for _, r := range reps {
		if r.Status == "running" {
			liveBefore++
		}
	}

	deployments, err := s.store.ListDeployments(app.ID)
	if err != nil || len(deployments) == 0 {
		return false, fmt.Errorf("warm expand %s: no deployments", slug)
	}
	current := deployments[0]

	// Build deploy params using the same field set as ScaleUp, reusing the
	// current deployment's bundle dir and all resource/session config from the
	// live app row. withTierPlacement maps each index to its configured tier.
	defaultMem, defaultCPU := s.cfg.Runtime.DefaultResourcesForApp(app)
	p := s.withTierPlacement(deploy.Params{
		Slug:                  slug,
		BundleDir:             current.BundleDir,
		Replicas:              app.Replicas,
		Manager:               s.manager,
		Proxy:                 s.proxy,
		MemoryLimitMB:         deploy.ResolveMemoryLimitMB(app.MemoryLimitMB, defaultMem),
		CPUQuotaPercent:       deploy.ResolveCPUQuotaPercent(app.CPUQuotaPercent, defaultCPU),
		MaxSessionsPerReplica: deploy.ResolveMaxSessionsPerReplica(app.MaxSessionsPerReplica, s.cfg.Runtime.DefaultMaxSessionsPerReplica),
		IdentityHeaders:       deploy.ResolveIdentityHeaders(app.IdentityHeaders, s.cfg.Auth.IdentityHeadersEnabled()),
		ContentDigest:         current.ContentDigest,
		DeploymentID:          current.ID,
		AppVersion:            current.Version,
	}, app)

	// Boot warm victims in ascending index order, mirroring the order a full
	// deploy would use. Continue past individual failures so every victim gets
	// an attempt; accumulate the first error to surface to the caller.
	var firstErr error
	anyRestored := false
	for _, v := range victims {
		idx := v.index
		r, bootErr := s.deployReplica(p, idx)
		if bootErr != nil {
			// Hand the failed victim to the watchdog: crashed/running causes
			// reconcileReplicas to restart it on the next tick.
			if upsertErr := s.store.UpsertReplica(db.UpsertReplicaParams{
				AppID:        app.ID,
				Index:        idx,
				Status:       "crashed",
				Provider:     v.rep.Provider,
				Tier:         v.rep.Tier,
				AppVersion:   v.rep.AppVersion,
				DesiredState: "running",
				DeploymentID: v.rep.DeploymentID,
			}); upsertErr != nil {
				slog.Warn("warm expand: persist crashed victim", "slug", slug, "index", idx, "err", upsertErr)
			}
			if firstErr == nil {
				firstErr = fmt.Errorf("warm expand %s: boot replica %d: %w", slug, idx, bootErr)
			}
			continue
		}

		// Boot succeeded: register the backend and persist the running row.
		if s.proxy != nil {
			if regErr := s.proxy.RegisterReplica(slug, idx, r.EndpointURL, nil, 0); regErr != nil {
				slog.Warn("warm expand: register replica", "slug", slug, "index", idx, "err", regErr)
			}
		}
		pid, port := r.PID, r.Port
		depID := current.ID
		if upsertErr := s.store.UpsertReplica(db.UpsertReplicaParams{
			AppID:        app.ID,
			Index:        idx,
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
		}); upsertErr != nil {
			// The process is running but the row is stale - log and continue so
			// the watchdog can observe and reconcile.
			slog.Warn("warm expand: persist running replica", "slug", slug, "index", idx, "err", upsertErr)
		}
		anyRestored = true
	}

	// Recount live replicas after expansion for the audit detail.
	liveAfter := liveBefore
	if anyRestored {
		updatedReps, listErr := s.store.ListReplicas(app.ID)
		if listErr == nil {
			liveAfter = 0
			for _, r := range updatedReps {
				if r.Status == "running" {
					liveAfter++
				}
			}
		}
	}

	// Emit one audit event regardless of partial failures, so operators can
	// observe the expansion attempt and its result.
	s.store.LogAuditEvent(db.AuditEventParams{
		Action:       "warm_expand",
		ResourceType: "app",
		ResourceID:   slug,
		Detail:       fmt.Sprintf(`{"from":%d,"to":%d}`, liveBefore, liveAfter),
	})

	return anyRestored, firstErr
}
