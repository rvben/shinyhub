package api

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/rvben/shinyhub/internal/activation"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/process"
)

// rollGroupedActivation reuses deploy handoff with a new durable execution ID
// for the same bundle. Old bindings retain their worker; new clients select
// the ready candidate. Publication fencing and the app operation lock are
// held by Roll at publication; readiness releases the operation lock while the
// durable running activation fences lifecycle writers.
func (s *Server) rollGroupedActivation(ctx context.Context, app *db.App, current *db.Deployment, a *db.ScheduleActivation, unlockReadiness func() func() error) error {
	if app.Status == "waking" {
		return &activation.RetryableError{Reason: "app wake is in progress", RetryAfter: 5 * time.Second}
	}
	restarting := a.Phase == "grouped_restart"
	if app.Status != "running" && app.Status != "degraded" && !restarting {
		// A later wake starts fresh workers; discard snapshots without resurrecting
		// an intentionally stopped or hibernated application.
		for _, worker := range s.manager.AllForSlug(app.Slug) {
			if worker != nil && worker.Status == process.StatusSuspended {
				if err := s.manager.StopReplicaConfirmed(app.Slug, worker.Index); err != nil {
					return s.activationRepairError(err)
				}
			}
		}
		return activation.ErrNotNeeded
	}
	linked, err := s.store.ActivationDeploymentID(a.ID)
	if err != nil {
		return err
	}
	if linked == current.ID {
		// A crash after cutover must resume retirement, not create another roll.
		rows, err := s.store.ListDeploymentReplicas(app.ID)
		if err != nil {
			return s.activationRepairError(err)
		}
		ready := false
		liveCandidate := false
		oldGenerations := map[int64]bool{}
		for _, row := range rows {
			if row.DeploymentID == current.ID && row.DataGeneration >= a.TargetGeneration {
				info, ok := s.manager.GetGenerationReplica(app.Slug, current.ID, row.Index)
				if ok && info.Status == process.StatusRunning {
					liveCandidate = true
					ready = ready || s.proxy.GroupedGenerationReady(app.Slug, current.ID, row.Index, info.EndpointURL)
				}
			} else if row.DeploymentID != current.ID {
				oldGenerations[row.DeploymentID] = true
			}
		}
		if !ready && liveCandidate {
			if _, err := s.activateProxyGeneration(app.Slug, current.ID); err != nil {
				return s.activationRepairError(err)
			}
			ready = true
		}
		if ready {
			for id := range oldGenerations {
				if s.proxy.IsGenerationDraining(app.Slug, id) {
					s.startGenerationRetirement(app.Slug, id)
				}
			}
			return nil
		}
		if len(rows) == 0 {
			return &activation.RetryableError{Reason: "published grouped generation is awaiting its first healthy worker", RetryAfter: 5 * time.Second}
		}
		return s.activationRepairError(errors.New("published grouped activation has no verified ready worker"))
	}
	if s.proxy.HasDrainingGeneration(app.Slug) {
		return &activation.RetryableError{Reason: "previous grouped generation is still draining", RetryAfter: 5 * time.Second}
	}
	rows, err := s.store.ListDeploymentReplicas(app.ID)
	if err != nil {
		return s.activationRepairError(err)
	}
	blockingRows := rows[:0]
	for _, row := range rows {
		if row.DeploymentID != linked {
			blockingRows = append(blockingRows, row)
		}
	}
	if hasBlockingGenerationRows(blockingRows, current.ID, true) {
		return &activation.RetryableError{Reason: "previous grouped generation cleanup is unconfirmed", RetryAfter: 5 * time.Second}
	}
	projected := *app
	projected.Replicas = max(1, 1+app.WorkerWarmSpares)
	if !restarting {
		if err := s.generationHandoffCapacityCheck(&projected); err != nil {
			if a.RollFallback != "restart" {
				return err
			}
			if err := s.store.UpdateScheduleActivationProgress(a.ID, "grouped_restart", -1, 0); err != nil {
				return err
			}
			restarting = true
		}
	}
	if !current.Prepared {
		return fmt.Errorf("%w: rolling activation requires a prepared deployment", activation.ErrUnsupported)
	}
	candidate, err := s.store.ActivationDeployment(a.ID, current)
	if err != nil {
		return s.activationRepairError(err)
	}
	// An interrupted pending candidate must be proven absent before retrying
	// its execution identity. Keep its durable activation linkage.
	if candidate.Status == db.DeploymentPending {
		if !s.stopAndForgetCandidate(app.Slug, candidate.ID) {
			return s.activationRepairError(errors.New("interrupted grouped candidate cleanup is unconfirmed"))
		}
	}
	if candidate.Status == db.DeploymentFailed {
		if !s.stopAndForgetCandidate(app.Slug, candidate.ID) {
			return s.activationRepairError(errors.New("failed grouped candidate cleanup is unconfirmed"))
		}
		if err := s.store.ResetFailedActivationDeployment(a.ID, candidate.ID); err != nil {
			return s.activationRepairError(err)
		}
		candidate, err = s.store.ActivationDeployment(a.ID, current)
		if err != nil {
			return s.activationRepairError(err)
		}
	}
	if !candidate.Prepared {
		return fmt.Errorf("%w: rolling activation requires a prepared deployment", activation.ErrUnsupported)
	}
	if restarting {
		if err := s.confirmAppConsumersStopped(app); err != nil {
			return s.activationRepairError(err)
		}
		s.proxy.Deregister(app.Slug)
		// Restore grouped policy after removing the stopped pool.
		s.proxy.SetPoolMode(app.Slug, "grouped", app.WorkerGroupedSize, app.WorkerMaxWorkers)
	} else if err := s.persistDrainingGeneration(app, current); err != nil {
		return s.activationRepairError(err)
	}
	// Reserve host launch capacity only after potentially slow cleanup.
	releaseLaunch := s.manager.AcquireLaunchReservation()
	defer releaseLaunch()
	if !restarting {
		if err := s.generationHandoffCapacityCheck(&projected); err != nil {
			return err
		}
	}
	s.proxy.SetGenerationActivationToken(app.Slug, candidate.ID, candidate.ActivationToken)
	params := s.activationDeployParams(app, candidate)
	params.GenerationScoped = true
	params.GroupedHandoff = true
	params.Preparation = deploy.PrepareSkip
	params.GuardUntilAcknowledged = true
	params.LaunchReservationHeld = true
	params = s.traceDeploy(ctx, params)
	params.ReplicaStarted = func(result deploy.Result) error {
		err := s.persistStartingGenerationReplica(app, candidate, result)
		releaseLaunch()
		return err
	}
	// The durable running activation fences lifecycle writers. Releasing the
	// app lock here lets the serving generation admit demand workers while
	// candidate readiness is checked.
	run := func() (*deploy.PoolResult, error) { return s.deployRun(params) }
	if !restarting {
		relock := unlockReadiness()
		run = func() (result *deploy.PoolResult, runErr error) {
			defer func() {
				if err := relock(); err != nil {
					runErr = errors.Join(runErr, err)
				}
			}()
			return s.deployRun(params)
		}
	}
	result, err := run()
	if err == nil {
		for _, rep := range result.Replicas {
			pid, port := rep.PID, rep.Port
			err = s.store.UpsertDeploymentReplica(db.UpsertDeploymentReplicaParams{AppID: app.ID, DeploymentID: candidate.ID, Index: rep.Index, PID: &pid, Port: &port, Status: "running", Provider: rep.Provider, Tier: rep.Tier, EndpointURL: rep.EndpointURL, WorkerID: rep.WorkerID, DataGeneration: a.TargetGeneration, StartupPeakRSSBytes: rep.StartupPeakRSSBytes})
			if err != nil {
				break
			}
		}
	}
	if err == nil {
		_, err = s.activateDeployManagerGeneration(app, candidate.ID, current.ID, result, true)
	}
	if err != nil {
		cleaned := s.stopAndForgetCandidate(app.Slug, candidate.ID)
		if cleaned {
			_ = s.store.FailDeploymentWithReason(candidate.ID, err.Error())
		}
		if restarting || !cleaned {
			return s.activationRepairError(err)
		}
		if errors.Is(err, activation.ErrSuperseded) {
			return err
		}
		return s.activationRetryError(a, err)
	}
	if err := s.store.RecordDeploymentScheduleSnapshot(candidate.ID, app.ID); err != nil {
		return s.activationRepairError(err)
	}
	if err := s.promoteDeployment(candidate.ID); err != nil {
		authority, readErr := s.store.GetActiveDeploymentGeneration(app.ID)
		if readErr != nil {
			return s.activationRepairError(errors.Join(err, readErr))
		}
		if authority.DeploymentID != candidate.ID {
			cleaned := s.stopAndForgetCandidate(app.Slug, candidate.ID)
			if cleaned {
				_ = s.store.FailDeploymentWithReason(candidate.ID, err.Error())
			}
			if restarting || !cleaned {
				return s.activationRepairError(err)
			}
			return s.activationRetryError(a, err)
		}
	}
	previous, err := s.activateProxyGeneration(app.Slug, candidate.ID)
	if err != nil {
		if !restarting {
			if revertErr := s.store.RevertDeploymentActivation(candidate.ID, current.ID, err.Error()); revertErr == nil {
				if s.stopAndForgetCandidate(app.Slug, candidate.ID) {
					return s.activationRetryError(a, err)
				}
			} else {
				err = errors.Join(err, revertErr)
			}
		}
		return s.activationRepairError(err)
	}
	if previous != 0 {
		s.startGenerationRetirement(app.Slug, previous)
	}
	s.proxy.ReconcileElasticWarmSpares(app.Slug)
	_ = s.store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: app.Slug, Status: "running"})
	s.store.LogAuditEvent(db.AuditEventParams{Action: "schedule_activation_roll", ResourceType: "app", ResourceID: app.Slug, Detail: db.AuditDetail(map[string]any{"activation_id": a.ID, "deployment_id": candidate.ID, "target_generation": a.TargetGeneration, "grouped": true, "restart_fallback": restarting})})
	return nil
}
