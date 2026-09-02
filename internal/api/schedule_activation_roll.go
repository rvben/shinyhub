package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"syscall"
	"time"

	"github.com/rvben/shinyhub/internal/activation"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/schedulespec"
	gopsmem "github.com/shirou/gopsutil/v4/mem"
)

// Roll implements activation.Runner. It serializes with every other app
// lifecycle mutation, brings up one healthy surge replica, then replaces stale
// canonical slots one at a time. The configured replica count is never changed:
// max_replicas remains a steady-state ceiling and activation owns max_surge=1.
func (s *Server) Roll(ctx context.Context, a *db.ScheduleActivation) error {
	if a == nil || a.AppID == nil {
		return activation.ErrTargetDeleted
	}
	release := s.acquireDeployLock(a.AppSlug)
	defer release()

	app, err := s.store.GetAppByID(*a.AppID)
	if errors.Is(err, db.ErrNotFound) {
		return activation.ErrTargetDeleted
	}
	if err != nil {
		return fmt.Errorf("load activation target: %w", err)
	}
	if app.Slug != a.AppSlug {
		return fmt.Errorf("%w: app identity changed", activation.ErrTargetDeleted)
	}
	if err := s.guardCompatibilityQuarantine(app.ID, "scheduled data activation"); err != nil {
		return fmt.Errorf("%w: %v", activation.ErrUnsupported, err)
	}
	releaseConsumerBoot, gateErr := s.acquireConsumerBootGate(app.ID)
	if gateErr != nil {
		return fmt.Errorf("acquire activation publication fence: %w", gateErr)
	}
	defer releaseConsumerBoot()
	if a.SourceContentDigest != "" && a.SourceProducerFingerprint != "" {
		publication, publicationErr := s.store.GetAppDataPublication(app.ID)
		if errors.Is(publicationErr, db.ErrNotFound) {
			return activation.ErrSuperseded
		}
		if publicationErr != nil {
			return fmt.Errorf("load activation data publication: %w", publicationErr)
		}
		if a.ScheduleRunID == nil || publication.Generation != a.TargetGeneration ||
			publication.ScheduleRunID != *a.ScheduleRunID ||
			publication.ProducerContentDigest != a.SourceContentDigest ||
			publication.ProducerFingerprint != a.SourceProducerFingerprint {
			return activation.ErrSuperseded
		}
		if (publication.ProducerDeploymentID == nil) != (a.SourceDeploymentID == nil) ||
			(publication.ProducerDeploymentID != nil && *publication.ProducerDeploymentID != *a.SourceDeploymentID) {
			return activation.ErrSuperseded
		}
	}
	if err := s.validateScheduleActivationForApp(app, "roll"); err != nil {
		return fmt.Errorf("%w: %v", activation.ErrUnsupported, err)
	}
	if s.manager == nil || s.proxy == nil {
		return fmt.Errorf("%w: local process manager and proxy are required", activation.ErrUnsupported)
	}
	if app.Replicas <= 0 {
		return activation.ErrNotNeeded
	}
	targetReplicas := app.Replicas
	if a.SurgeIndex >= 0 {
		targetReplicas = a.SurgeIndex
		if app.Replicas != targetReplicas {
			return s.activationRepairError(fmt.Errorf(
				"activation layout changed during repair: durable replicas=%d current replicas=%d",
				targetReplicas, app.Replicas,
			))
		}
	}
	surgeIndex := targetReplicas
	deployments, err := s.store.ListDeployments(app.ID)
	if err != nil {
		return fmt.Errorf("list deployments: %w", err)
	}
	if len(deployments) == 0 {
		return fmt.Errorf("%w: app has no deployment", activation.ErrUnsupported)
	}
	current := deployments[0]
	if a.SourceContentDigest != "" && current.ContentDigest != a.SourceContentDigest {
		return activation.ErrSuperseded
	}
	if a.SourceProducerFingerprint != "" && a.ScheduleID != nil {
		schedule, err := s.store.GetSchedule(*a.ScheduleID)
		if errors.Is(err, db.ErrNotFound) {
			return activation.ErrTargetDeleted
		}
		if err != nil {
			return fmt.Errorf("load activation producer: %w", err)
		}
		_, currentFingerprint, err := schedulespec.ProducerIdentity(schedule.CommandJSON)
		if err != nil {
			return fmt.Errorf("resolve activation producer: %w", err)
		}
		if currentFingerprint != a.SourceProducerFingerprint {
			return activation.ErrSuperseded
		}
	}

	replicaRows, err := s.store.ListReplicas(app.ID)
	if err != nil {
		return fmt.Errorf("list replicas: %w", err)
	}
	byIndex := make(map[int]*db.Replica, len(replicaRows))
	for _, replica := range replicaRows {
		byIndex[replica.Index] = replica
	}
	if app.Status == "waking" {
		return &activation.RetryableError{Reason: "app wake is in progress", RetryAfter: 5 * time.Second}
	}
	if app.Status != "running" && app.Status != "degraded" {
		// Never resurrect an intentionally stopped or hibernated app. Frozen warm
		// snapshots still have to be invalidated so a later wake cannot restore
		// the pre-refresh process image.
		for i := 0; i < app.Replicas; i++ {
			if row := byIndex[i]; row != nil && (row.DesiredState == db.ReplicaDesiredWarm || row.Status == db.ReplicaStatusSuspended) {
				if err := s.invalidateDormantActivationReplica(app, a, i); err != nil {
					return s.activationRetryError(a, fmt.Errorf("invalidate dormant replica %d: %w", i, err))
				}
			}
		}
		return activation.ErrNotNeeded
	}
	var staleRunning, dormant []int
	for i := 0; i < app.Replicas; i++ {
		row := byIndex[i]
		if s.activationCanonicalCurrent(app, current, a, i, row) {
			continue
		}
		if row != nil && row.DesiredState == db.ReplicaDesiredWarm {
			dormant = append(dormant, i)
		} else {
			// A configured canonical slot with running intent must be restored even
			// when a previous activation attempt stopped it before checkpointing.
			staleRunning = append(staleRunning, i)
		}
	}
	if len(staleRunning) == 0 && len(dormant) == 0 {
		// Recovery may find every canonical slot checkpointed while the transient
		// surge still exists (for example a crash during final cleanup).
		_, surgePresent := s.manager.GetReplica(app.Slug, surgeIndex)
		surgeRow := byIndex[surgeIndex]
		if surgePresent || (surgeRow != nil && surgeRow.ActivationID != nil) {
			if err := s.retireActivationSurge(app, surgeIndex); err != nil {
				return s.activationRepairError(err)
			}
			return nil
		}
		return activation.ErrNotNeeded
	}

	params := s.activationDeployParams(app, current)
	var releaseLaunch func()
	releaseLaunchReservation := func() {
		if releaseLaunch != nil {
			releaseLaunch()
			releaseLaunch = nil
		}
	}
	params.ReplicaStarted = func(result deploy.Result) error {
		err := s.persistStartingActivationReplica(app, current, &result, a)
		// Capacity is reserved only through concrete runtime creation and its
		// durable identity checkpoint. Readiness may legitimately take many
		// minutes and must not block unrelated app starts for that entire time.
		releaseLaunchReservation()
		return err
	}

	// Invalidate parked/suspended replicas. Resuming an old snapshot later would
	// reintroduce stale module-scope data even after every serving process rolled.
	for _, index := range dormant {
		if err := s.invalidateDormantActivationReplica(app, a, index); err != nil {
			return s.activationRetryError(a, fmt.Errorf("invalidate dormant replica %d: %w", index, err))
		}
	}
	if len(staleRunning) == 0 {
		return nil
	}

	params = activationSurgePlacement(params, s.cfg.Runtime.TierOrder())
	// Every activation-owned native start, including canonical replacements on
	// recovery attempts that already have a valid surge, must persist its PID
	// before app code can exec.
	params.GuardUntilAcknowledged = true
	needSurge := !s.activationSurgeCurrent(app, current, a, surgeIndex, byIndex[surgeIndex])
	if needSurge {
		if err := s.discardInvalidActivationSurge(app, surgeIndex); err != nil {
			return s.activationRepairError(fmt.Errorf("discard stale surge: %w", err))
		}
		if err := s.activationCapacityCheck(app, staleRunning[0]); err != nil {
			if a.RollFallback == "restart" {
				return s.restartActivationPool(ctx, app, current, a, err)
			}
			return err
		}
	}
	if err := s.store.UpdateScheduleActivationProgress(a.ID, "starting_surge", surgeIndex, 0); err != nil {
		if !needSurge {
			return s.activationRepairError(err)
		}
		return s.activationRetryError(a, err)
	}
	a.SurgeIndex = surgeIndex
	if needSurge {
		// Serialize the final capacity sample through runtime start with every
		// other Manager.Start. This turns host capacity into an admission decision
		// instead of a racy observation.
		releaseLaunch = s.manager.AcquireLaunchReservation()
		params.LaunchReservationHeld = true
		if err := s.activationCapacityCheck(app, staleRunning[0]); err != nil {
			releaseLaunch()
			if a.RollFallback == "restart" {
				return s.restartActivationPool(ctx, app, current, a, err)
			}
			return err
		}
	}
	s.proxy.SetPoolSize(app.Slug, app.Replicas+1)
	surgeResult, err := s.ensureActivationSurge(params, app, current, a, surgeIndex)
	// Manager.Start failures occur before ReplicaStarted can release the
	// reservation. Ensure every such path still unlocks the host admission gate.
	releaseLaunchReservation()
	if err != nil {
		s.proxy.SetPoolSize(app.Slug, app.Replicas)
		if confirmed, clearErr := s.clearConfirmedActivationStart(app.ID, surgeIndex, err); confirmed {
			if clearErr != nil {
				return s.activationRepairError(fmt.Errorf("checkpoint stopped surge replica: %w", clearErr))
			}
			return s.activationRetryError(a, fmt.Errorf("start surge replica: %w", err))
		}
		if errors.Is(err, process.ErrStopUnconfirmed) {
			return s.activationRepairError(fmt.Errorf("start surge replica: %w", err))
		}
		return s.activationRetryError(a, fmt.Errorf("start surge replica: %w", err))
	}
	if err := s.persistActivationReplica(app, current, surgeResult, a); err != nil {
		return s.activationRepairError(fmt.Errorf("persist surge replica: %w", err))
	}
	// The surge reservation ended with ensureActivationSurge. Canonical
	// replacements reclaim one existing slot and must participate normally in
	// any reservation held by another concurrent launch.
	params.LaunchReservationHeld = false
	if err := s.store.UpdateScheduleActivationProgress(a.ID, "surge_ready", surgeIndex, 0); err != nil {
		return s.activationRepairError(err)
	}

	for _, index := range staleRunning {
		if err := ctx.Err(); err != nil {
			return &activation.RepairRequiredError{Reason: "activation interrupted: " + err.Error(), RetryAfter: time.Second}
		}
		// A prior attempt may have committed this slot before failing later.
		rows, err := s.store.ListReplicas(app.ID)
		if err != nil {
			return s.activationRepairError(err)
		}
		alreadyCurrent := false
		for _, row := range rows {
			if row.Index == index && s.activationCanonicalCurrent(app, current, a, index, row) {
				alreadyCurrent = true
				break
			}
		}
		if alreadyCurrent {
			continue
		}

		if err := s.store.UpdateScheduleActivationProgress(a.ID, "draining_slot", surgeIndex, index); err != nil {
			return s.activationRepairError(err)
		}
		oldTarget := s.proxy.ReplicaTargetURL(app.Slug, index)
		if oldTarget != "" {
			s.proxy.DrainReplica(app.Slug, index)
			grace := s.cfg.Server.DrainTimeout
			if grace <= 0 {
				grace = 60 * time.Second
			}
			s.waitForDrain(app.Slug, index, grace, nil)
			detached := s.proxy.DetachDrainedReplica(app.Slug, index, oldTarget, false)
			if !detached.Matched {
				return s.activationRepairError(fmt.Errorf("detach replica %d: route changed during drain", index))
			}
			if !detached.Detached {
				detached = s.proxy.DetachDrainedReplica(app.Slug, index, oldTarget, true)
				slog.Warn("schedule activation forced replica drain", "activation_id", a.ID, "app", app.Slug, "index", index, "active_connections", detached.ActiveConns)
			}
		}
		if err := s.confirmActivationReplicaStopped(app, index); err != nil {
			return s.activationRepairError(fmt.Errorf("stop replica %d: %w", index, err))
		}
		if err := s.store.UpdateScheduleActivationProgress(a.ID, "starting_slot", surgeIndex, index); err != nil {
			return s.activationRepairError(err)
		}
		result, err := s.deployReplica(params, index)
		if err != nil {
			confirmed, clearErr := s.clearConfirmedActivationStart(app.ID, index, err)
			if !confirmed {
				// Cleanup proof is the authority here, not the shape of the cleanup
				// error. Runtime signal failures are not required to wrap
				// ErrStopUnconfirmed, but they can still leave this exact process
				// alive. Preserve its durable identity for repair and crash recovery.
				return s.activationRepairError(fmt.Errorf("boot replacement %d: %w", index, err))
			}
			if clearErr != nil {
				return s.activationRepairError(fmt.Errorf("checkpoint stopped replacement %d: %w", index, clearErr))
			}
			_ = s.store.UpsertReplica(db.UpsertReplicaParams{
				AppID: app.ID, Index: index, Status: "crashed", Tier: tierForActivationIndex(params, index),
				AppVersion: current.Version, DeploymentID: &current.ID, Reason: "activation replacement failed: " + err.Error(),
			})
			return s.activationRepairError(fmt.Errorf("boot replacement %d: %w", index, err))
		}
		if err := s.persistActivationReplica(app, current, result, a); err != nil {
			return s.activationRepairError(err)
		}
	}
	rows, err := s.store.ListReplicas(app.ID)
	if err != nil {
		return s.activationRepairError(fmt.Errorf("verify canonical replicas: %w", err))
	}
	verified := make(map[int]*db.Replica, app.Replicas)
	for _, row := range rows {
		verified[row.Index] = row
	}
	for index := 0; index < app.Replicas; index++ {
		if !s.activationCanonicalCurrent(app, current, a, index, verified[index]) {
			return s.activationRepairError(fmt.Errorf("canonical replica %d is not healthy, routed, and current", index))
		}
	}

	if err := s.store.UpdateScheduleActivationProgress(a.ID, "retiring_surge", surgeIndex, app.Replicas); err != nil {
		return s.activationRepairError(err)
	}
	if err := s.retireActivationSurge(app, surgeIndex); err != nil {
		return s.activationRepairError(err)
	}
	s.store.LogAuditEvent(db.AuditEventParams{
		Action: "schedule_activation_roll", ResourceType: "app", ResourceID: app.Slug,
		Detail: fmt.Sprintf(`{"activation_id":%d,"schedule_run_id":%d,"target_generation":%d}`,
			a.ID, derefActivationRunID(a.ScheduleRunID), a.TargetGeneration),
	})
	return nil
}

