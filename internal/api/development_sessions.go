package api

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
)

const (
	minEphemeralTTL       = 15 * time.Minute
	maxEphemeralTTL       = 7 * 24 * time.Hour
	ephemeralTTLClockSkew = time.Minute
)

var developmentSessionIDRE = regexp.MustCompile(`^[a-f0-9]{32}$`)

type developmentRequest struct {
	ID     string
	Target string
}

func developmentRequestFromHeaders(r *http.Request) (developmentRequest, error) {
	channel := strings.ToLower(strings.TrimSpace(r.Header.Get(deploymentChannelHeader)))
	id := strings.TrimSpace(r.Header.Get(developmentSessionHeader))
	target := strings.ToLower(strings.TrimSpace(r.Header.Get(developmentTargetHeader)))
	if channel != deploymentChannelWatch {
		if id != "" || target != "" {
			return developmentRequest{}, errors.New("development session headers require deploy channel watch")
		}
		return developmentRequest{}, nil
	}
	if !developmentSessionIDRE.MatchString(id) {
		return developmentRequest{}, errors.New("watch deployments require a 32-character development session ID")
	}
	if !db.ValidDevelopmentTarget(target) {
		return developmentRequest{}, errors.New("development target must be existing, created, or ephemeral")
	}
	return developmentRequest{ID: id, Target: target}, nil
}

func developmentSessionParams(r *http.Request, appID int64, dev developmentRequest, expiresAt *time.Time) db.UpsertDevelopmentSessionParams {
	origin := deploymentOriginForRequest(r, "", false)
	return db.UpsertDevelopmentSessionParams{
		ID: dev.ID, AppID: appID, TargetKind: dev.Target,
		UserID: origin.UserID, Actor: origin.Actor,
		CredentialID: origin.CredentialID, CredentialType: origin.CredentialType,
		CredentialName: origin.CredentialName, ExpiresAt: expiresAt,
	}
}

func (s *Server) registerDevelopmentSession(r *http.Request, appID int64, expiresAt *time.Time) (developmentRequest, error) {
	dev, err := developmentRequestFromHeaders(r)
	if err != nil || dev.ID == "" {
		return dev, err
	}
	if err := s.store.UpsertDevelopmentSession(developmentSessionParams(r, appID, dev, expiresAt)); err != nil {
		return developmentRequest{}, err
	}
	return dev, nil
}

func (s *Server) handleEndDevelopmentSession(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	app, ok := s.requireManageApp(w, r, slug)
	if !ok {
		return
	}
	id := chi.URLParam(r, "sessionID")
	if !developmentSessionIDRE.MatchString(id) {
		writeError(w, http.StatusBadRequest, "invalid development session ID")
		return
	}
	if err := s.store.EndDevelopmentSession(app.ID, id, time.Now().UTC()); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "development session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if u := auth.UserFromContext(r.Context()); u != nil {
		s.logAuditEvent(r, db.AuditEventParams{
			UserID: &u.ID, Action: "end_development_session", ResourceType: "app",
			ResourceID: slug, Detail: fmt.Sprintf(`{"development_session_id":%q}`, id),
			IPAddress: s.ClientIP(r),
		})
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleHeartbeatDevelopmentSession(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	app, ok := s.requireManageApp(w, r, slug)
	if !ok {
		return
	}
	id := chi.URLParam(r, "sessionID")
	if !developmentSessionIDRE.MatchString(id) {
		writeError(w, http.StatusBadRequest, "invalid development session ID")
		return
	}
	dev, err := developmentRequestFromHeaders(r)
	if err != nil || dev.ID != id {
		writeError(w, http.StatusBadRequest, "development heartbeat requires matching watch session headers")
		return
	}
	if err := s.store.HeartbeatDevelopmentSession(developmentSessionParams(r, app.ID, dev, nil)); err != nil {
		if errors.Is(err, db.ErrDevelopmentSessionConflict) {
			writeError(w, http.StatusConflict, "development session has ended; start a new session")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) developmentDeploymentFilter(r *http.Request, appID int64) (map[int64]bool, bool, error) {
	id := strings.TrimSpace(r.URL.Query().Get("development_session_id"))
	if id == "" {
		return nil, false, nil
	}
	if !developmentSessionIDRE.MatchString(id) {
		return nil, true, errors.New("invalid development session ID")
	}
	if _, err := s.store.GetDevelopmentSession(appID, id); err != nil {
		return nil, true, err
	}
	ids, err := s.store.DevelopmentSessionDeploymentIDs(appID, id)
	return ids, true, err
}
