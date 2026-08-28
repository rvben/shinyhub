package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/rvben/shinyhub/internal/db"
)

func (s *Server) handleReconcileScheduleConvergence(w http.ResponseWriter, r *http.Request) {
	app, ok := s.requireManageApp(w, r, chi.URLParam(r, "slug"))
	if !ok {
		return
	}
	release := s.acquireDeployLock(app.Slug)
	defer release()
	deployments, err := s.store.ListDeployments(app.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "resolve current deployment")
		return
	}
	if len(deployments) == 0 {
		writeJSON(w, http.StatusOK, []ScheduleConvergenceResult{})
		return
	}
	results, err := s.reconcileAndDispatchScheduleConvergence(app.ID, deployments[0].ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, results)
}

// handleRetryScheduleConvergence is the deliberate retry boundary for a
// producer command that actually ran and failed. Infrastructure admission and
// interrupted-server failures repair themselves without operator action.
func (s *Server) handleRetryScheduleConvergence(w http.ResponseWriter, r *http.Request) {
	app, ok := s.requireManageApp(w, r, chi.URLParam(r, "slug"))
	if !ok {
		return
	}
	release := s.acquireDeployLock(app.Slug)
	defer release()
	scheduleID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad schedule id")
		return
	}
	schedule, err := s.store.GetSchedule(scheduleID)
	if err != nil || schedule.AppID != app.ID {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if !schedule.Enabled {
		writeError(w, http.StatusConflict, "schedule is disabled; enable it before retrying convergence, or run it manually to perform an explicit repair")
		return
	}
	rows, err := s.store.ScheduleFreshnessByApp(app.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "resolve schedule convergence")
		return
	}
	var obligationID *int64
	for _, row := range rows {
		if row.ScheduleID == scheduleID && row.ConvergenceStatus == "failed" {
			obligationID = row.ConvergenceObligationID
			break
		}
	}
	if obligationID == nil {
		writeError(w, http.StatusConflict, "current schedule convergence is not failed")
		return
	}
	// Read the immutable routing identity before accepting the mutation. Once
	// RetryDeployObligation commits, every response must acknowledge acceptance;
	// a later read/dispatch failure is advisory, never an ambiguous 5xx.
	current, err := s.store.GetDeployObligation(*obligationID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "read schedule convergence")
		return
	}
	if err := s.store.RetryDeployObligation(*obligationID); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusConflict, "current schedule convergence is no longer failed")
			return
		}
		writeError(w, http.StatusInternalServerError, "retry schedule convergence")
		return
	}
	current.Status = "pending"
	current.ScheduleRunID = nil
	var warning string
	if err := s.dispatchPendingScheduleConvergenceFor(app.ID, current.DeploymentID); err != nil {
		warning = "retry was durably accepted, but immediate convergence dispatch failed: " + err.Error()
	} else if refreshed, refreshErr := s.store.GetDeployObligation(*obligationID); refreshErr != nil {
		warning = "retry was durably accepted, but its current convergence state could not be read: " + refreshErr.Error()
	} else {
		current = refreshed
	}
	s.audit(r, "schedule_convergence_retry", "schedule", fmt.Sprintf("%d", scheduleID),
		fmt.Sprintf(`{"app":%q,"name":%q,"obligation_id":%d}`, app.Slug, schedule.Name, current.ID))
	writeJSON(w, http.StatusAccepted, ScheduleConvergenceResult{
		ScheduleID: scheduleID, Schedule: schedule.Name, ObligationID: current.ID,
		Status: current.Status, RunID: current.ScheduleRunID, Warning: warning,
	})
}

// ScheduleConvergenceResult is the server-authoritative desired-state outcome
// for every persisted deploy-triggered schedule, independent of whether the
// uploaded manifest happened to mention it.
type ScheduleConvergenceResult struct {
	ScheduleID   int64  `json:"schedule_id"`
	Schedule     string `json:"schedule"`
	ObligationID int64  `json:"obligation_id"`
	Status       string `json:"status"`
	RunID        *int64 `json:"run_id,omitempty"`
	// Prestart proves convergence was satisfied before any consumer replica for
	// the deployment was started. The authoritative run may have completed in an
	// earlier request. Clients must not perform a redundant warm restart.
	Prestart bool   `json:"prestart,omitempty"`
	Warning  string `json:"warning,omitempty"`
}

