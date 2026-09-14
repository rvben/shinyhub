package api

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
)

func invitationDigest(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}

func (s *Server) requireInvitationAdmin(w http.ResponseWriter, r *http.Request) (*auth.ContextUser, bool) {
	user, ok := requireAdmin(w, r)
	if !ok {
		return nil, false
	}
	if user.IsServiceAccount() {
		writeError(w, http.StatusForbidden, "Only people with the Admin role can manage invitations.")
		return nil, false
	}
	return user, true
}

func (s *Server) handleCreateUserInvitation(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireInvitationAdmin(w, r)
	if !ok {
		return
	}
	if !s.cfg.Auth.LocalLoginEnabled() {
		writeError(w, http.StatusConflict, "Password sign-in is disabled. Share the SSO sign-in link instead.")
		return
	}
	var req struct {
		Username string `json:"username"`
		Role     string `json:"role"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req) != nil {
		writeError(w, 400, "Enter a username and role.")
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" || utf8.RuneCountInString(req.Username) > 64 || strings.ContainsAny(req.Username, "\r\n\t") {
		writeError(w, 400, "Use a username between 1 and 64 characters, without line breaks or tabs.")
		return
	}
	if req.Role == "" {
		req.Role = "viewer"
	}
	if !auth.IsValidGlobalRole(req.Role) {
		writeError(w, 400, "Choose Viewer, Developer, Operator, or Admin.")
		return
	}
	var secret [32]byte
	var id [16]byte
	if _, err := rand.Read(secret[:]); err != nil {
		writeError(w, 500, "Could not create invitation.")
		return
	}
	if _, err := rand.Read(id[:]); err != nil {
		writeError(w, 500, "Could not create invitation.")
		return
	}
	token := hex.EncodeToString(secret[:])
	now := time.Now().UTC()
	inv := db.UserInvitation{ID: hex.EncodeToString(id[:]), Username: req.Username, Role: req.Role, CreatedBy: user.ID, CreatedAt: now, ExpiresAt: now.Add(7 * 24 * time.Hour)}
	if err := s.store.CreateUserInvitation(inv, invitationDigest(token)); err != nil {
		if errors.Is(err, db.ErrUsernameExists) {
			writeError(w, 409, "This username already has an account or a pending invitation. Choose another username, or revoke the existing invitation.")
			return
		}
		if errors.Is(err, db.ErrReservedUsername) {
			writeError(w, 409, "This username is reserved. Choose another username.")
			return
		}
		writeError(w, 500, "Could not create invitation. Try again.")
		return
	}
	s.logAuditEvent(r, db.AuditEventParams{UserID: &user.ID, Action: "invite_user", ResourceType: "user", ResourceID: inv.Username, Detail: db.AuditDetail(map[string]any{"role": inv.Role, "invitation_id": inv.ID}), IPAddress: s.ClientIP(r)})
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 201, map[string]any{"invitation": inv, "token": token})
}

func (s *Server) handleListUserInvitations(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireInvitationAdmin(w, r); !ok {
		return
	}
	invitations, err := s.store.ListUserInvitations()
	if err != nil {
		writeError(w, 500, "Could not load invitations. Try again.")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"items": invitations})
}

func (s *Server) handleRevokeUserInvitation(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireInvitationAdmin(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	removed, err := s.store.RevokeUserInvitation(id)
	if err != nil {
		writeError(w, 500, "Could not revoke invitation. Try again.")
		return
	}
	if !removed {
		writeError(w, 404, "This invitation is no longer pending. Refresh the list.")
		return
	}
	s.logAuditEvent(r, db.AuditEventParams{UserID: &user.ID, Action: "revoke_user_invitation", ResourceType: "user_invitation", ResourceID: id, Detail: db.AuditDetail(map[string]any{"invitation_id": id, "link_invalidated": true}), IPAddress: s.ClientIP(r)})
	w.WriteHeader(http.StatusNoContent)
}

// The secret travels in the request body, never the path/query or audit log.
// The landing page keeps it in the URL fragment, which HTTP does not transmit.
func (s *Server) handlePublicUserInvitation(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.sameOriginPost(r) {
		writeError(w, 403, "Open this invitation on the ShinyHub server that issued it.")
		return
	}
	if !s.cfg.Auth.LocalLoginEnabled() {
		writeError(w, 409, "Password sign-in is disabled. Ask your administrator for the SSO sign-in link.")
		return
	}
	var req struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req) != nil {
		writeError(w, 400, "The invitation could not be read.")
		return
	}
	if len(req.Token) != 64 {
		writeError(w, 404, "This invitation is invalid, expired, or already used. Ask your administrator for a new link.")
		return
	}
	digest := invitationDigest(req.Token)
	inv, err := s.store.GetUserInvitation(digest)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, 404, "This invitation is invalid, expired, or already used. Ask your administrator for a new link.")
		} else {
			writeError(w, 500, "Could not check the invitation. Try again.")
		}
		return
	}
	if strings.HasSuffix(r.URL.Path, "/preview") {
		writeJSON(w, 200, map[string]any{"username": inv.Username, "role": inv.Role, "expires_at": inv.ExpiresAt})
		return
	}
	if err := auth.ValidateNewPassword(req.Password); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeError(w, 500, "Could not create your account. Try again.")
		return
	}
	created, err := s.store.AcceptUserInvitation(digest, hash)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, 409, "This invitation is no longer available. Ask your administrator for a new link.")
			return
		}
		if errors.Is(err, db.ErrUsernameExists) {
			writeError(w, 409, "This username is already in use. Ask your administrator for a new invitation.")
			return
		}
		writeError(w, 500, "Could not create your account. Try again.")
		return
	}
	s.logAuditEvent(r, db.AuditEventParams{UserID: &created.ID, Action: "accept_user_invitation", ResourceType: "user", ResourceID: created.Username, Detail: db.AuditDetail(map[string]any{"role": created.Role, "invited_by": inv.CreatedBy, "invitation_id": inv.ID}), IPAddress: s.ClientIP(r)})
	writeJSON(w, 201, map[string]any{"username": created.Username})
}

func (s *Server) handleReplaceUserInvitation(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireInvitationAdmin(w, r)
	if !ok {
		return
	}
	if !s.cfg.Auth.LocalLoginEnabled() {
		writeError(w, http.StatusConflict, "Password sign-in is disabled. Share the SSO sign-in link instead.")
		return
	}
	var secret [32]byte
	var id [16]byte
	if _, err := rand.Read(secret[:]); err != nil {
		writeError(w, 500, "Could not replace the invitation. Try again.")
		return
	}
	if _, err := rand.Read(id[:]); err != nil {
		writeError(w, 500, "Could not replace the invitation. Try again.")
		return
	}
	token := hex.EncodeToString(secret[:])
	now := time.Now().UTC()
	oldID := chi.URLParam(r, "id")
	inv, err := s.store.ReplaceUserInvitation(oldID, db.UserInvitation{ID: hex.EncodeToString(id[:]), CreatedBy: user.ID, CreatedAt: now, ExpiresAt: now.Add(7 * 24 * time.Hour)}, invitationDigest(token))
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, 409, "This invitation has changed or the account already exists. Refresh to see its current status.")
		} else {
			writeError(w, 500, "Could not replace the invitation. Try again.")
		}
		return
	}
	s.logAuditEvent(r, db.AuditEventParams{UserID: &user.ID, Action: "replace_user_invitation", ResourceType: "user", ResourceID: inv.Username, Detail: db.AuditDetail(map[string]any{"old_invitation_id": oldID, "invitation_id": inv.ID, "role": inv.Role}), IPAddress: s.ClientIP(r)})
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, map[string]any{"invitation": inv, "token": token})
}
