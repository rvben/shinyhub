package api

import (
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/process"
)

// WarmShrink drains and stops every running replica above floor, marking the
// rows desired_state='warm' so reconcile, recovery, and warm-expansion can
// distinguish them from crash-stopped and manually-stopped replicas.
// app.Replicas is not touched: configured capacity is immutable here; only
// runtime state changes. Runs under the per-slug deploy lock, serializing
// against deploys, ScaleDown, and restarts.
//
// Returns (false, nil) when nothing above the (replica-clamped) floor is
// running. On a partial failure the loop stops and returns the error; rows
// already written as stopped/warm survive so a re-run can complete
// idempotently.
func (s *Server) WarmShrink(slug string, floor int, grace time.Duration) (bool, error) {
	release := s.acquireDeployLock(slug)
	defer release()

	app, err := s.store.GetAppBySlug(slug)
	if err != nil {
		return false, fmt.Errorf("warm shrink %s: get app: %w", slug, err)
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
	// the proxy pool stays contiguous down to the floor.
	for i := len(victims) - 1; i >= 0; i-- {
		v := victims[i]
		idx := v.index

		if s.proxy != nil {
			s.proxy.DrainReplica(slug, idx)
			s.waitForDrain(slug, idx, grace, s.clusteredFleetWait(app.ID, idx))
		}

		if s.manager != nil {
			if stopErr := s.manager.StopReplica(slug, idx); stopErr != nil {
				if !errors.Is(stopErr, process.ErrReplicaNotFound) {
					// A genuine stop failure: the replica may still be running.
					// Undrain it so it resumes serving and return the error. The
					// rows already written as stopped/warm in this loop stay that
					// way; a re-run will skip them (already stopped) and retry the
					// remaining victims.
					if s.proxy != nil {
						s.proxy.UndrainReplica(slug, idx)
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
		// remove only the slot that belonged to this victim.
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
