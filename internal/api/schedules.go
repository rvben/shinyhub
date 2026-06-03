package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/lifecycle/scheduler"
	"github.com/rvben/shinyhub/internal/schedulespec"
)

type scheduleDTO struct {
	ID               int64    `json:"id"`
	AppID            int64    `json:"app_id"`
	Name             string   `json:"name"`
	CronExpr         string   `json:"cron_expr"`
	Command          []string `json:"command"`
	Enabled          bool     `json:"enabled"`
	TimeoutSeconds   int      `json:"timeout_seconds"`
	OverlapPolicy    string   `json:"overlap_policy"`
	MissedPolicy     string   `json:"missed_policy"`
	// Timezone is the raw stored value; null/nil means "inherit server default".
	Timezone         *string  `json:"timezone"`
	// EffectiveTimezone is the resolved IANA zone name that will actually be
	// used when firing this schedule (after server-default and UTC fallback).
	EffectiveTimezone string   `json:"effective_timezone"`
	// TimezoneInherited is true when no per-schedule timezone is stored and
	// the effective zone comes from the server default.
	TimezoneInherited bool     `json:"timezone_inherited"`
	NextFire         *string  `json:"next_fire,omitempty"`
	// DSTAdvisory warns when a fixed-hour schedule in a DST-observing zone will
	// fire twice on the fall-back day. Omitted when the schedule is safe.
	DSTAdvisory *string `json:"dst_advisory,omitempty"`
}

func toScheduleDTO(sc *db.Schedule, next *time.Time, serverDefaultLoc *time.Location) scheduleDTO {
	var cmd []string
	_ = json.Unmarshal([]byte(sc.CommandJSON), &cmd)
	if serverDefaultLoc == nil {
		serverDefaultLoc = time.UTC
	}
	loc := sc.EffectiveLocation(serverDefaultLoc)
	inherited := sc.Timezone == nil || *sc.Timezone == ""
	out := scheduleDTO{
		ID: sc.ID, AppID: sc.AppID, Name: sc.Name, CronExpr: sc.CronExpr,
		Command: cmd, Enabled: sc.Enabled, TimeoutSeconds: sc.TimeoutSeconds,
		OverlapPolicy: sc.OverlapPolicy, MissedPolicy: sc.MissedPolicy,
		Timezone:          sc.Timezone,
		EffectiveTimezone: loc.String(),
		TimezoneInherited: inherited,
	}
	if next != nil {
		s := next.Format(time.RFC3339)
		out.NextFire = &s
	}
	if advisory := scheduler.DSTAdvisory(sc.CronExpr, loc, time.Now()); advisory != "" {
		out.DSTAdvisory = &advisory
	}
	return out
}

