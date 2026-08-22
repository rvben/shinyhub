package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/go-chi/chi/v5"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/fleet"
)

const (
	fleetConvergenceInSync     = "in_sync"
	fleetConvergenceIncomplete = "incomplete"
)

var fleetStateKeys = map[string]bool{
	"source": true, "visibility": true, "name": true, "description": true,
	"icon": true, "project": true, "hibernate_timeout_minutes": true,
	"replicas": true, "max_sessions_per_replica": true, "render_seconds": true,
	"identity_headers": true, "min_warm_replicas": true, "memory_limit_mb": true,
	"cpu_quota_percent": true, "worker_isolation": true, "worker_grouped_size": true,
	"worker_max_workers": true, "worker_warm_spares": true,
	"worker_max_session_lifetime_secs": true, "autoscale": true,
}

type recordFleetStateRequest struct {
	Status               string                  `json:"status"`
	DesiredContentDigest string                  `json:"desired_content_digest"`
	Declaration          []db.FleetDeclaredValue `json:"declaration"`
	Error                string                  `json:"error,omitempty"`
}

type fleetStateChange struct {
	Key     string `json:"key"`
	Current string `json:"current"`
	Fleet   string `json:"fleet"`
}

type fleetApplicationView struct {
	*db.FleetStateRun
	AppliedAt *time.Time `json:"applied_at,omitempty"`
}

type appFleetStateView struct {
	Status      string                `json:"status"`
	FleetID     string                `json:"fleet_id"`
	Changes     []fleetStateChange    `json:"changes,omitempty"`
	Application *fleetApplicationView `json:"application,omitempty"`
	Attempt     *db.FleetStateRun     `json:"attempt,omitempty"`
	Error       string                `json:"error,omitempty"`
	UpdatedAt   *time.Time            `json:"updated_at,omitempty"`
}

func quoted(v string) string { return fmt.Sprintf("%q", v) }

// currentFleetValues maps database values into fleet.Diff's canonical display
// representation. This is deliberately server-side: the browser receives an
// already-proven comparison and never tries to reconstruct manifest semantics.
func currentFleetValues(app *db.App) map[string]string {
	hibernate := "(default)"
	if app.HibernateTimeoutMinutes != nil {
		hibernate = fmt.Sprintf("%d", *app.HibernateTimeoutMinutes)
	}
	identity := "(unset)"
	if app.IdentityHeaders != nil {
		identity = fmt.Sprintf("%t", *app.IdentityHeaders)
	}
	memory := "(unset)"
	if app.MemoryLimitMB != nil {
		memory = fmt.Sprintf("%d", *app.MemoryLimitMB)
	}
	cpu := "(unset)"
	if app.CPUQuotaPercent != nil {
		cpu = fmt.Sprintf("%d", *app.CPUQuotaPercent)
	}
	return map[string]string{
		"source":                           app.ContentDigest,
		"visibility":                       app.Access,
		"name":                             quoted(app.Name),
		"description":                      quoted(app.Description),
		"icon":                             quoted(app.IconEmoji),
		"project":                          quoted(app.ProjectSlug),
		"hibernate_timeout_minutes":        hibernate,
		"replicas":                         fmt.Sprintf("%d", app.Replicas),
		"max_sessions_per_replica":         fmt.Sprintf("%d", app.MaxSessionsPerReplica),
		"render_seconds":                   fmt.Sprintf("%g", app.RenderSeconds),
		"identity_headers":                 identity,
		"min_warm_replicas":                fmt.Sprintf("%d", app.MinWarmReplicas),
		"memory_limit_mb":                  memory,
		"cpu_quota_percent":                cpu,
		"worker_isolation":                 quoted(app.WorkerIsolation),
		"worker_grouped_size":              fmt.Sprintf("%d", app.WorkerGroupedSize),
		"worker_max_workers":               fmt.Sprintf("%d", app.WorkerMaxWorkers),
		"worker_warm_spares":               fmt.Sprintf("%d", app.WorkerWarmSpares),
		"worker_max_session_lifetime_secs": fmt.Sprintf("%d", app.WorkerMaxSessionLifetimeSecs),
		"autoscale": fleet.AutoscaleDisplay(app.AutoscaleEnabled, app.AutoscaleMinReplicas,
			app.AutoscaleMaxReplicas, app.AutoscaleTarget),
	}
}

func fleetIDFromMarker(marker *string) string {
	if marker == nil || !strings.HasPrefix(*marker, "fleet:") {
		return ""
	}
	return strings.TrimPrefix(*marker, "fleet:")
}