// restartActivationPool is the explicitly disruptive fallback for a roll
// whose surge cannot be admitted. It stops the old pool before starting the
// target generation, trading an availability gap for bounded data freshness.
// Once stopping begins every failure is repair-required: recovery must keep
// retrying until a complete current pool is serving again.
func (s *Server) restartActivationPool(ctx context.Context, app *db.App, current *db.Deployment, a *db.ScheduleActivation, capacityErr error) error {
	if err := ctx.Err(); err != nil {
		return &activation.RetryableError{Reason: "restart fallback interrupted before stop: " + err.Error(), RetryAfter: time.Second}
	}
	slog.Warn("schedule activation using stop-first fallback",
		"activation_id", a.ID, "app", app.Slug, "reason", capacityErr.Error())
	if err := s.store.UpdateScheduleActivationProgress(a.ID, "stopping_pool", -1, 0); err != nil {
		return s.activationRetryError(a, err)
	}
	for index := 0; index < app.Replicas; index++ {
		if err := s.manager.StopReplicaConfirmed(app.Slug, index); err != nil && !errors.Is(err, process.ErrReplicaNotFound) {
			return s.activationRepairError(fmt.Errorf("stop replica %d for restart fallback: %w", index, err))
		}
	}
	s.proxy.Deregister(app.Slug)
	if err := s.store.UpdateScheduleActivationProgress(a.ID, "starting_pool", -1, 0); err != nil {
		return s.activationRepairError(err)
	}

	params := s.activationDeployParams(app, current)
	params.Preparation = activationPreparation(current.Prepared)
	params.GuardUntilAcknowledged = true
	params.ReplicaStarted = func(result deploy.Result) error {
		return s.persistStartingActivationReplica(app, current, &result, a)
	}
	result, err := s.deployRun(params)
	if err != nil {
		return s.activationRepairError(fmt.Errorf("start pool after restart fallback: %w", err))
	}
	if !current.Prepared {
		if err := s.store.MarkDeploymentPrepared(current.ID); err != nil {
			slog.Warn("activation restart: record preparation state", "app", app.Slug, "err", err)
		}
	}
	for i := range result.Replicas {
		if err := s.persistActivationReplica(app, current, &result.Replicas[i], a); err != nil {
			return s.activationRepairError(err)
		}
	}
	rows, err := s.store.ListReplicas(app.ID)
	if err != nil {
		return s.activationRepairError(fmt.Errorf("verify restarted pool: %w", err))
	}
	byIndex := make(map[int]*db.Replica, len(rows))
	for _, row := range rows {
		byIndex[row.Index] = row
	}
	for index := 0; index < app.Replicas; index++ {
		if !s.activationCanonicalCurrent(app, current, a, index, byIndex[index]) {
			return s.activationRepairError(fmt.Errorf("restarted replica %d is not healthy, routed, and current", index))
		}
	}
	if err := s.store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: app.Slug, Status: "running"}); err != nil {
		slog.Error("activation restart: persist running status", "app", app.Slug, "err", err)
	}
	s.store.LogAuditEvent(db.AuditEventParams{
		Action: "schedule_activation_restart", ResourceType: "app", ResourceID: app.Slug,
		Detail: fmt.Sprintf(`{"activation_id":%d,"schedule_run_id":%d,"target_generation":%d,"capacity_reason":%q}`,
			a.ID, derefActivationRunID(a.ScheduleRunID), a.TargetGeneration, capacityErr.Error()),
	})
	return nil
}

