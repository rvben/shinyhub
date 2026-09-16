package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
)

func (s *Server) entitlementAudit(r *http.Request) db.AuditEventParams {
	u := auth.UserFromContext(r.Context())
	p := db.AuditEventParams{UserID: &u.ID, IPAddress: s.ClientIP(r)}
	requestAuditCredential(r, &p)
	return p
}

func entitlementError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, db.ErrNotFound):
		writeError(w, http.StatusNotFound, "app, entitlement, or user not found")
	case errors.Is(err, db.ErrInvalidEntitlement):
		writeError(w, http.StatusBadRequest, "use an entitlement name of 1–64 lowercase letters, digits, underscores or hyphens starting with a letter; description at most 512 bytes; specify exactly one valid user or group")
	case errors.Is(err, db.ErrEntitlementLimit), errors.Is(err, db.ErrEntitlementInUse):
		writeError(w, http.StatusConflict, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "app entitlement operation failed")
	}
}

func (s *Server) requireEntitlementManagement(w http.ResponseWriter, r *http.Request) (*db.App, bool) {
	app, ok := s.requireManageApp(w, r, chi.URLParam(r, "slug"))
	if !ok {
		return nil, false
	}
	return app, true
}

func (s *Server) handleListAppEntitlements(w http.ResponseWriter, r *http.Request) {
	app, ok := s.requireEntitlementManagement(w, r)
	if !ok {
		return
	}
	items, err := s.store.ListAppEntitlements(app.ID)
	if err != nil {
		entitlementError(w, err)
		return
	}
	limit, offset := parsePagination(r)
	writeList(w, items, limit, offset, nil)
}

func (s *Server) handleDefineAppEntitlement(w http.ResponseWriter, r *http.Request) {
	app, ok := s.requireEntitlementManagement(w, r)
	if !ok {
		return
	}
	var req struct {
		Description *string `json:"description"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid entitlement definition")
		return
	}
	err := s.store.DefineAppEntitlement(app.ID, db.DefineAppEntitlementParams{Name: chi.URLParam(r, "entitlement"), Description: req.Description}, s.entitlementAudit(r))
	if err != nil {
		entitlementError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteAppEntitlement(w http.ResponseWriter, r *http.Request) {
	app, ok := s.requireEntitlementManagement(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteAppEntitlement(app.ID, chi.URLParam(r, "entitlement"), s.entitlementAudit(r)); err != nil {
		entitlementError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type entitlementPrincipalRequest struct {
	UserID   int64  `json:"user_id"`
	Username string `json:"username"`
	Group    string `json:"group"`
}

func (s *Server) resolveEntitlementPrincipal(req entitlementPrincipalRequest) (db.EntitlementPrincipal, error) {
	n := 0
	if req.UserID != 0 {
		n++
	}
	if req.Username != "" {
		n++
	}
	if req.Group != "" {
		n++
	}
	if n != 1 || req.UserID < 0 {
		return db.EntitlementPrincipal{}, db.ErrInvalidEntitlement
	}
	if req.Username != "" {
		u, err := s.store.GetUserByUsername(req.Username)
		if err != nil {
			return db.EntitlementPrincipal{}, err
		}
		return db.EntitlementPrincipal{UserID: u.ID}, nil
	}
	return db.EntitlementPrincipal{UserID: req.UserID, Group: req.Group}, nil
}

func (s *Server) handleSetAppEntitlementGrant(w http.ResponseWriter, r *http.Request) {
	app, ok := s.requireEntitlementManagement(w, r)
	if !ok {
		return
	}
	var req entitlementPrincipalRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid entitlement principal")
		return
	}
	principal, err := s.resolveEntitlementPrincipal(req)
	if err != nil {
		entitlementError(w, err)
		return
	}
	err = s.store.SetAppEntitlementGrant(app.ID, chi.URLParam(r, "entitlement"), principal, r.Method == http.MethodPost, s.entitlementAudit(r))
	if err != nil {
		entitlementError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListAppEntitlementGrants(w http.ResponseWriter, r *http.Request) {
	app, ok := s.requireEntitlementManagement(w, r)
	if !ok {
		return
	}
	items, err := s.store.ListAppEntitlementGrants(app.ID, 0)
	if err != nil {
		entitlementError(w, err)
		return
	}
	limit, offset := parsePagination(r)
	writeList(w, items, limit, offset, nil)
}

func (s *Server) handleEffectiveAppEntitlements(w http.ResponseWriter, r *http.Request) {
	app, ok := s.requireEntitlementManagement(w, r)
	if !ok {
		return
	}
	req := entitlementPrincipalRequest{Username: r.URL.Query().Get("username")}
	if raw := r.URL.Query().Get("user_id"); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			entitlementError(w, db.ErrInvalidEntitlement)
			return
		}
		req.UserID = id
	}
	principal, err := s.resolveEntitlementPrincipal(req)
	if err != nil {
		entitlementError(w, err)
		return
	}
	if _, err := s.store.GetUserByID(principal.UserID); err != nil {
		entitlementError(w, err)
		return
	}
	grants, err := s.store.ListAppEntitlementGrants(app.ID, principal.UserID)
	if err != nil {
		entitlementError(w, err)
		return
	}
	names := []string{}
	for _, grant := range grants {
		if len(names) == 0 || names[len(names)-1] != grant.Entitlement {
			names = append(names, grant.Entitlement)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"user_id": principal.UserID, "entitlements": names, "sources": grants})
}
