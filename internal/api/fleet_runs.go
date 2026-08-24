package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/fleet"
	"github.com/rvben/shinyhub/internal/provenance"
)

const fleetRunHeader = "X-Shinyhub-Run-Id"

const fleetRunAbandonedAfter = 2 * time.Minute

type registerFleetRunRequest struct {
	RunID      string              `json:"run_id"`
	FleetID    string              `json:"fleet_id"`
	Kind       string              `json:"kind"`
	Provenance provenance.Metadata `json:"provenance"`
}

func (s *Server) handleRegisterFleetRun(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if !canCreateApps(u) {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	var req registerFleetRunRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, provenance.MaxEncodedBytes+1024))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid fleet run")
		return
	}
	if !provenance.ValidRunID(req.RunID) || !fleet.ValidFleetID(req.FleetID) || req.Kind != "fleet_apply" {
		writeError(w, http.StatusUnprocessableEntity, "invalid run_id, fleet_id, or kind")
		return
	}
	if err := req.Provenance.Validate(); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	run, created, err := s.store.CreateFleetRun(db.CreateFleetRunParams{
		ID: req.RunID, FleetID: req.FleetID, Kind: req.Kind, Provenance: req.Provenance, UserID: &u.ID,
	})
	if errors.Is(err, db.ErrFleetRunConflict) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if created {
		s.store.LogAuditEvent(db.AuditEventParams{UserID: &u.ID, Action: "fleet_apply_started", ResourceType: "fleet", ResourceID: req.FleetID, IPAddress: s.ClientIP(r), RunID: req.RunID})
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, map[string]any{"run": run})
}

type updateFleetRunRequest struct {
	Status     string `json:"status"`
	ExitCode   *int   `json:"exit_code,omitempty"`
	ExitReason string `json:"exit_reason,omitempty"`
}

type fleetRunView struct {
	*db.FleetRun
	ObservedStatus string `json:"observed_status"`
}

func observedFleetRunStatus(run *db.FleetRun, now time.Time) string {
	if run.Status == "running" && now.Sub(run.HeartbeatAt) > fleetRunAbandonedAfter {
		return "abandoned"
	}
	return run.Status
}

func (s *Server) authorizedFleetRun(w http.ResponseWriter, r *http.Request) (*db.FleetRun, *auth.ContextUser, bool) {
	u := auth.UserFromContext(r.Context())
	if !canCreateApps(u) {
		writeError(w, http.StatusForbidden, "forbidden")
		return nil, nil, false
	}
	run, err := s.store.GetFleetRun(chi.URLParam(r, "id"))
	if errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, "fleet run not found")
		return nil, nil, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return nil, nil, false
	}
	if u.Role != "admin" && (run.UserID == nil || *run.UserID != u.ID) {
		writeError(w, http.StatusForbidden, "forbidden")
		return nil, nil, false
	}
	return run, u, true
}

func (s *Server) handleGetFleetRun(w http.ResponseWriter, r *http.Request) {
	run, _, ok := s.authorizedFleetRun(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run": fleetRunView{FleetRun: run, ObservedStatus: observedFleetRunStatus(run, time.Now())}})
}

func (s *Server) handleUpdateFleetRun(w http.ResponseWriter, r *http.Request) {
	run, u, ok := s.authorizedFleetRun(w, r)
	if !ok {
		return
	}
	var req updateFleetRunRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid fleet run update")
		return
	}
	var err error
	if req.Status == "running" {
		if req.ExitCode != nil || req.ExitReason != "" {
			writeError(w, http.StatusUnprocessableEntity, "a heartbeat cannot include an exit result")
			return
		}
		err = s.store.TouchFleetRun(run.ID)
	} else {
		if req.ExitCode == nil || *req.ExitCode < 0 || *req.ExitCode > 255 || !validFleetStateText(req.ExitReason, 2048) {
			writeError(w, http.StatusUnprocessableEntity, "a terminal update requires a valid exit_code and exit_reason")
			return
		}
		err = s.store.FinishFleetRun(run.ID, req.Status, *req.ExitCode, req.ExitReason)
	}
	if errors.Is(err, db.ErrFleetRunFinished) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if req.Status != "running" {
		s.store.LogAuditEvent(db.AuditEventParams{UserID: &u.ID, Action: "fleet_apply_finished", ResourceType: "fleet", ResourceID: run.FleetID, IPAddress: s.ClientIP(r), RunID: run.ID})
	}
	w.WriteHeader(http.StatusNoContent)
}

// knownFleetRunID treats malformed and unregistered headers as absent. This is
// intentional compatibility behavior: old CLIs and direct API clients may send
// no registration, while the mutation itself must retain its normal semantics.
func (s *Server) knownFleetRunID(r *http.Request) string {
	id := r.Header.Get(fleetRunHeader)
	if !provenance.ValidRunID(id) {
		return ""
	}
	if _, err := s.store.GetFleetRun(id); err != nil {
		return ""
	}
	return id
}