func (s *Server) appFleetState(app *db.App) (*appFleetStateView, error) {
	fleetID := fleetIDFromMarker(app.ManagedBy)
	if fleetID == "" {
		return nil, nil
	}
	view := &appFleetStateView{Status: "owned", FleetID: fleetID}
	state, err := s.store.GetAppFleetState(app.ID)
	if err != nil || state == nil {
		return view, err
	}
	if state.LatestRun == nil || state.LatestRun.FleetID != fleetID {
		return view, nil
	}
	if state.ConvergenceStatus == fleetConvergenceIncomplete {
		view.Status = fleetConvergenceIncomplete
		view.Attempt = state.LatestRun
		view.Error = state.ConvergenceError
		updatedAt := state.ConvergenceUpdatedAt
		view.UpdatedAt = &updatedAt
		if state.SuccessfulRun != nil && state.SuccessfulRun.FleetID == fleetID {
			view.Application = &fleetApplicationView{FleetStateRun: state.SuccessfulRun, AppliedAt: state.AppliedAt}
		}
		return view, nil
	}
	if state.SuccessfulRun == nil || state.SuccessfulRun.FleetID != fleetID || state.AppliedAt == nil {
		return view, nil
	}
	view.Status = fleetConvergenceInSync
	view.Application = &fleetApplicationView{FleetStateRun: state.SuccessfulRun, AppliedAt: state.AppliedAt}
	current := currentFleetValues(app)
	for _, declared := range state.Declaration {
		if got, ok := current[declared.Key]; ok && got != declared.Desired {
			view.Changes = append(view.Changes, fleetStateChange{Key: declared.Key, Current: got, Fleet: declared.Desired})
		}
	}
	if state.DesiredContentDigest != "" && app.ContentDigest != state.DesiredContentDigest {
		view.Changes = append(view.Changes, fleetStateChange{Key: "source", Current: app.ContentDigest, Fleet: state.DesiredContentDigest})
	}
	if len(view.Changes) == 0 {
		return view, nil
	}
	view.Status = "temporary_changes"
	return view, nil
}

func validFleetStateText(v string, max int) bool {
	return len(v) <= max && strings.IndexFunc(v, unicode.IsControl) < 0
}

func validateFleetDeclaration(values []db.FleetDeclaredValue, allowEmpty bool) error {
	if len(values) == 0 && !allowEmpty {
		return fmt.Errorf("declaration is required")
	}
	if len(values) > len(fleetStateKeys) {
		return fmt.Errorf("declaration has too many fields")
	}
	seen := make(map[string]bool, len(values))
	for _, item := range values {
		if !fleetStateKeys[item.Key] || item.Key == "source" {
			return fmt.Errorf("invalid declaration key %q", item.Key)
		}
		if seen[item.Key] {
			return fmt.Errorf("duplicate declaration key %q", item.Key)
		}
		if !validFleetStateText(item.Desired, 2048) {
			return fmt.Errorf("invalid declared value for %q", item.Key)
		}
		seen[item.Key] = true
	}
	return nil
}

// handleRecordAppFleetState is called only by a capability-aware fleet apply.
// It is intentionally separate from ordinary app mutations: fleet provenance
// becomes a durable baseline only after convergence has actually finished.
func (s *Server) handleRecordAppFleetState(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	app, ok := s.requireManageApp(w, r, slug)
	if !ok {
		return
	}
	runID := s.knownFleetRunID(r)
	if runID == "" {
		writeError(w, http.StatusUnprocessableEntity, "a registered fleet run id is required")
		return
	}
	run, err := s.store.GetFleetRun(runID)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "unknown fleet run")
		return
	}
	if fleetIDFromMarker(app.ManagedBy) != run.FleetID {
		writeError(w, http.StatusConflict, "app is not owned by this fleet run")
		return
	}
	var req recordFleetStateRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid fleet state")
		return
	}
	switch req.Status {
	case fleetConvergenceInSync:
		if !validFleetStateText(req.DesiredContentDigest, 128) || req.DesiredContentDigest == "" {
			writeError(w, http.StatusUnprocessableEntity, "desired_content_digest is required")
			return
		}
		if err := validateFleetDeclaration(req.Declaration, false); err != nil {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		current := currentFleetValues(app)
		if current["source"] != req.DesiredContentDigest {
			writeError(w, http.StatusConflict, "fleet source no longer matches the app")
			return
		}
		for _, declared := range req.Declaration {
			if current[declared.Key] != declared.Desired {
				writeError(w, http.StatusConflict, "fleet declaration no longer matches the app")
				return
			}
		}
		if err := s.store.RecordAppFleetSuccess(app.ID, runID, req.DesiredContentDigest, req.Declaration); err != nil {
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
	case fleetConvergenceIncomplete:
		if !validFleetStateText(req.Error, 2048) {
			writeError(w, http.StatusUnprocessableEntity, "invalid convergence error")
			return
		}
		if err := s.store.RecordAppFleetIncomplete(app.ID, runID, req.Error); err != nil {
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
	default:
		writeError(w, http.StatusUnprocessableEntity, "status must be in_sync or incomplete")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