// reconcileAndDispatchScheduleConvergence materializes all persisted policy
// obligations for a promoted deployment, then drains the durable outbox.
// Dispatch admission is atomic with the run row; any error releases the claim
// so startup/periodic reconciliation can safely try again.
func (s *Server) reconcileAndDispatchScheduleConvergence(appID, deploymentID int64) ([]ScheduleConvergenceResult, error) {
	obligations, err := s.store.ReconcileDeployObligationsForDeployment(appID, deploymentID)
	if err != nil {
		return nil, err
	}
	if err := s.dispatchPendingScheduleConvergenceFor(appID, deploymentID); err != nil {
		return nil, err
	}

	schedules, err := s.store.ListSchedulesByApp(appID)
	if err != nil {
		return nil, err
	}
	names := make(map[int64]string, len(schedules))
	for _, schedule := range schedules {
		names[schedule.ID] = schedule.Name
	}
	results := make([]ScheduleConvergenceResult, 0, len(obligations))
	for _, original := range obligations {
		current, err := s.store.GetDeployObligation(original.ID)
		if err != nil {
			return nil, err
		}
		results = append(results, ScheduleConvergenceResult{
			ScheduleID:   current.ScheduleID,
			Schedule:     names[current.ScheduleID],
			ObligationID: current.ID,
			Status:       current.Status,
			RunID:        current.ScheduleRunID,
		})
	}
	return results, nil
}

func (s *Server) dispatchPendingScheduleConvergence() error {
	return s.dispatchScheduleConvergence(0, 0, false)
}

func (s *Server) dispatchPendingScheduleConvergenceFor(appID, deploymentID int64) error {
	return s.dispatchScheduleConvergence(appID, deploymentID, true)
}

func (s *Server) dispatchScheduleConvergence(appID, deploymentID int64, scoped bool) error {
	var dispatchErrs []error
	for attempts := 0; attempts < 256; attempts++ {
		var obligation *db.ScheduleDeployObligation
		var err error
		if scoped {
			obligation, err = s.store.ClaimNextDeployObligationFor(appID, deploymentID)
		} else {
			obligation, err = s.store.ClaimNextDeployObligation()
		}
		if errors.Is(err, db.ErrNotFound) {
			return errors.Join(dispatchErrs...)
		}
		if err != nil {
			return fmt.Errorf("claim schedule convergence: %w", err)
		}
		if s.jobs == nil {
			err := errors.New("jobs manager unavailable")
			_ = s.store.ReleaseDeployObligation(obligation.ID, err)
			dispatchErrs = append(dispatchErrs, err)
			continue
		}
		if _, err := s.jobs.RunDeployObligation(obligation); err != nil {
			if releaseErr := s.store.ReleaseDeployObligation(obligation.ID, err); releaseErr != nil {
				dispatchErrs = append(dispatchErrs, fmt.Errorf("dispatch schedule convergence: %v; release claim: %w", err, releaseErr))
				continue
			}
			dispatchErrs = append(dispatchErrs, fmt.Errorf("dispatch schedule convergence: %w", err))
			continue
		}
	}
	return errors.Join(append(dispatchErrs, errors.New("schedule convergence drain limit reached"))...)
}

func (s *Server) reconcileCurrentSchedule(appID, scheduleID int64) (*ScheduleConvergenceResult, error) {
	deployments, err := s.store.ListDeployments(appID)
	if err != nil {
		return nil, err
	}
	if len(deployments) == 0 {
		return nil, nil
	}
	results, err := s.reconcileAndDispatchScheduleConvergence(appID, deployments[0].ID)
	if err != nil {
		return nil, err
	}
	for _, result := range results {
		if result.ScheduleID == scheduleID {
			result := result
			return &result, nil
		}
	}
	return nil, nil
}