func (s *Server) activationDeployParams(app *db.App, current *db.Deployment) deploy.Params {
	defaultMem, defaultCPU := s.cfg.Runtime.DefaultResourcesForApp(app)
	p := deploy.Params{
		Slug: app.Slug, BundleDir: current.BundleDir, Replicas: app.Replicas,
		Manager: s.manager, Proxy: s.proxy,
		MemoryLimitMB:         deploy.ResolveMemoryLimitMB(app.MemoryLimitMB, defaultMem),
		CPUQuotaPercent:       deploy.ResolveCPUQuotaPercent(app.CPUQuotaPercent, defaultCPU),
		MaxSessionsPerReplica: deploy.ResolveMaxSessionsPerReplica(app.MaxSessionsPerReplica, s.cfg.Runtime.DefaultMaxSessionsPerReplica),
		IdentityHeaders:       deploy.ResolveIdentityHeaders(app.IdentityHeaders, s.cfg.Auth.IdentityHeadersEnabled()),
		ContentDigest:         current.ContentDigest, DeploymentID: current.ID, AppVersion: current.Version,
	}
	return s.withTierPlacement(p, app)
}

func activationSurgePlacement(p deploy.Params, tierOrder []string) deploy.Params {
	if len(p.Placement) == 0 {
		p.Replicas++
		return p
	}
	copyPlacement := make(map[string]int, len(p.Placement))
	for tier, count := range p.Placement {
		copyPlacement[tier] = count
	}
	tier := lastPopulatedTier(copyPlacement, tierOrder)
	if tier == "" {
		tier = p.DefaultTier
	}
	copyPlacement[tier]++
	p.Placement = copyPlacement
	return p
}

