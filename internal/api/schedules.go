package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/robfig/cron/v3"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
)

var scheduleNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

type scheduleDTO struct {
	ID             int64    `json:"id"`
	AppID          int64    `json:"app_id"`
	Name           string   `json:"name"`
	CronExpr       string   `json:"cron_expr"`
	Command        []string `json:"command"`
	Enabled        bool     `json:"enabled"`
	TimeoutSeconds int      `json:"timeout_seconds"`
	OverlapPolicy  string   `json:"overlap_policy"`
	MissedPolicy   string   `json:"missed_policy"`
	NextFire       *string  `json:"next_fire,omitempty"`
}

func toScheduleDTO(sc *db.Schedule, next *time.Time) scheduleDTO {
	var cmd []string
	_ = json.Unmarshal([]byte(sc.CommandJSON), &cmd)
	out := scheduleDTO{
		ID: sc.ID, AppID: sc.AppID, Name: sc.Name, CronExpr: sc.CronExpr,
		Command: cmd, Enabled: sc.Enabled, TimeoutSeconds: sc.TimeoutSeconds,
		OverlapPolicy: sc.OverlapPolicy, MissedPolicy: sc.MissedPolicy,
	}
	if next != nil {
		s := next.Format(time.RFC3339)
		out.NextFire = &s
	}
	return out
}

// GET /api/apps/{slug}/schedules
func (s *Server) handleListSchedules(w http.ResponseWriter, r *http.Request) {
	app, _, ok := s.requireViewApp(w, r, chi.URLParam(r, "slug"))
	if !ok {
		return
	}
	rows, err := s.store.ListSchedulesByApp(app.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]scheduleDTO, 0, len(rows))
	for _, sc := range rows {
		var next *time.Time
		if s.scheduler != nil {
			if t, err := s.scheduler.NextFire(sc.ID); err == nil {
				next = &t
			}
		}
		out = append(out, toScheduleDTO(sc, next))
	}
	writeJSON(w, http.StatusOK, out)
}

