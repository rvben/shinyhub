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

// callerID returns the ID pointer of the authenticated user, or nil if not present.
// Used to keep audit calls concise.
func callerID(r *http.Request) *int64 {
	if u := auth.UserFromContext(r.Context()); u != nil {
		return &u.ID
	}
	return nil
}

// requireAdmin returns the user from context and writes 403 if they are not an admin.
func requireAdmin(w http.ResponseWriter, r *http.Request) (*auth.ContextUser, bool) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return nil, false
	}
	if u.Role != "admin" {
		writeError(w, http.StatusForbidden, "forbidden")
		return nil, false
	}
	return u, true
}

// refuseSystemUser writes a response and returns true when the caller should
// abort: either the target is a server-managed system user (403) or the DB
// lookup failed unexpectedly (500). ErrNotFound is allowed through so the
// downstream handler emits its own 404. Fails closed — never silently lets a
// mutation proceed when the target identity cannot be confirmed.
func (s *Server) refuseSystemUser(w http.ResponseWriter, id int64) bool {
	u, err := s.store.GetUserByID(id)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return false
		}
		writeError(w, http.StatusInternalServerError, "internal server error")
		return true
	}
	if db.IsSystemUser(u.Username) {
		writeError(w, http.StatusForbidden, "cannot modify system user")
		return true
	}
	return false
}

// userResponse is the safe public view of a user (no password hash).
type userResponse struct {
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	Role        string `json:"role"`
	DisplayName string `json:"display_name"`
	CreatedAt   string `json:"created_at"`
}

func toUserResponse(u *db.User) userResponse {
	return userResponse{
		ID:          u.ID,
		Username:    u.Username,
		Role:        u.Role,
		DisplayName: u.DisplayName,
		CreatedAt:   u.CreatedAt.Format("2006-01-02T15:04:05Z"),
	}
}

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}

	users, err := s.store.ListUsers()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	resp := make([]userResponse, len(users))
	for i, u := range users {
		resp[i] = toUserResponse(u)
	}
	// ListUsers already orders by username, so the page is stably sorted.
	limit, offset := parsePagination(r)
	writeList(w, resp, limit, offset, nil)
}

type createUserRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}

	var req createUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request")
		return
	}
	if req.Username == "" {
		writeError(w, http.StatusBadRequest, "username is required")
		return
	}
	if len(req.Password) < 8 {
		writeError(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}
	role := req.Role
	if role == "" {
		role = "developer"
	}
	if !auth.IsValidGlobalRole(role) {
		writeError(w, http.StatusBadRequest, "role must be viewer, developer, operator, or admin")
		return
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if err := s.store.CreateUser(db.CreateUserParams{
		Username:     req.Username,
		PasswordHash: hash,
		Role:         role,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	user, err := s.store.GetUserByUsername(req.Username)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	s.store.LogAuditEvent(db.AuditEventParams{
		UserID:       callerID(r),
		Action:       "create_user",
		ResourceType: "user",
		ResourceID:   req.Username,
		IPAddress:    s.ClientIP(r),
	})
	writeJSON(w, http.StatusCreated, toUserResponse(user))
}

type patchUserRequest struct {
	Role string `json:"role"`
}

func (s *Server) handlePatchUser(w http.ResponseWriter, r *http.Request) {
	admin, ok := requireAdmin(w, r)
	if !ok {
		return
	}

	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}
	if s.refuseSystemUser(w, id) {
		return
	}
	// An admin cannot change their own role via the API (the UI also blocks it):
	// self-demotion is a footgun that can strand the last admin.
	if admin.ID == id {
		writeError(w, http.StatusForbidden, "cannot change your own role")
		return
	}

	var req patchUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request")
		return
	}

	// Capture the prior role so the audit trail distinguishes a privilege
	// escalation from a downgrade.
	existing, err := s.store.GetUserByID(id)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	oldRole := existing.Role

	if req.Role == "" {
		// Empty role clears the manual override: the user returns to group/default governance.
		if err := s.store.ClearManualRole(id, AuthMappings(s.cfg.Auth.GroupRoleMappings), s.jitOAuthRole()); err != nil {
			if errors.Is(err, db.ErrLastAdmin) {
				writeError(w, http.StatusConflict, "cannot remove the last admin")
				return
			}
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
	} else {
		if !auth.IsValidGlobalRole(req.Role) {
			writeError(w, http.StatusBadRequest, "role must be viewer, developer, operator, or admin")
			return
		}
		if err := s.store.SetManualRole(id, req.Role); err != nil {
			if errors.Is(err, db.ErrLastAdmin) {
				writeError(w, http.StatusConflict, "cannot remove the last admin")
				return
			}
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
	}

	user, err := s.store.GetUserByID(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	roleDetail, _ := json.Marshal(map[string]string{"old_role": oldRole, "new_role": user.Role})
	s.store.LogAuditEvent(db.AuditEventParams{
		UserID:       callerID(r),
		Action:       "update_user",
		ResourceType: "user",
		ResourceID:   strconv.FormatInt(id, 10),
		Detail:       string(roleDetail),
		IPAddress:    s.ClientIP(r),
	})
	writeJSON(w, http.StatusOK, toUserResponse(user))
}

type patchUserPasswordRequest struct {
	Password string `json:"password"`
}

func (s *Server) handlePatchUserPassword(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}

	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}
	if s.refuseSystemUser(w, id) {
		return
	}

	var req patchUserPasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request")
		return
	}
	if len(req.Password) < 8 {
		writeError(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if err := s.store.UpdateUserPassword(id, hash); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	s.store.LogAuditEvent(db.AuditEventParams{
		UserID:       callerID(r),
		Action:       "reset_user_password",
		ResourceType: "user",
		ResourceID:   strconv.FormatInt(id, 10),
		IPAddress:    s.ClientIP(r),
	})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	admin, ok := requireAdmin(w, r)
	if !ok {
		return
	}

	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}
	if s.refuseSystemUser(w, id) {
		return
	}
	// An admin cannot delete their own account via the API (the UI also blocks
	// it): self-deletion can strand the instance with no admin.
	if admin.ID == id {
		writeError(w, http.StatusForbidden, "cannot delete your own account")
		return
	}

	if err := s.store.DeleteUser(id); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		if errors.Is(err, db.ErrLastAdmin) {
			writeError(w, http.StatusConflict, "cannot remove the last admin")
			return
		}
		if errors.Is(err, db.ErrUserOwnsApps) {
			writeError(w, http.StatusConflict, "user still owns apps; transfer them first (shinyhub apps transfer <slug> <new-owner>) or delete them")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	s.store.LogAuditEvent(db.AuditEventParams{
		UserID:       callerID(r),
		Action:       "delete_user",
		ResourceType: "user",
		ResourceID:   strconv.FormatInt(id, 10),
		IPAddress:    s.ClientIP(r),
	})
	w.WriteHeader(http.StatusNoContent)
}