func tierForActivationIndex(p deploy.Params, index int) string {
	if len(p.Placement) == 0 {
		return p.DefaultTier
	}
	remaining := index
	for _, tier := range p.TierOrder {
		if remaining < p.Placement[tier] {
			return tier
		}
		remaining -= p.Placement[tier]
	}
	return p.DefaultTier
}

func (s *Server) activationCapacityCheck(app *db.App, sampleIndex int) error {
	estimate := s.activationSurgeMemoryEstimate(app, sampleIndex)
	neededMB := estimate.MemoryMB
	if neededMB <= 0 {
		return &activation.CapacityError{Reason: "surge memory is unknown; configure memory_limit_mb or enable runtime metrics", RetryAfter: time.Minute}
	}
	vm, err := gopsmem.VirtualMemory()
	if err != nil || vm == nil {
		return &activation.CapacityError{Reason: "host available memory could not be measured", RetryAfter: time.Minute}
	}
	availableMB := int(vm.Available / (1024 * 1024))
	requiredMB := s.cfg.MinAvailableMemoryMB() + neededMB
	if availableMB < requiredMB {
		return &activation.CapacityError{
			Reason:     fmt.Sprintf("%d MiB available; need %d MiB for one surge replica plus the %d MiB host floor (%s)", availableMB, requiredMB, s.cfg.MinAvailableMemoryMB(), estimate.Provenance),
			RetryAfter: time.Minute,
		}
	}
	return nil
}

