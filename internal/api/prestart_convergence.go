package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/schedulespec"
)

type prestartSchedulePlan struct {
	producers                []*db.Schedule
	gateIDs                  []int64
	placeholder              map[string]int64
	candidates               map[string]*db.Schedule
	previousDeclarations     []*db.Schedule
	deploymentRepairRequired bool
	deploymentRepairComplete bool
}

// planPrestartSchedules projects manifest declarations without changing an
// existing schedule row. New producer schedules receive disabled placeholders
// solely to obtain stable IDs/FKs; they cannot fire until the normal manifest
// commit after the candidate producer succeeds.
func (s *Server) planPrestartSchedules(app *db.App, manifest *deploy.Manifest, digest string) (*prestartSchedulePlan, error) {
	existing, err := s.store.ListSchedulesByApp(app.ID)
	if err != nil {
		return nil, err
	}
	plan := &prestartSchedulePlan{placeholder: map[string]int64{}}
	plan.deploymentRepairRequired, err = s.store.AppDeploymentCompatibilityQuarantined(app.ID)
	if err != nil {
		return nil, err
	}
	completed := false
	defer func() {
		if !completed {
			s.removePrestartPlaceholders(plan)
		}
	}()
	byName := make(map[string]*db.Schedule, len(existing))
	for _, schedule := range existing {
		previous := *schedule
		candidate := *schedule
		plan.previousDeclarations = append(plan.previousDeclarations, &previous)
		byName[schedule.Name] = &candidate
		plan.gateIDs = append(plan.gateIDs, schedule.ID)
	}
	if manifest != nil {
		for _, spec := range manifest.Schedules {
			candidate, err := projectedManifestSchedule(app.ID, byName[spec.Name], spec)
			if err != nil {
				return nil, fmt.Errorf("schedule %q: %w", spec.Name, err)
			}
			if candidate.ID == 0 && candidate.Enabled && candidate.DeployTrigger != schedulespec.DeployTriggerNever {
				id, err := s.store.CreateSchedule(db.CreateScheduleParams{
					AppID: app.ID, Name: candidate.Name, CronExpr: candidate.CronExpr,
					CommandJSON: candidate.CommandJSON, Enabled: false,
					TimeoutSeconds: candidate.TimeoutSeconds, OverlapPolicy: candidate.OverlapPolicy,
					MissedPolicy: candidate.MissedPolicy, DeployTrigger: schedulespec.DeployTriggerNever,
					Timezone: candidate.Timezone, OnSuccess: "none", RollFallback: "defer",
				})
				if err != nil {
					return nil, err
				}
				candidate.ID = id
				plan.placeholder[candidate.Name] = id
				plan.gateIDs = append(plan.gateIDs, id)
			}
			byName[spec.Name] = candidate
		}
	}
	if err := s.populateUnsatisfiedProducers(plan, byName, digest); err != nil {
		return nil, err
	}
	plan.candidates = byName
	completed = true
	return plan, nil
}

