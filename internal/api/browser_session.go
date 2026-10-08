package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/rvben/shinyhub/internal/auth"
)

// HandleAppSession is the deliberately small renewal endpoint on the app
// origin. Production wraps it with app access middleware, which resolves only
// the browser cookie or upstream identity and checks current app permissions.
// It never exposes dashboard capabilities or accepts support-session renewal.
func (s *Server) HandleAppSession(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	u := auth.UserFromContext(r.Context())
	if u == nil {
		writeError(w, http.StatusUnauthorized, "sign in again")
		return
	}
	if u.SupportSession != nil {
		writeError(w, http.StatusForbidden, "support sessions cannot be renewed")
		return
	}
	var session *browserSessionResponse
	if ti := auth.TokenInfoFromContext(r.Context()); ti != nil && !auth.ForwardAuthFromContext(r.Context()) {
		var err error
		session, err = s.setBrowserSession(w, r, u, ti.AuthTime, ti.JTI)
		if errors.Is(err, auth.ErrSessionExpired) {
			auth.ClearSessionCookie(w, r, s.cfg.TrustedProxyNets)
			writeError(w, http.StatusUnauthorized, "session expired; sign in again")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not renew session")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user": map[string]any{"id": u.ID}, "session": session,
	})
}

// The browser gets timing information, never the HttpOnly credential. Server
// time lets it schedule renewal independently of the workstation's clock.
type browserSessionResponse struct {
	ServerTime        time.Time `json:"server_time"`
	ExpiresAt         time.Time `json:"expires_at"`
	AbsoluteExpiresAt time.Time `json:"absolute_expires_at"`
	RefreshAfter      float64   `json:"refresh_after_seconds"`
}

func (s *Server) browserSessionRevocationExpiry(authTime, expiresAt time.Time) time.Time {
	if authTime.IsZero() {
		// A legacy token may have been renewed concurrently and started its
		// absolute clock at that renewal. Retain revocation conservatively.
		authTime = time.Now()
	}
	if deadline := authTime.Add(s.cfg.Auth.BrowserSessionMaxAge()); deadline.After(expiresAt) {
		return deadline
	}
	return expiresAt
}

func (s *Server) setBrowserSession(w http.ResponseWriter, r *http.Request, u *auth.ContextUser, authTime time.Time, sessionID ...string) (*browserSessionResponse, error) {
	token, info, err := auth.IssueBrowserSession(u, s.cfg.Auth.Secret, authTime, s.cfg.Auth.BrowserSessionTTL(), s.cfg.Auth.BrowserSessionMaxAge(), sessionID...)
	if err != nil {
		return nil, err
	}
	w.Header().Set("Cache-Control", "no-store")
	auth.SetSessionCookieUntil(w, r, token, info.ExpiresAt, s.cfg.TrustedProxyNets)
	now := time.Now()
	refreshAfter := min(5*time.Minute, s.cfg.Auth.BrowserSessionTTL()/3)
	// Continue checking identity at the normal cadence near the absolute cap,
	// shortening the next interval when the deadline arrives sooner.
	deadline := info.AuthTime.Add(s.cfg.Auth.BrowserSessionMaxAge()).Truncate(time.Second)
	if info.ExpiresAt.Equal(deadline) {
		refreshAfter = min(refreshAfter, max(time.Second, time.Until(deadline)))
	}
	return &browserSessionResponse{ServerTime: now, ExpiresAt: info.ExpiresAt,
		AbsoluteExpiresAt: deadline, RefreshAfter: refreshAfter.Seconds()}, nil
}