// generationHandoffCapacityCheck admits the complete candidate pool beside
// the currently active one. Unlike a rolling activation, a version handoff
// needs every target replica healthy before publication, so budgeting a single
// surge replica would still leave the host vulnerable to an OOM mid-start.
func (s *Server) generationHandoffCapacityCheck(app *db.App) error {
	estimate := s.activationSurgeMemoryEstimate(app, 0)
	if estimate.MemoryMB <= 0 {
		return &activation.CapacityError{Reason: "parallel-generation memory is unknown; configure memory_limit_mb or enable runtime metrics", RetryAfter: time.Minute}
	}
	replicas := app.Replicas
	if replicas < 1 {
		replicas = 1
	}
	neededMB := estimate.MemoryMB * replicas
	availableMB, err := s.availableMemoryMB()
	if err != nil {
		return &activation.CapacityError{Reason: "host available memory could not be measured", RetryAfter: time.Minute}
	}
	requiredMB := s.cfg.MinAvailableMemoryMB() + neededMB
	if availableMB < requiredMB {
		return &activation.CapacityError{
			Reason: fmt.Sprintf("%d MiB available; need %d MiB for %d parallel-generation replicas plus the %d MiB host floor (%s)",
				availableMB, requiredMB, replicas, s.cfg.MinAvailableMemoryMB(), estimate.Provenance),
			RetryAfter: time.Minute,
		}
	}
	return nil
}

func hostAvailableMemoryMB() (int, error) {
	vm, err := gopsmem.VirtualMemory()
	if err != nil {
		return 0, err
	}
	if vm == nil {
		return 0, errors.New("host memory sample is nil")
	}
	return int(vm.Available / (1024 * 1024)), nil
}

func (s *Server) rollFeasibilityAdvisory(app *db.App, onSuccess, fallback string) *string {
	if onSuccess != "roll" {
		return nil
	}
	estimate := s.activationSurgeMemoryEstimate(app, 0)
	neededMB := estimate.MemoryMB
	if neededMB <= 0 {
		msg := "Surge-roll feasibility could not be checked because replica memory is unknown; configure memory_limit_mb or keep runtime metrics available."
		return &msg
	}
	vm, err := gopsmem.VirtualMemory()
	if err != nil || vm == nil {
		msg := "Surge-roll feasibility could not be checked because host available memory could not be measured."
		return &msg
	}
	availableMB := int(vm.Available / (1024 * 1024))
	msg := formatRollFeasibilityAdvisory(neededMB, availableMB, s.cfg.MinAvailableMemoryMB(), fallback)
	if msg == "" {
		return nil
	}
	msg += " Memory estimate: " + estimate.Provenance + "."
	return &msg
}