// POST /api/apps/{slug}/schedules
func (s *Server) handleCreateSchedule(w http.ResponseWriter, r *http.Request) {
	app, ok := s.requireManageApp(w, r, chi.URLParam(r, "slug"))
	if !ok {
		return
	}
	var req struct {
		Name           string   `json:"name"`
		CronExpr       string   `json:"cron_expr"`
		Command        []string `json:"command"`
		Enabled        *bool    `json:"enabled"`
		TimeoutSeconds int      `json:"timeout_seconds"`
		OverlapPolicy  string   `json:"overlap_policy"`
		MissedPolicy   string   `json:"missed_policy"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if err := validateSchedule(req.Name, req.CronExpr, req.Command, req.TimeoutSeconds, req.OverlapPolicy, req.MissedPolicy); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	cmdJSON, _ := json.Marshal(req.Command)
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	id, err := s.store.CreateSchedule(db.CreateScheduleParams{
		AppID: app.ID, Name: req.Name, CronExpr: req.CronExpr,
		CommandJSON: string(cmdJSON), Enabled: enabled,
		TimeoutSeconds: req.TimeoutSeconds, OverlapPolicy: req.OverlapPolicy,
		MissedPolicy: req.MissedPolicy,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if s.scheduler != nil {
		if err := s.scheduler.Reload(id); err != nil {
			writeError(w, http.StatusInternalServerError, "scheduler reload: "+err.Error())
			return
		}
	}
	s.audit(r, "schedule_create", "schedule", fmt.Sprintf("%d", id), fmt.Sprintf(`{"app":%q,"name":%q}`, app.Slug, req.Name))
	sc, _ := s.store.GetSchedule(id)
	writeJSON(w, http.StatusCreated, toScheduleDTO(sc, nil))
}

// PATCH /api/apps/{slug}/schedules/{id}
func (s *Server) handlePatchSchedule(w http.ResponseWriter, r *http.Request) {
	app, ok := s.requireManageApp(w, r, chi.URLParam(r, "slug"))
	if !ok {
		return
	}
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
	var req struct {
		Name           *string   `json:"name,omitempty"`
		CronExpr       *string   `json:"cron_expr,omitempty"`
		Command        *[]string `json:"command,omitempty"`
		Enabled        *bool     `json:"enabled,omitempty"`
		TimeoutSeconds *int      `json:"timeout_seconds,omitempty"`
		OverlapPolicy  *string   `json:"overlap_policy,omitempty"`
		MissedPolicy   *string   `json:"missed_policy,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	// Validate any provided fields by composing into the existing record.
	name := sc.Name
	if req.Name != nil {
		name = *req.Name
	}
	cronExpr := sc.CronExpr
	if req.CronExpr != nil {
		cronExpr = *req.CronExpr
	}
	var cmd []string
	_ = json.Unmarshal([]byte(sc.CommandJSON), &cmd)
	if req.Command != nil {
		cmd = *req.Command
	}
	timeout := sc.TimeoutSeconds
	if req.TimeoutSeconds != nil {
		timeout = *req.TimeoutSeconds
	}
	overlap := sc.OverlapPolicy
	if req.OverlapPolicy != nil {
		overlap = *req.OverlapPolicy
	}
	missed := sc.MissedPolicy
	if req.MissedPolicy != nil {
		missed = *req.MissedPolicy
	}
	if err := validateSchedule(name, cronExpr, cmd, timeout, overlap, missed); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var cmdJSONPtr *string
	if req.Command != nil {
		b, _ := json.Marshal(*req.Command)
		v := string(b)
		cmdJSONPtr = &v
	}
	if err := s.store.UpdateSchedule(id, db.UpdateScheduleParams{
		Name: req.Name, CronExpr: req.CronExpr, CommandJSON: cmdJSONPtr,
		Enabled: req.Enabled, TimeoutSeconds: req.TimeoutSeconds,
		OverlapPolicy: req.OverlapPolicy, MissedPolicy: req.MissedPolicy,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if s.scheduler != nil {
		if err := s.scheduler.Reload(id); err != nil {
			writeError(w, http.StatusInternalServerError, "scheduler reload: "+err.Error())
			return
		}
	}
	s.audit(r, "schedule_update", "schedule", fmt.Sprintf("%d", id), "")
	fresh, _ := s.store.GetSchedule(id)
	writeJSON(w, http.StatusOK, toScheduleDTO(fresh, nil))
}

// DELETE /api/apps/{slug}/schedules/{id}
func (s *Server) handleDeleteSchedule(w http.ResponseWriter, r *http.Request) {
	app, ok := s.requireManageApp(w, r, chi.URLParam(r, "slug"))
	if !ok {
		return
	}
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
	if err := s.store.DeleteSchedule(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if s.scheduler != nil {
		_ = s.scheduler.Remove(id)
	}
	s.audit(r, "schedule_delete", "schedule", fmt.Sprintf("%d", id), "")
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/apps/{slug}/schedules/{id}/run
func (s *Server) handleRunSchedule(w http.ResponseWriter, r *http.Request) {
	app, ok := s.requireManageApp(w, r, chi.URLParam(r, "slug"))
	if !ok {
		return
	}
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
	user := auth.UserFromContext(r.Context())
	var uid *int64
	if user != nil {
		u := user.ID
		uid = &u
	}
	// Run asynchronously so the response doesn't block on the run finishing.
	if s.jobs != nil {
		go func() {
			if _, err := s.jobs.Run(id, "manual", uid); err != nil {
				// Run already logs the audit event; nothing to do here.
			}
		}()
	}
	s.audit(r, "schedule_run_manual", "schedule", fmt.Sprintf("%d", id), "")
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "started"})
}

// GET /api/apps/{slug}/schedules/{id}/runs
func (s *Server) handleListScheduleRuns(w http.ResponseWriter, r *http.Request) {
	app, _, ok := s.requireViewApp(w, r, chi.URLParam(r, "slug"))
	if !ok {
		return
	}
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
	limit, offset := paginationParams(r, 50, 200)
	runs, err := s.store.ListScheduleRuns(id, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, runs)
}

// POST /api/apps/{slug}/schedules/{id}/runs/{run_id}/cancel
func (s *Server) handleCancelScheduleRun(w http.ResponseWriter, r *http.Request) {
	app, ok := s.requireManageApp(w, r, chi.URLParam(r, "slug"))
	if !ok {
		return
	}
	scheduleID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad schedule id")
		return
	}
	runID, err := strconv.ParseInt(chi.URLParam(r, "run_id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad run id")
		return
	}
	// Verify the run belongs to a schedule owned by the caller's app.
	run, err := s.store.GetScheduleRun(runID)
	if errors.Is(err, db.ErrNotFound) || err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if run.ScheduleID != scheduleID {
		// The {id} in the URL and the run's schedule must agree.
		writeError(w, http.StatusBadRequest, "run does not belong to the given schedule")
		return
	}
	sched, err := s.store.GetSchedule(run.ScheduleID)
	if errors.Is(err, db.ErrNotFound) || err != nil || sched.AppID != app.ID {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if s.jobs != nil {
		if err := s.jobs.Cancel(runID); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// validateSchedule applies all field-level validation.
func validateSchedule(name, cronExpr string, cmd []string, timeout int, overlap, missed string) error {
	if !scheduleNameRe.MatchString(name) {
		return errors.New("name: must match [A-Za-z0-9_-]{1,64}")
	}
	if _, err := cron.ParseStandard(cronExpr); err != nil {
		return fmt.Errorf("cron_expr: %w", err)
	}
	if len(cmd) == 0 || strings.TrimSpace(cmd[0]) == "" {
		return errors.New("command: must not be empty")
	}
	if timeout < 1 || timeout > 86400 {
		return errors.New("timeout_seconds: must be 1..86400")
	}
	switch overlap {
	case "skip", "queue", "concurrent":
	default:
		return errors.New("overlap_policy: must be skip|queue|concurrent")
	}
	switch missed {
	case "skip", "run_once":
	default:
		return errors.New("missed_policy: must be skip|run_once")
	}
	return nil
}

// paginationParams reads limit + offset query params, applying defaults + caps.
func paginationParams(r *http.Request, defLimit, max int) (limit, offset int) {
	limit = defLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > max {
		limit = max
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}
	return
}

// --- Shared data ---

type sharedDataDTO struct {
	SourceSlug string `json:"source_slug"`
	SourceID   int64  `json:"source_id"`
}

// GET /api/apps/{slug}/shared-data
func (s *Server) handleListSharedData(w http.ResponseWriter, r *http.Request) {
	app, _, ok := s.requireViewApp(w, r, chi.URLParam(r, "slug"))
	if !ok {
		return
	}
	rows, err := s.store.ListSharedDataSources(app.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]sharedDataDTO, 0, len(rows))
	for _, m := range rows {
		out = append(out, sharedDataDTO{SourceSlug: m.SourceSlug, SourceID: m.SourceAppID})
	}
	writeJSON(w, http.StatusOK, out)
}

// POST /api/apps/{slug}/shared-data
func (s *Server) handleGrantSharedData(w http.ResponseWriter, r *http.Request) {
	app, ok := s.requireManageApp(w, r, chi.URLParam(r, "slug"))
	if !ok {
		return
	}
	var req struct {
		SourceSlug string `json:"source_slug"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.SourceSlug == "" {
		writeError(w, http.StatusBadRequest, "source_slug required")
		return
	}
	src, err := s.store.GetApp(req.SourceSlug)
	if err != nil {
		writeError(w, http.StatusNotFound, "source app not found")
		return
	}
	user := auth.UserFromContext(r.Context())
	// The caller must have viewer+ on the source app.
	ok, err = s.canViewApp(user, src)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	if err := s.store.GrantSharedData(app.ID, src.ID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "shared_data_grant", "app", app.Slug, fmt.Sprintf(`{"source":%q}`, src.Slug))
	writeJSON(w, http.StatusCreated, sharedDataDTO{SourceSlug: src.Slug, SourceID: src.ID})
}

// DELETE /api/apps/{slug}/shared-data/{source_slug}
func (s *Server) handleRevokeSharedData(w http.ResponseWriter, r *http.Request) {
	app, ok := s.requireManageApp(w, r, chi.URLParam(r, "slug"))
	if !ok {
		return
	}
	sourceSlug := chi.URLParam(r, "source_slug")
	src, err := s.store.GetApp(sourceSlug)
	if err != nil {
		writeError(w, http.StatusNotFound, "source app not found")
		return
	}
	if err := s.store.RevokeSharedData(app.ID, src.ID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "shared_data_revoke", "app", app.Slug, fmt.Sprintf(`{"source":%q}`, src.Slug))
	w.WriteHeader(http.StatusNoContent)
}
