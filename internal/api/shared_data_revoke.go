package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"

	"github.com/go-chi/chi/v5"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/jobs"
	"github.com/rvben/shinyhub/internal/process"
)

func (s *Server) pauseSharedDataRuns(appID int64) (func(), error) {
	release := func() {}
	if s.jobs != nil {
		var err error
		release, err = s.jobs.TryPauseAppRuns(appID)
		if err != nil {
			return nil, err
		}
	}
	active, err := s.store.HasActiveAppScheduleRuns(appID)
	if err != nil {
		release()
		return nil, err
	}
	if active {
		release()
		return nil, jobs.ErrAppRunsActive
	}
	return release, nil
}

func (s *Server) sharedDataRunError(w http.ResponseWriter, err error) {
	if errors.Is(err, jobs.ErrAppRunsActive) || errors.Is(err, jobs.ErrManagerStopped) {
		writeError(w, http.StatusConflict, err.Error())
	} else {
		writeError(w, http.StatusInternalServerError, "could not verify scheduled jobs are idle")
	}
}

// DELETE /api/apps/{slug}/shared-data/{source_slug}?restart=true
// Success means old consumers are physically gone, not just that a grant row
// was deleted. A running app requires explicit restart consent. A dormant app
// stays dormant. Retries reconcile stale runtime links even if the grant is gone.
func (s *Server) handleRevokeSharedData(w http.ResponseWriter, r *http.Request) {
	app, ok := s.requireManageApp(w, r, chi.URLParam(r, "slug"))
	if !ok {
		return
	}
	sourceSlug := chi.URLParam(r, "source_slug")
	u := auth.UserFromContext(r.Context())
	if u == nil || !u.AppInScope(sourceSlug) {
		writeError(w, http.StatusNotFound, "source app not found")
		return
	}
	src, err := s.store.GetApp(sourceSlug)
	if err != nil {
		writeError(w, http.StatusNotFound, "source app not found")
		return
	}
	release, err := s.acquireAppOperation(app.Slug, false)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	defer release()
	app, ok = s.rereadAuthorizedApp(w, app.Slug, app.ID)
	if !ok {
		return
	}
	if err := s.guardActivationLifecycle(app.ID, "unmount shared data "+app.Slug); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	releaseRuns, err := s.pauseSharedDataRuns(app.ID)
	if err != nil {
		s.sharedDataRunError(w, err)
		return
	}
	defer releaseRuns()

	restart := app.Status == "running" || app.Status == "degraded"
	live := restart || app.Status == "starting" || (s.manager != nil && s.manager.HoldsLiveProcess(app.Slug))
	if live && r.URL.Query().Get("restart") != "true" {
		writeError(w, http.StatusConflict, "unmounting requires restarting this app; retry with restart=true or stop the app first. Active sessions will disconnect")
		return
	}
	// StopConfirmed covers active, draining, staged, suspended, and durable pool
	// identities. No grant or link changes if a runtime cannot confirm shutdown.
	if s.manager != nil {
		if err := s.verifySharedDataConsumerOwnership(app.ID, app.Slug); err != nil {
			writeError(w, http.StatusConflict, "unmount not applied: "+err.Error())
			return
		}
		if err := s.confirmAppConsumersStopped(app); err != nil {
			writeError(w, http.StatusConflict, "unmount not applied: app processes could not be confirmed stopped: "+err.Error())
			return
		}
	} else {
		replicas, err := s.store.ListReplicas(app.ID)
		generations, genErr := s.store.ListDeploymentReplicas(app.ID)
		if err != nil || genErr != nil || live || len(replicas) != 0 || len(generations) != 0 {
			writeError(w, http.StatusServiceUnavailable, "process manager unavailable; unmount was not applied")
			return
		}
	}
	// Native children inherit the lifetime descriptor. Retain this exclusive
	// fence through link/config removal so untracked survivors cannot be missed.
	releaseConsumers := func() {}
	if s.jobs != nil {
		ctx, cancel := context.WithCancel(r.Context())
		cancel()
		releaseConsumers, err = s.jobs.AcquireExclusiveConsumerLifetime(ctx, app.ID)
		if err != nil {
			writeError(w, http.StatusConflict, "unmount not applied: a previous app process still holds its data access; retry after it stops")
			return
		}
	}
	defer func() { releaseConsumers() }()
	if s.proxy != nil {
		s.proxy.Deregister(app.Slug)
	}
	// Keep stopped/hibernated intent intact, and leave a failed restart safely
	// stopped. Confirmed old rows cannot be resumed from a stale snapshot.
	status := app.Status
	if restart || app.Status == "starting" {
		status = "stopped"
	}
	if err := s.store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: app.Slug, Status: status}); err != nil {
		writeError(w, http.StatusInternalServerError, "app stopped, but its status could not be saved; unmount not applied")
		return
	}
	rows, err := s.store.ListReplicas(app.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "app stopped, but replica state could not be loaded; unmount not applied")
		return
	}
	for _, row := range rows {
		if err := s.store.SetReplicaDesiredState(app.ID, row.Index, "stopped"); err != nil {
			writeError(w, http.StatusInternalServerError, "app stopped, but replica state could not be saved; unmount not applied")
			return
		}
	}
	if s.cfg.Storage.AppDataDir != "" {
		if err := process.RemoveSharedMountLink(filepath.Join(s.cfg.Storage.AppDataDir, app.Slug), sourceSlug); err != nil {
			writeError(w, http.StatusInternalServerError, "app stopped, but the shared mount link could not be removed; unmount not applied")
			return
		}
	}
	if err := s.store.RevokeSharedData(app.ID, src.ID); err != nil && !errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, "app stopped, but the mount configuration could not be removed; retry unmount")
		return
	}
	s.audit(r, "shared_data_revoke", "app", app.Slug, fmt.Sprintf(`{"source":%q}`, src.Slug))
	// New executions now resolve only the remaining grants. Release these fences
	// before the normal boot path reacquires consumer/producer lifetime locks.
	releaseConsumers()
	releaseConsumers = func() {}
	releaseRuns() // release funcs are idempotent
	if restart {
		restarted, restartErr := s.restartForChangeLocked(r, app, app.Slug, true)
		result := map[string]any{"unmounted": true, "restarted": restarted, "app_status": "running"}
		if restartErr != nil || !restarted {
			result["app_status"] = "stopped"
			message := "app could not be restarted; start it when ready"
			if restartErr != nil {
				message = restartErr.Error()
			}
			result["restart_error"] = message
		}
		writeJSON(w, http.StatusOK, result)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// A surviving worker must never be mistaken for a dead local PID after owner
// loss. Require an exact managed incarnation for every durable identity before
// trusting the pool-wide confirmed stop, including old draining generations.
func (s *Server) verifySharedDataConsumerOwnership(appID int64, slug string) error {
	managed := s.manager.All()
	owned := func(index int, pid *int, workerID string, deploymentID int64) bool {
		if (pid == nil || *pid <= 0) && workerID == "" {
			return true
		}
		for _, info := range managed {
			if info.Slug != slug || info.Index != index {
				continue
			}
			if workerID != "" && info.WorkerID != workerID {
				continue
			}
			if pid != nil && *pid > 0 && info.PID != *pid {
				continue
			}
			if deploymentID != 0 && info.DeploymentID != deploymentID {
				continue
			}
			return true
		}
		return false
	}
	rows, err := s.store.ListReplicas(appID)
	if err != nil {
		return err
	}
	for _, row := range rows {
		var deploymentID int64
		if row.DeploymentID != nil {
			deploymentID = *row.DeploymentID
		}
		if !owned(row.Index, row.PID, row.WorkerID, deploymentID) {
			return fmt.Errorf("replica %d has no verified runtime owner; recover its worker before retrying unmount", row.Index)
		}
	}
	generations, err := s.store.ListDeploymentReplicas(appID)
	if err != nil {
		return err
	}
	for _, row := range generations {
		if !owned(row.Index, row.PID, row.WorkerID, row.DeploymentID) {
			return fmt.Errorf("generation %d replica %d has no verified runtime owner; recover its worker before retrying unmount", row.DeploymentID, row.Index)
		}
	}
	return nil
}