func formatRollFeasibilityAdvisory(neededMB, availableMB, hostFloorMB int, fallback string) string {
	requiredMB := neededMB + hostFloorMB
	if neededMB <= 0 || availableMB >= requiredMB {
		return ""
	}
	msg := fmt.Sprintf("Surge roll is currently infeasible: %d MiB available; need %d MiB for one surge replica plus the %d MiB host floor.",
		availableMB, requiredMB, hostFloorMB)
	if fallback == "restart" {
		msg += " The configured restart fallback will replace replicas in place; a single-replica app will be unavailable while it starts."
	} else {
		msg += " Activation will defer until capacity changes; set roll_fallback=restart if a bounded availability gap is preferable to stale data."
	}
	return msg
}

func (s *Server) activationSurgeMemoryMB(app *db.App, preferredIndex int) int {
	return s.activationSurgeMemoryEstimate(app, preferredIndex).MemoryMB
}

type surgeMemoryEstimate struct {
	MemoryMB   int
	Provenance string
}

func (s *Server) activationSurgeMemoryEstimate(app *db.App, preferredIndex int) surgeMemoryEstimate {
	neededMB, _ := s.cfg.Runtime.DefaultResourcesForApp(app)
	neededMB = deploy.ResolveMemoryLimitMB(app.MemoryLimitMB, neededMB)
	if neededMB > 0 {
		return surgeMemoryEstimate{MemoryMB: neededMB, Provenance: fmt.Sprintf("configured memory limit of %d MiB", neededMB)}
	}

	var baselineBytes int64
	provenance := ""
	if s.store != nil {
		replicas, err := s.store.ListReplicas(app.ID)
		if err == nil {
			for _, replica := range replicas {
				if replica.Index >= 0 && replica.Index < app.Replicas && replica.StartupPeakRSSBytes > baselineBytes {
					baselineBytes = replica.StartupPeakRSSBytes
					provenance = "persisted healthy-start RSS peak"
				}
			}
		}
	}

	indices := make([]int, 0, app.Replicas)
	if preferredIndex >= 0 && preferredIndex < app.Replicas {
		indices = append(indices, preferredIndex)
	}
	for index := 0; index < app.Replicas; index++ {
		if index != preferredIndex {
			indices = append(indices, index)
		}
	}
	if s.sampler != nil && s.manager != nil {
		for _, index := range indices {
			if handle, ok := s.manager.HandleReplica(app.Slug, index); ok {
				if stats, err := s.sampler.Sample(handle); err == nil {
					if stats.RSSBytes > baselineBytes {
						baselineBytes = stats.RSSBytes
						provenance = "current live RSS"
					}
				}
			}
		}
	}
	if baselineBytes <= 0 {
		return surgeMemoryEstimate{}
	}
	// Even an observed startup peak is a sample, not a hard cap. Retain the
	// existing 25% plus 64 MiB uncertainty margin for allocator, workload, and
	// sampling variance rather than admitting exactly at the historic peak.
	baselineMB := int((baselineBytes + (1024*1024 - 1)) / (1024 * 1024))
	return surgeMemoryEstimate{
		MemoryMB:   baselineMB + baselineMB/4 + 64,
		Provenance: fmt.Sprintf("%s of %d MiB plus the 25%% + 64 MiB safety margin", provenance, baselineMB),
	}
}

func (s *Server) activationCanonicalCurrent(app *db.App, current *db.Deployment, a *db.ScheduleActivation, index int, row *db.Replica) bool {
	if row == nil || row.Status != db.ReplicaStatusRunning || row.DataGeneration < a.TargetGeneration ||
		row.DeploymentID == nil || *row.DeploymentID != current.ID {
		return false
	}
	info, ok := s.manager.GetReplica(app.Slug, index)
	if !ok || info.Status != process.StatusRunning || (info.DeploymentID != 0 && info.DeploymentID != current.ID) || info.EndpointURL == "" {
		return false
	}
	return s.proxy.ReplicaTargetURL(app.Slug, index) == info.EndpointURL
}

func (s *Server) activationSurgeCurrent(app *db.App, current *db.Deployment, a *db.ScheduleActivation, surgeIndex int, row *db.Replica) bool {
	if row == nil || row.ActivationID == nil || *row.ActivationID != a.ID || row.DataGeneration != a.TargetGeneration {
		return false
	}
	return s.activationCanonicalCurrent(app, current, a, surgeIndex, row)
}