// GET /api/apps/{slug}/schedules
func (s *Server) handleListSchedules(w http.ResponseWriter, r *http.Request) {
	app, _, ok := s.requireExplicitAppAccess(w, r, chi.URLParam(r, "slug"))
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
		out = append(out, toScheduleDTO(sc, next, s.cfg.Scheduler.Location))
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
		Timezone       *string  `json:"timezone"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	tzStr := ""
	if req.Timezone != nil {
		tzStr = *req.Timezone
	}
	if err := schedulespec.Validate(req.Name, req.CronExpr, tzStr, req.Command, req.TimeoutSeconds, req.OverlapPolicy, req.MissedPolicy); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Normalise empty string to nil (inherit server default).
	var storedTZ *string
	if req.Timezone != nil && *req.Timezone != "" {
		storedTZ = req.Timezone
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
		MissedPolicy: req.MissedPolicy, Timezone: storedTZ,
	})
	if err != nil {
		if errors.Is(err, db.ErrScheduleNameExists) {
			writeError(w, http.StatusConflict, db.ErrScheduleNameExists.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if s.scheduler != nil {
		if err := s.scheduler.Reload(id); err != nil {
			writeError(w, http.StatusInternalServerError, "scheduler reload: "+err.Error())
			return
		}
	}
	s.audit(r, "schedule_create", "schedule", fmt.Sprintf("%d", id), fmt.Sprintf(`{"app":%q,"name":%q,"effective_timezone":%q}`, app.Slug, req.Name, effectiveTZLabel(storedTZ, s.cfg.Scheduler.Location)))
	sc, _ := s.store.GetSchedule(id)
	writeJSON(w, http.StatusCreated, toScheduleDTO(sc, nil, s.cfg.Scheduler.Location))
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

	// Decode the body twice: once into a raw map to detect key presence, and
	// once into a typed struct for all other fields. This lets us implement the
	// tri-state timezone contract:
	//   key absent          -> unchanged (do not touch stored value)
	//   key present, null   -> clear to inherit (SetTimezone=true, Timezone=nil)
	//   key present, ""     -> clear to inherit (SetTimezone=true, Timezone=nil)
	//   key present, "<tz>" -> validate and set (SetTimezone=true, Timezone=&v)
	// All other PATCH fields use plain *T + omitempty because they have no
	// "clear to default" semantic — absent and null both mean "unchanged".
	bodyBytes, readErr := io.ReadAll(r.Body)
	if readErr != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	var rawFields map[string]json.RawMessage
	if err := json.Unmarshal(bodyBytes, &rawFields); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
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
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}

	// Resolve timezone tri-state from the raw map.
	// tzPresent is true when the key appeared in the JSON body at all.
	// tzValue holds the new stored value (nil = clear to inherit).
	var tzPresent bool
	var tzValue *string // nil = clear to inherit, non-nil = the new IANA zone
	if raw, present := rawFields["timezone"]; present {
		tzPresent = true
		// Unmarshal as *string so JSON null decodes to nil and a string to &v.
		var v *string
		if err := json.Unmarshal(raw, &v); err != nil {
			writeError(w, http.StatusBadRequest, "timezone must be a string or null")
			return
		}
		if v != nil && *v != "" {
			tzValue = v // non-empty string: validate below
		}
		// null or empty string -> tzValue stays nil (clear to inherit)
	}

	// Validate all provided fields by composing against the existing record.
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
	// Compose timezone for validation: use the incoming value when the key was
	// present (empty string = clear = validate as ""); fall back to the existing
	// stored value when the key was absent (unchanged semantics).
	tzForValidation := ""
	if tzPresent {
		if tzValue != nil {
			tzForValidation = *tzValue
		}
	} else if sc.Timezone != nil {
		tzForValidation = *sc.Timezone
	}
	if err := schedulespec.Validate(name, cronExpr, tzForValidation, cmd, timeout, overlap, missed); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	var cmdJSONPtr *string
	if req.Command != nil {
		b, _ := json.Marshal(*req.Command)
		v := string(b)
		cmdJSONPtr = &v
	}

	updateParams := db.UpdateScheduleParams{
		Name: req.Name, CronExpr: req.CronExpr, CommandJSON: cmdJSONPtr,
		Enabled: req.Enabled, TimeoutSeconds: req.TimeoutSeconds,
		OverlapPolicy: req.OverlapPolicy, MissedPolicy: req.MissedPolicy,
	}
	if tzPresent {
		// Key was in the body: update the column regardless of value.
		// tzValue=nil -> stored NULL (inherit); tzValue=&v -> store the zone.
		updateParams.SetTimezone = true
		updateParams.Timezone = tzValue
	}

	if err := s.store.UpdateSchedule(id, updateParams); err != nil {
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
	writeJSON(w, http.StatusOK, toScheduleDTO(fresh, nil, s.cfg.Scheduler.Location))
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
	if s.jobs == nil {
		writeError(w, http.StatusServiceUnavailable, "scheduler unavailable")
		return
	}
	// Run admits the run synchronously (insert row + overlap policy) and
	// executes the command in its own goroutine, so this does not block on
	// the run finishing but does surface admission failures to the caller.
	runID, err := s.jobs.Run(id, "manual", uid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start run: "+err.Error())
		return
	}
	s.audit(r, "schedule_run_manual", "schedule", fmt.Sprintf("%d", id), "")
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "started", "run_id": runID})
}

// GET /api/apps/{slug}/schedules/{id}/runs
func (s *Server) handleListScheduleRuns(w http.ResponseWriter, r *http.Request) {
	app, _, ok := s.requireExplicitAppAccess(w, r, chi.URLParam(r, "slug"))
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

// GET /api/apps/{slug}/schedules/{id}/runs/{run_id}
func (s *Server) handleGetScheduleRun(w http.ResponseWriter, r *http.Request) {
	app, _, ok := s.requireExplicitAppAccess(w, r, chi.URLParam(r, "slug"))
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
	run, err := s.store.GetScheduleRun(runID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if run.ScheduleID != scheduleID {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	sched, err := s.store.GetSchedule(run.ScheduleID)
	if err != nil || sched.AppID != app.ID {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, http.StatusOK, run)
}

// GET /api/apps/{slug}/schedules/{id}/runs/{run_id}/logs
//
// Run logs are stderr+stdout of the scheduled command. They can include
// values that the command happened to print which originate from the
// app's secret env vars (e.g. an HTTP client logging an Authorization
// header), so the endpoint requires manage rights — matching the policy
// applied to the live app log stream at GET /api/apps/{slug}/logs.
func (s *Server) handleScheduleRunLogs(w http.ResponseWriter, r *http.Request) {
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
	run, err := s.store.GetScheduleRun(runID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if run.ScheduleID != scheduleID {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	sched, err := s.store.GetSchedule(run.ScheduleID)
	if err != nil || sched.AppID != app.ID {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if run.LogPath == "" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if _, err := os.Stat(run.LogPath); err != nil {
		writeError(w, http.StatusNotFound, "log file not found")
		return
	}

	// follow selects the response shape, mirroring GET /api/apps/{slug}/logs:
	// follow=false returns a one-shot plain-text body (no SSE framing) for
	// scripted callers; follow=true (the default) returns an event stream.
	// Live following only happens while the run is still running; a finished
	// run's static log is flushed once and the stream closes.
	follow := true
	if raw := r.URL.Query().Get("follow"); raw != "" {
		switch raw {
		case "true", "1":
			follow = true
		case "false", "0":
			follow = false
		default:
			writeError(w, http.StatusBadRequest, "follow must be true or false")
			return
		}
	}
	if !follow {
		writeLogFilePlain(w, run.LogPath, defaultLogTail)
		return
	}
	if run.Status == "running" {
		// Live run: follow the log but stop when the run reaches a terminal
		// state, so the stream is finite and clients can read the final exit
		// code afterwards.
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		go s.cancelWhenRunDone(ctx, cancel, runID)
		streamLogFile(w, r.WithContext(ctx), run.LogPath, true)
		return
	}
	// Finished run: a one-shot SSE dump of the static log, then close.
	streamLogFile(w, r, run.LogPath, false)
}

// terminalRunPollInterval is how often a live run-log stream polls the run's
// status to detect completion.
const terminalRunPollInterval = 500 * time.Millisecond

// finalLineDrainGrace gives the log follower a brief window to deliver the
// last lines a process flushed just before exiting, before the stream closes.
const finalLineDrainGrace = 300 * time.Millisecond

// cancelWhenRunDone polls the run's status and cancels ctx once the run leaves
// the "running" state, ending a live log-follow stream.
func (s *Server) cancelWhenRunDone(ctx context.Context, cancel context.CancelFunc, runID int64) {
	ticker := time.NewTicker(terminalRunPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run, err := s.store.GetScheduleRun(runID)
			if err != nil {
				continue
			}
			if run.Status != "running" {
				time.Sleep(finalLineDrainGrace)
				cancel()
				return
			}
		}
	}
}

// effectiveTZLabel returns a human-readable label of the timezone that will
// actually be used for a newly created schedule, for audit logging.
func effectiveTZLabel(storedTZ *string, serverDefault *time.Location) string {
	if storedTZ != nil && *storedTZ != "" {
		return *storedTZ
	}
	if serverDefault == nil {
		return "UTC"
	}
	return serverDefault.String() + " (server default)"
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
	// Warning is set on grant under the native runtime, where the read-only
	// mount is a convention only (the source data dir is symlinked and the
	// filesystem still permits writes through it). Empty under the Docker
	// runtime, which enforces read-only at the OS level.
	Warning string `json:"warning,omitempty"`
}

// sharedDataNativeWarning explains that the read-only shared-data mount is not
// OS-enforced under the native runtime. Surfaced on grant so operators can
// choose the Docker runtime when they need real enforcement.
const sharedDataNativeWarning = "Read-only is a convention under the native runtime: the source data dir is symlinked and writes through it are not blocked. Switch to the Docker runtime for OS-level read-only enforcement."

// GET /api/apps/{slug}/shared-data
func (s *Server) handleListSharedData(w http.ResponseWriter, r *http.Request) {
	app, _, ok := s.requireExplicitAppAccess(w, r, chi.URLParam(r, "slug"))
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
	// The caller must have explicit access to the source app. Public or
	// shared visibility is not enough — granting a read-only mount of
	// another app's data dir into your own would otherwise let anyone with
	// developer access to any public app exfiltrate that app's data over a
	// schedule run they fully control. See A.2 in v0.2.2 audit.
	ok, err = s.hasExplicitAccess(user, src)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	if err := s.store.GrantSharedData(app.ID, src.ID); err != nil {
		switch {
		case errors.Is(err, db.ErrSelfMount), errors.Is(err, db.ErrSharedDataCycle):
			writeError(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, db.ErrDuplicateMount):
			writeError(w, http.StatusConflict, err.Error())
		default:
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	s.audit(r, "shared_data_grant", "app", app.Slug, fmt.Sprintf(`{"source":%q}`, src.Slug))
	dto := sharedDataDTO{SourceSlug: src.Slug, SourceID: src.ID}
	if s.cfg.Runtime.Mode != "docker" {
		dto.Warning = sharedDataNativeWarning
	}
	writeJSON(w, http.StatusCreated, dto)
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
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "data not mounted from this source")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "shared_data_revoke", "app", app.Slug, fmt.Sprintf(`{"source":%q}`, src.Slug))
	w.WriteHeader(http.StatusNoContent)
}
