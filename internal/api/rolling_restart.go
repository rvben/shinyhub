package api

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/rvben/shinyhub/internal/db"
)

// handleEnqueueRollingRestart admits durable work without waiting for the app
// lifecycle lock. Roll re-reads the app and serving bundle under that lock and
// defers while a previous generation drains. Only unstarted requests can be
// coalesced: an executing roll may already have read an older environment.
func (s *Server) handleEnqueueRollingRestart(w http.ResponseWriter, r *http.Request, app *db.App) {
	if err := s.guardCompatibilityQuarantine(app.ID, "restart "+app.Slug); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if err := s.validateScheduleActivationForApp(app, "roll"); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if app.Status != "running" && app.Status != "degraded" {
		writeError(w, http.StatusConflict, "rolling restart requires a running app; use apps start for a stopped or hibernated app")
		return
	}
	current, err := s.store.GetServingDeployment(app.ID)
	if errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusConflict, "app has no successful deployment")
		return
	}
	if err != nil {
		writeInternal(w, r, "internal server error", err)
		return
	}
	if !current.Prepared {
		writeError(w, http.StatusConflict, "rolling restart requires a prepared deployment; deploy the app first")
		return
	}
	if err := s.checkColocatedShared(app.ID, s.tiersForApp(app)); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	a, err := s.store.EnqueueRollingRestart(app, current)
	if errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if errors.Is(err, db.ErrRollingRestartTargetChanged) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		writeInternal(w, r, "enqueue rolling restart failed", err)
		return
	}
	s.audit(r, "rolling_restart", "app", app.Slug, fmt.Sprintf(`{"activation_id":%d}`, a.ID))
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "accepted", "slug": app.Slug, "activation_id": a.ID, "drain_timeout_seconds": s.generationDrainTimeoutSeconds()})
}