func (s *Server) ensureActivationSurge(p deploy.Params, app *db.App, current *db.Deployment, a *db.ScheduleActivation, surgeIndex int) (*deploy.Result, error) {
	rows, err := s.store.ListReplicas(app.ID)
	if err != nil {
		return nil, err
	}
	var row *db.Replica
	for _, candidate := range rows {
		if candidate.Index == surgeIndex {
			row = candidate
			break
		}
	}
	if s.activationSurgeCurrent(app, current, a, surgeIndex, row) {
		info, _ := s.manager.GetReplica(app.Slug, surgeIndex)
		return &deploy.Result{Index: surgeIndex, PID: info.PID, Port: info.Port, Provider: info.Provider,
			Tier: info.Tier, EndpointURL: info.EndpointURL, WorkerID: info.WorkerID}, nil
	}
	return s.deployReplica(p, surgeIndex)
}

// discardInvalidActivationSurge is safe only before a canonical route is
// detached. It removes an unattributed/stale process at the transient index so
// capacity admission and a fresh launch cannot be poisoned by an old surge.
func (s *Server) discardInvalidActivationSurge(app *db.App, surgeIndex int) error {
	if err := s.confirmActivationReplicaStopped(app, surgeIndex); err != nil {
		return err
	}
	target := s.proxy.ReplicaTargetURL(app.Slug, surgeIndex)
	if target != "" {
		s.proxy.DeregisterReplicaIfTarget(app.Slug, surgeIndex, target)
	}
	if err := s.store.DeleteReplica(app.ID, surgeIndex); err != nil && !errors.Is(err, db.ErrNotFound) {
		return err
	}
	return nil
}

func (s *Server) confirmActivationReplicaStopped(app *db.App, index int) error {
	rows, err := s.store.ListReplicas(app.ID)
	if err != nil {
		return fmt.Errorf("load activation replica identity: %w", err)
	}
	var durable *db.Replica
	for _, row := range rows {
		if row.Index == index {
			durable = row
			break
		}
	}
	stopErr := s.manager.StopReplicaConfirmed(app.Slug, index)
	if errors.Is(stopErr, process.ErrReplicaNotFound) && durable != nil {
		if lister, ok := s.manager.RuntimeForTier(durable.Tier).(interface {
			ListByLabel(string) ([]process.ContainerInfo, error)
			RemoveContainer(string) error
		}); ok {
			containers, listErr := lister.ListByLabel(process.ManagedContainerFilterJSON)
			if listErr != nil {
				return fmt.Errorf("verify activation container %d absence: %w", index, listErr)
			}
			if durable.WorkerID == "" {
				return fmt.Errorf("activation container %d has no durable container id: %w", index, process.ErrStopUnconfirmed)
			}
			for _, container := range containers {
				if container.ID != durable.WorkerID {
					continue
				}
				if err := lister.RemoveContainer(container.ID); err != nil {
					return fmt.Errorf("remove activation container %s: %w", container.ID, err)
				}
				break
			}
			stopErr = nil // successful inventory proves exact-container presence/absence
		}
	}
	if errors.Is(stopErr, process.ErrReplicaNotFound) && durable != nil && durable.PID != nil && *durable.PID > 0 {
		groupProbeErr := syscall.Kill(-*durable.PID, 0)
		pidProbeErr := syscall.Kill(*durable.PID, 0)
		if errors.Is(groupProbeErr, syscall.ESRCH) && errors.Is(pidProbeErr, syscall.ESRCH) {
			// A confirmed-absent PID is safe to tombstone even if a previous DB
			// checkpoint failed after the manager had already stopped it.
			stopErr = nil
		} else {
			// Recovery deliberately leaves a PID-backed row behind when it cannot
			// prove the PID belongs to this app. Manager absence is therefore not
			// proof of process absence. Preserve both the identity and route until an
			// operator or a later recovery pass can verify ownership safely.
			return fmt.Errorf("activation replica %d has durable pid %d but no verified runtime owner: %w", index, *durable.PID, process.ErrStopUnconfirmed)
		}
	}
	if stopErr != nil && !errors.Is(stopErr, process.ErrReplicaNotFound) {
		return stopErr
	}
	if durable != nil {
		if err := s.store.ClearReplicaRuntimeIdentity(app.ID, index); err != nil && !errors.Is(err, db.ErrNotFound) {
			return err
		}
	}
	return nil
}

// confirmAppConsumersStopped combines the in-memory manager view with durable
// replica identities. Guarded native launches persist a starting row before app
// code can exec, so an empty Manager after owner loss is never accepted as
// proof while a durable PID or container identity remains.
func (s *Server) confirmAppConsumersStopped(app *db.App) error {
	if s.manager == nil {
		return errors.New("process manager unavailable")
	}
	if err := s.manager.StopConfirmed(app.Slug); err != nil && !errors.Is(err, process.ErrReplicaNotFound) {
		return err
	}
	rows, err := s.store.ListReplicas(app.ID)
	if err != nil {
		return fmt.Errorf("list durable replica identities: %w", err)
	}
	for _, row := range rows {
		if err := s.confirmActivationReplicaStopped(app, row.Index); err != nil {
			return fmt.Errorf("confirm replica %d stopped: %w", row.Index, err)
		}
	}
	// Generation identities are a crash-recovery ledger, not capacity cache.
	// Clear them only after Manager.StopConfirmed has physically confirmed every
	// active, staged, and draining pool for the slug is gone.
	generationRows, err := s.store.ListDeploymentReplicas(app.ID)
	if err != nil {
		return fmt.Errorf("list durable generation identities: %w", err)
	}
	seen := make(map[int64]struct{})
	for _, row := range generationRows {
		seen[row.DeploymentID] = struct{}{}
	}
	for deploymentID := range seen {
		if err := s.store.DeleteDeploymentReplicas(deploymentID); err != nil {
			return fmt.Errorf("clear stopped generation %d identities: %w", deploymentID, err)
		}
	}
	return nil
}

