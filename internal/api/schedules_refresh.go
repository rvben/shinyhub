package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rvben/shinyhub/internal/auth"
)

// acquireRefreshOperation preserves both the fleet and cross-process app fences
// while allowing cancellation before admission, without detached lock waiters.
func (s *Server) acquireRefreshOperation(ctx context.Context, slug string) (func(), error) {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		release, err := s.acquireAppOperation(slug, true)
		if err != nil || release != nil {
			return release, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *Server) handleRefreshStaleSchedule(w http.ResponseWriter, r *http.Request) {
	app, ok := s.requireManageApp(w, r, chi.URLParam(r, "slug"))
	if !ok {
		return
	}
	release, err := s.acquireRefreshOperation(r.Context(), app.Slug)
	if err != nil {
		writeError(w, http.StatusRequestTimeout, "schedule refresh admission failed: "+err.Error())
		return
	}
	defer release()
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad schedule id")
		return
	}
	sc, err := s.store.GetSchedule(id)
	if err != nil || sc.AppID != app.ID {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if s.jobs == nil {
		writeError(w, http.StatusServiceUnavailable, "scheduler unavailable")
		return
	}
	var uid *int64
	if user := auth.UserFromContext(r.Context()); user != nil {
		id := user.ID
		uid = &id
	}
	result, err := s.jobs.RefreshStale(r.Context(), id, uid, s.cfg.Scheduler.Location)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			status = http.StatusRequestTimeout
		}
		writeError(w, status, "schedule refresh admission failed: "+err.Error())
		return
	}
	s.audit(r, "schedule_refresh_stale", "schedule", strconv.FormatInt(id, 10), fmt.Sprintf(`{"status":%q,"run_id":%d}`, result.Status, result.RunID))
	status := http.StatusOK
	if result.Status == "started" || result.Status == "joined" {
		status = http.StatusAccepted
	}
	writeJSON(w, status, result)
}