// planRollbackPrestartSchedules projects the rollback target's immutable
// declaration snapshot onto stable current schedule IDs. Missing historical
// names receive disabled placeholders so their producer run FKs and gates are
// established before the atomic restore.
func (s *Server) planRollbackPrestartSchedules(app *db.App, deploymentID int64, digest string) (*prestartSchedulePlan, error) {
	snapshots, err := s.store.DeploymentScheduleSnapshot(deploymentID)
	if err != nil {
		return nil, err
	}
	existing, err := s.store.ListSchedulesByApp(app.ID)
	if err != nil {
		return nil, err
	}
	plan := &prestartSchedulePlan{placeholder: map[string]int64{}}
	plan.deploymentRepairRequired, err = s.store.AppDeploymentCompatibilityQuarantined(app.ID)
	if err != nil {
		return nil, err
	}
	completed := false
	defer func() {
		if !completed {
			s.removePrestartPlaceholders(plan)
		}
	}()
	currentByName := make(map[string]*db.Schedule, len(existing))
	for _, schedule := range existing {
		copy := *schedule
		plan.previousDeclarations = append(plan.previousDeclarations, &copy)
		currentByName[schedule.Name] = schedule
		plan.gateIDs = append(plan.gateIDs, schedule.ID)
	}
	desired := make(map[string]*db.Schedule, len(snapshots))
	for _, snapshot := range snapshots {
		candidate := *snapshot
		candidate.AppID = app.ID
		if err := s.validateScheduleActivationForApp(app, candidate.OnSuccess); err != nil {
			return nil, fmt.Errorf("rollback schedule %q activation topology: %w", candidate.Name, err)
		}
		if err := s.validateScheduleProducerTopology(app, candidate.DeployTrigger, candidate.OnSuccess); err != nil {
			return nil, fmt.Errorf("rollback schedule %q producer topology: %w", candidate.Name, err)
		}
		if current := currentByName[candidate.Name]; current != nil {
			candidate.ID = current.ID
		} else {
			id, err := s.store.CreateSchedule(db.CreateScheduleParams{
				AppID: app.ID, Name: candidate.Name, CronExpr: candidate.CronExpr,
				CommandJSON: candidate.CommandJSON, Enabled: false,
				TimeoutSeconds: candidate.TimeoutSeconds, OverlapPolicy: candidate.OverlapPolicy,
				MissedPolicy: candidate.MissedPolicy, DeployTrigger: schedulespec.DeployTriggerNever,
				Timezone: candidate.Timezone, OnSuccess: "none", RollFallback: "defer",
			})
			if err != nil {
				return nil, err
			}
			candidate.ID = id
			plan.placeholder[candidate.Name] = id
			plan.gateIDs = append(plan.gateIDs, id)
		}
		desired[candidate.Name] = &candidate
	}
	if err := s.populateUnsatisfiedProducers(plan, desired, digest); err != nil {
		return nil, err
	}
	plan.candidates = desired
	completed = true
	return plan, nil
}

// revalidatePrestartPlan must run after every producer gate is held. Planning
// discovers stable schedule IDs first, but an already-admitted writer may
// finish while those gates are being acquired and invalidate a previously
// satisfied producer state.
func (s *Server) revalidatePrestartPlan(plan *prestartSchedulePlan, digest string) error {
	plan.producers = nil
	return s.populateUnsatisfiedProducers(plan, plan.candidates, digest)
}

func (s *Server) restorePrestartDeclarations(plan *prestartSchedulePlan, app *db.App) error {
	restored, err := s.store.RestoreScheduleDeclarations(app.ID, plan.previousDeclarations)
	if err != nil {
		return err
	}
	for _, schedule := range restored {
		if err := s.reloadScheduler(schedule.ID, app.Slug, schedule.Name); err != nil {
			return fmt.Errorf("reload restored schedule %q: %w", schedule.Name, err)
		}
	}
	return nil
}

// convergePrestartAndFenceConsumer closes the cross-process check/use window
// between producer convergence and consumer startup. A retiring control-plane
// process may already be publishing when this deployment reaches activation.
// The shared publication lock waits for that writer; provenance is then checked
// again while the read side remains held. If the retiring writer made the
// candidate stale, release the read side, republish from the candidate bundle,
// and repeat until a satisfied state and the consumer fence are held together.
//
// The returned release must remain held through process start and replica
// persistence so the recorded consumer generation is the one startup loaded.
func (s *Server) convergePrestartAndFenceConsumer(
	plan *prestartSchedulePlan,
	digest string,
	app *db.App,
	deployment *db.Deployment,
	producerBarrierEntered *bool,
) (func(), error) {
	for {
		releaseConsumer, err := s.acquireRawConsumerBootGate(app.ID)
		if err != nil {
			return nil, fmt.Errorf("acquire consumer publication fence: %w", err)
		}
		if err := s.revalidatePrestartPlan(plan, digest); err != nil {
			releaseConsumer()
			return nil, fmt.Errorf("revalidate schedule convergence under publication fence: %w", err)
		}
		if len(plan.producers) == 0 {
			if plan.deploymentRepairRequired && !plan.deploymentRepairComplete {
				releaseConsumer()
				return nil, errors.New("the app has an unresolved failed producer barrier, but this target declares no enabled deploy-triggered producer that can prove compatible data")
			}
			quarantined, err := s.store.AppDataCompatibilityQuarantined(app.ID)
			if err != nil {
				releaseConsumer()
				return nil, fmt.Errorf("verify schedule-writer compatibility: %w", err)
			}
			if quarantined {
				releaseConsumer()
				return nil, errors.New("shared data has an unrepaired schedule-writer failure; successfully rerun or replace every failed producer before starting consumers")
			}
			return releaseConsumer, nil
		}
		releaseConsumer()

		if s.jobs == nil {
			return nil, errors.New("deploy-triggered producer runner unavailable")
		}
		if !*producerBarrierEntered {
			if err := s.store.MarkDeploymentProducerBarrierEntered(deployment.ID); err != nil {
				return nil, fmt.Errorf("record pre-start producer barrier: %w", err)
			}
			*producerBarrierEntered = true
		}
		for _, producer := range plan.producers {
			if _, err := s.jobs.RunCandidateProducerLocked(producer, app, deployment); err != nil {
				return nil, fmt.Errorf("pre-start producer %q after publication wait: %w", producer.Name, err)
			}
		}
		plan.deploymentRepairComplete = true
	}
}

