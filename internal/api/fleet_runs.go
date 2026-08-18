package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/fleet"
	"github.com/rvben/shinyhub/internal/provenance"
)

const fleetRunHeader = "X-Shinyhub-Run-Id"

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