func (s *Server) clearConfirmedActivationStart(appID int64, index int, startErr error) (bool, error) {
	var start *deploy.ReplicaStartError
	if !errors.As(startErr, &start) || !start.CleanupConfirmed {
		return false, nil
	}
	if err := s.store.ClearReplicaRuntimeIdentity(appID, index); err != nil && !errors.Is(err, db.ErrNotFound) {
		return true, err
	}
	return true, nil
}

func (s *Server) invalidateDormantActivationReplica(app *db.App, a *db.ScheduleActivation, index int) error {
	target := s.proxy.ReplicaTargetURL(app.Slug, index)
	if err := s.confirmActivationReplicaStopped(app, index); err != nil {
		return err
	}
	if target != "" {
		s.proxy.DeregisterReplicaIfTarget(app.Slug, index, target)
	}
	return s.store.InvalidateReplicaSnapshot(app.ID, index, a.TargetGeneration, a.ID)
}

func (s *Server) persistActivationReplica(app *db.App, current *db.Deployment, result *deploy.Result, a *db.ScheduleActivation) error {
	pid, port, deploymentID := result.PID, result.Port, current.ID
	if err := s.store.UpsertActivationReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: result.Index, PID: &pid, Port: &port, Status: db.ReplicaStatusRunning,
		Provider: result.Provider, Tier: result.Tier, EndpointURL: result.EndpointURL, WorkerID: result.WorkerID,
		AppVersion: current.Version, DesiredState: "running", DeploymentID: &deploymentID,
		StartupPeakRSSBytes: result.StartupPeakRSSBytes,
	}, a.TargetGeneration, a.ID); err != nil {
		return fmt.Errorf("persist replacement replica %d: %w", result.Index, err)
	}
	return nil
}

func (s *Server) persistStartingActivationReplica(app *db.App, current *db.Deployment, result *deploy.Result, a *db.ScheduleActivation) error {
	pid, port, deploymentID := result.PID, result.Port, current.ID
	return s.store.UpsertActivationReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: result.Index, PID: &pid, Port: &port, Status: "starting",
		Provider: result.Provider, Tier: result.Tier, EndpointURL: result.EndpointURL, WorkerID: result.WorkerID,
		AppVersion: current.Version, DesiredState: "running", DeploymentID: &deploymentID,
	}, a.TargetGeneration, a.ID)
}

func (s *Server) retireActivationSurge(app *db.App, surgeIndex int) error {
	target := s.proxy.ReplicaTargetURL(app.Slug, surgeIndex)
	if target != "" {
		s.proxy.DrainReplica(app.Slug, surgeIndex)
		grace := s.cfg.Server.DrainTimeout
		if grace <= 0 {
			grace = 60 * time.Second
		}
		s.waitForDrain(app.Slug, surgeIndex, grace, nil)
		detached := s.proxy.DetachDrainedReplica(app.Slug, surgeIndex, target, false)
		if !detached.Matched {
			return errors.New("detach surge replica: route changed during drain")
		}
		if !detached.Detached {
			detached = s.proxy.DetachDrainedReplica(app.Slug, surgeIndex, target, true)
			slog.Warn("schedule activation forced surge drain", "app", app.Slug, "index", surgeIndex, "active_connections", detached.ActiveConns)
		}
	}
	if err := s.confirmActivationReplicaStopped(app, surgeIndex); err != nil {
		return fmt.Errorf("stop surge replica: %w", err)
	}
	if err := s.store.DeleteReplica(app.ID, surgeIndex); err != nil && !errors.Is(err, db.ErrNotFound) {
		return fmt.Errorf("delete surge replica row: %w", err)
	}
	s.proxy.SetPoolSize(app.Slug, app.Replicas)
	return nil
}

func (s *Server) activationRetryError(a *db.ScheduleActivation, err error) error {
	if a.Attempts >= 3 {
		return err
	}
	return &activation.RetryableError{Reason: err.Error(), RetryAfter: 5 * time.Second}
}

func (s *Server) activationRepairError(err error) error {
	return &activation.RepairRequiredError{Reason: err.Error(), RetryAfter: 5 * time.Second}
}

func derefActivationRunID(id *int64) int64 {
	if id == nil {
		return 0
	}
	return *id
}
