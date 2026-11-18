package api

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/rvben/shinyhub/internal/db"
	policy "github.com/rvben/shinyhub/internal/entitlementpolicy"
)

type entitlementPolicyRequest struct {
	Owner   string        `json:"owner"`
	Desired policy.Policy `json:"desired"`
	Token   string        `json:"token,omitempty"`
}

func decodeEntitlementPolicy(w http.ResponseWriter, r *http.Request) (entitlementPolicyRequest, bool) {
	var req entitlementPolicyRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil || decoder.Decode(new(any)) != io.EOF || !policy.ValidOwner(req.Owner) {
		writeError(w, http.StatusBadRequest, "invalid entitlement policy request")
		return req, false
	}
	if err := req.Desired.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return req, false
	}
	return req, true
}

func (s *Server) handlePreviewEntitlementPolicy(w http.ResponseWriter, r *http.Request) {
	app, ok := s.requireEntitlementManagement(w, r)
	if !ok {
		return
	}
	req, ok := decodeEntitlementPolicy(w, r)
	if !ok {
		return
	}
	preview, err := s.store.PreviewEntitlementPolicy(r.Context(), app.ID, req.Owner, req.Desired)
	if err != nil {
		entitlementError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, preview)
}

func (s *Server) handleApplyEntitlementPolicy(w http.ResponseWriter, r *http.Request) {
	app, ok := s.requireEntitlementManagement(w, r)
	if !ok {
		return
	}
	req, ok := decodeEntitlementPolicy(w, r)
	if !ok {
		return
	}
	if req.Token == "" || req.Desired == nil {
		entitlementError(w, r, db.ErrInvalidEntitlement)
		return
	}
	audit := s.entitlementAudit(r)
	audit.RunID = s.knownFleetRunID(r)
	if err := s.store.ApplyEntitlementPolicy(app.ID, req.Owner, req.Token, req.Desired, audit); err != nil {
		entitlementError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRelinquishEntitlementPolicy(w http.ResponseWriter, r *http.Request) {
	app, ok := s.requireEntitlementManagement(w, r)
	if !ok {
		return
	}
	req, ok := decodeEntitlementPolicy(w, r)
	if !ok {
		return
	}
	if req.Desired != nil || req.Token != "" {
		writeError(w, http.StatusBadRequest, "relinquish accepts only the fleet owner")
		return
	}
	if err := s.store.RelinquishEntitlementPolicy(app.ID, req.Owner, s.entitlementAudit(r)); err != nil {
		entitlementError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
