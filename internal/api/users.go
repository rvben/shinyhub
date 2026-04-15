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

// userResponse is the safe public view of a user (no password hash).
type userResponse struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	Role      string `json:"role"`
	CreatedAt string `json:"created_at"`
}

func toUserResponse(u *db.User) userResponse {
	return userResponse{
		ID:        u.ID,
		Username:  u.Username,
		Role:      u.Role,
		CreatedAt: u.CreatedAt.Format("2006-01-02T15:04:05Z"),
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
	writeJSON(w, http.StatusOK, resp)
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
	if req.Username == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "username and password are required")
		return
	}
	role := req.Role
	if role == "" {
		role = "developer"
	}
	if role != "admin" && role != "developer" && role != "operator" {
		writeError(w, http.StatusBadRequest, "role must be admin, developer, or operator")
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
		IPAddress:    clientIP(r),
	})
	writeJSON(w, http.StatusCreated, toUserResponse(user))
}

type patchUserRequest struct {
	Role string `json:"role"`
}

func (s *Server) handlePatchUser(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}

	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}

	var req patchUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request")
		return
	}
	if req.Role == "" {
		writeError(w, http.StatusBadRequest, "role is required")
		return
	}
	if req.Role != "admin" && req.Role != "developer" && req.Role != "operator" {
		writeError(w, http.StatusBadRequest, "role must be admin, developer, or operator")
		return
	}

	if err := s.store.UpdateUserRole(id, req.Role); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	user, err := s.store.GetUserByID(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	s.store.LogAuditEvent(db.AuditEventParams{
		UserID:       callerID(r),
		Action:       "update_user",
		ResourceType: "user",
		ResourceID:   strconv.FormatInt(id, 10),
		IPAddress:    clientIP(r),
	})
	writeJSON(w, http.StatusOK, toUserResponse(user))
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}

	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}

	if err := s.store.DeleteUser(id); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
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
		IPAddress:    clientIP(r),
	})
	w.WriteHeader(http.StatusNoContent)
}