func (s *Server) populateUnsatisfiedProducers(plan *prestartSchedulePlan, candidates map[string]*db.Schedule, digest string) error {
	for _, candidate := range candidates {
		if !candidate.Enabled || candidate.DeployTrigger == schedulespec.DeployTriggerNever || candidate.ID == 0 {
			continue
		}
		canonical, fingerprint, err := schedulespec.ProducerIdentity(candidate.CommandJSON)
		if err != nil {
			return err
		}
		candidate.CommandJSON = canonical
		state, stateErr := s.store.GetScheduleProducerState(candidate.ID)
		repairRequired, repairErr := s.store.ScheduleProducerRepairRequired(candidate.ID)
		if repairErr != nil {
			return repairErr
		}
		satisfied := false
		switch candidate.DeployTrigger {
		case schedulespec.DeployTriggerFirstDeploy:
			satisfied = stateErr == nil && state.PublicationGeneration > 0
		case schedulespec.DeployTriggerBundleChange:
			satisfied = stateErr == nil && state.ContentDigest == digest && state.ProducerFingerprint == fingerprint
		}
		if stateErr != nil && !errors.Is(stateErr, db.ErrNotFound) {
			return stateErr
		}
		if repairRequired || (plan.deploymentRepairRequired && !plan.deploymentRepairComplete) {
			satisfied = false
		}
		if !satisfied {
			plan.producers = append(plan.producers, candidate)
		}
	}
	sort.Slice(plan.producers, func(i, j int) bool {
		if plan.producers[i].ID == plan.producers[j].ID {
			return plan.producers[i].Name < plan.producers[j].Name
		}
		return plan.producers[i].ID < plan.producers[j].ID
	})
	return nil
}

func projectedManifestSchedule(appID int64, base *db.Schedule, spec deploy.ScheduleSpec) (*db.Schedule, error) {
	var out db.Schedule
	if base != nil {
		out = *base
	}
	out.AppID = appID
	out.Name = spec.Name
	out.CronExpr = spec.Cron
	command, err := json.Marshal(spec.Command)
	if err != nil {
		return nil, err
	}
	out.CommandJSON = string(command)
	out.Enabled = !spec.Disabled
	out.TimeoutSeconds = 3600
	if spec.TimeoutSeconds != nil {
		out.TimeoutSeconds = *spec.TimeoutSeconds
	}
	out.OverlapPolicy = spec.Overlap
	out.MissedPolicy = spec.Missed
	out.DeployTrigger, err = schedulespec.NormalizeDeployTrigger(spec.DeployTrigger)
	if err != nil {
		return nil, err
	}
	if spec.Timezone == "" {
		out.Timezone = nil
	} else {
		tz := spec.Timezone
		out.Timezone = &tz
	}
	out.OnSuccess, out.RollFallback, err = schedulespec.ValidateActivationPolicy(
		spec.OnSuccess, spec.MinRollInterval, spec.RollFallback, spec.MaxDeferAge)
	if err != nil {
		return nil, err
	}
	out.MinRollIntervalSeconds = int(spec.MinRollInterval / time.Second)
	out.MaxDeferAgeSeconds = int(spec.MaxDeferAge / time.Second)
	return &out, nil
}

func (s *Server) removePrestartPlaceholders(plan *prestartSchedulePlan) {
	if plan == nil {
		return
	}
	for _, id := range plan.placeholder {
		schedule, err := s.store.GetSchedule(id)
		if err != nil || schedule.Enabled || schedule.DeployTrigger != schedulespec.DeployTriggerNever {
			continue
		}
		_, _ = s.store.DeleteSchedulePlaceholderIfUnused(id)
	}
}
