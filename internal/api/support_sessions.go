package api

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
)

const supportSessionRecentAuthWindow = 10 * time.Minute

type createSupportSessionRequest struct {
	UserID  int64  `json:"user_id"`
	AppSlug string `json:"app_slug"`
	Reason  string `json:"reason"`
}

type supportSessionResponse struct {
	ID              string    `json:"id"`
	LaunchURL       string    `json:"launch_url"`
	ExpiresAt       time.Time `json:"expires_at"`
	SubjectUserID   int64     `json:"subject_user_id"`
	SubjectUsername string    `json:"subject_username"`
	AppSlug         string    `json:"app_slug"`
}

type currentSupportSession struct {
	SubjectUsername  string    `json:"subject_username"`
	AppSlug          string    `json:"app_slug"`
	AppURL           string    `json:"app_url,omitempty"`
	ExpiresAt        time.Time `json:"expires_at"`
	RemainingSeconds int64     `json:"remaining_seconds"`
	Resumable        bool      `json:"resumable"`
}

func (s *Server) requireSupportSessionAdmin(w http.ResponseWriter, r *http.Request) (*auth.ContextUser, bool) {
	admin, ok := requireAdmin(w, r)
	if !ok {
		return nil, false
	}
	if !s.cfg.Auth.SupportSessions || s.cfg.Server.AppOrigin == "" {
		writeError(w, http.StatusNotFound, "not found")
		return nil, false
	}
	if admin.IsServiceAccount() {
		writeError(w, http.StatusForbidden, "support sessions require a human administrator")
		return nil, false
	}
	return admin, true
}

func (s *Server) supportSessionAppURL(slug string) string {
	appURL, _ := url.Parse(s.cfg.Server.AppOrigin)
	appURL.Path = "/" + strings.TrimPrefix(path.Join(appURL.Path, "app", slug), "/") + "/"
	appURL.RawQuery = ""
	appURL.Fragment = ""
	return appURL.String()
}

func randomSupportCapability() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (s *Server) handleCreateSupportSession(w http.ResponseWriter, r *http.Request) {
	admin, ok := s.requireSupportSessionAdmin(w, r)
	if !ok {
		return
	}

	// A support session is a privileged interactive action. Native sessions
	// must have authenticated recently; forward-auth deployments continuously
	// delegate freshness to their upstream identity gateway. API keys never
	// qualify.
	token := auth.TokenInfoFromContext(r.Context())
	credential := auth.CredentialInfoFromContext(r.Context())
	if (token == nil && (credential != nil || !s.cfg.Auth.ForwardAuth.Enabled)) ||
		(token != nil && (token.AuthTime.IsZero() || time.Since(token.AuthTime) > supportSessionRecentAuthWindow)) {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": "Recent authentication is required. Sign in again, then retry.",
			"code":  "recent_authentication_required",
		})
		return
	}

	var req createSupportSessionRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request")
		return
	}
	req.AppSlug = strings.TrimSpace(req.AppSlug)
	req.Reason = strings.TrimSpace(req.Reason)
	if req.UserID <= 0 || req.AppSlug == "" || len(req.Reason) < 8 || len(req.Reason) > 500 {
		writeError(w, http.StatusBadRequest, "user_id, app_slug, and a reason between 8 and 500 characters are required")
		return
	}
	if req.UserID == admin.ID {
		writeError(w, http.StatusForbidden, "cannot start a support session as yourself")
		return
	}

	subject, err := s.store.GetUserByID(req.UserID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "user not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if subject.PrincipalType == "service_account" || (subject.Role != "viewer" && subject.Role != "developer") {
		writeError(w, http.StatusForbidden, "support sessions can target only human viewers or developers")
		return
	}
	app, err := s.store.GetAppBySlug(req.AppSlug)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "app not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	canAccess := app.Access == "public" || app.Access == "shared"
	if !canAccess {
		canAccess, err = s.store.UserCanAccessApp(req.AppSlug, subject.ID)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if !canAccess {
		writeError(w, http.StatusForbidden, "the user does not have access to this app")
		return
	}

	sessionID, err := randomSupportCapability()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	launchCode, err := randomSupportCapability()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	launchHash := sha256.Sum256([]byte(launchCode))
	now := time.Now().UTC()
	expiresAt := now.Add(db.SupportSessionDuration)
	detail, _ := json.Marshal(map[string]any{
		"actor_username": admin.Username, "subject_user_id": subject.ID,
		"subject_username": subject.Username, "app_slug": req.AppSlug,
		"reason": req.Reason, "expires_at": expiresAt,
		"duration_seconds": int(db.SupportSessionDuration.Seconds()),
	})
	if err := s.store.CreateSupportSession(db.CreateSupportSessionParams{
		ID:                sessionID,
		ActorUserID:       admin.ID,
		ActorUsername:     admin.Username,
		ActorTokenEpoch:   admin.TokenEpoch,
		SubjectUserID:     subject.ID,
		SubjectUsername:   subject.Username,
		SubjectRole:       subject.Role,
		SubjectTokenEpoch: subject.TokenEpoch,
		AppID:             app.ID,
		AppSlug:           req.AppSlug,
		Reason:            req.Reason,
		LaunchCodeHash:    hex.EncodeToString(launchHash[:]),
		ExpiresAt:         expiresAt,
		AuditDetail:       string(detail),
		IPAddress:         s.ClientIP(r),
	}); err != nil {
		if errors.Is(err, db.ErrSupportSessionActive) {
			writeError(w, http.StatusConflict, "End the administrator's active support session before starting another one")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	launch, _ := url.Parse(s.supportSessionAppURL(req.AppSlug))
	q := launch.Query()
	q.Set("__shinyhub_launch", launchCode)
	launch.RawQuery = q.Encode()
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, supportSessionResponse{
		ID:              sessionID,
		LaunchURL:       launch.String(),
		ExpiresAt:       expiresAt,
		SubjectUserID:   subject.ID,
		SubjectUsername: subject.Username,
		AppSlug:         req.AppSlug,
	})
}

func (s *Server) handleGetCurrentSupportSession(w http.ResponseWriter, r *http.Request) {
	admin, ok := s.requireSupportSessionAdmin(w, r)
	if !ok {
		return
	}
	session, err := s.store.GetActiveSupportSessionForActor(admin.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if session == nil {
		writeJSON(w, http.StatusOK, map[string]any{"active": nil})
		return
	}
	resumable := session.TokenJTI != ""
	remaining := int64((time.Until(session.ExpiresAt) + time.Second - 1) / time.Second)
	if remaining < 0 {
		remaining = 0
	}
	active := currentSupportSession{
		SubjectUsername:  session.SubjectUsername,
		AppSlug:          session.AppSlug,
		ExpiresAt:        session.ExpiresAt,
		RemainingSeconds: remaining,
		Resumable:        resumable,
	}
	if resumable {
		active.AppURL = s.supportSessionAppURL(session.AppSlug)
	}
	writeJSON(w, http.StatusOK, map[string]any{"active": active})
}

func (s *Server) handleDeleteCurrentSupportSession(w http.ResponseWriter, r *http.Request) {
	admin, ok := s.requireSupportSessionAdmin(w, r)
	if !ok {
		return
	}
	_, err := s.store.StopActiveSupportSessionForActor(admin.ID, "ended_from_dashboard", s.ClientIP(r))
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

type supportSessionApp struct {
	ID   int64  `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

func (s *Server) handleListSupportSessionApps(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	if !s.cfg.Auth.SupportSessions {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	userID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}
	subject, err := s.store.GetUserByID(userID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "eligible user not found")
		} else {
			writeError(w, http.StatusInternalServerError, "internal server error")
		}
		return
	}
	if subject == nil || subject.PrincipalType == "service_account" ||
		(subject.Role != string(auth.RoleViewer) && subject.Role != string(auth.RoleDeveloper)) {
		writeError(w, http.StatusNotFound, "eligible user not found")
		return
	}
	apps, err := s.store.ListAppsVisibleToUser(subject.ID, 0, 0)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	items := make([]supportSessionApp, 0, len(apps))
	for _, app := range apps {
		items = append(items, supportSessionApp{ID: app.ID, Slug: app.Slug, Name: app.Name})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
