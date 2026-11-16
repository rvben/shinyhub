package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/oauth"
	"github.com/rvben/shinyhub/internal/secrets"
	"golang.org/x/oauth2"
)

func (s *Server) handleLinkOIDCUser(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	if s.oidcProvider == nil {
		writeError(w, http.StatusBadRequest, "configure OIDC before linking identities")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}
	var body struct {
		Sub      string `json:"sub"`
		Issuer   string `json:"issuer"`
		ClientID string `json:"client_id"`
		DryRun   bool   `json:"dry_run"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid link request")
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		writeError(w, http.StatusBadRequest, "link request must contain one JSON object")
		return
	}
	if body.Sub == "" || body.Issuer != s.oidcProvider.IssuerURL || body.ClientID != s.oidcProvider.ClientID {
		writeError(w, http.StatusBadRequest, "sub and matching issuer/client_id are required")
		return
	}
	target, err := s.store.GetUserByID(id)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "user not found")
		} else {
			writeInternal(w, r, "could not read user", err)
		}
		return
	}
	if body.DryRun {
		if err := s.store.CheckOIDCProvider(body.Issuer, body.ClientID); err != nil {
			writeError(w, http.StatusConflict, "OIDC authority migration required")
			return
		}
	} else {
		if err := s.store.BindOIDCProvider(body.Issuer, body.ClientID); err != nil {
			writeError(w, http.StatusConflict, "OIDC authority migration required")
			return
		}
	}
	user, linked, err := s.store.LinkOIDCUser(target.Username, body.Sub, body.DryRun, target.ID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "user not found")
		} else if errors.Is(err, db.ErrOIDCLinkConflict) {
			writeError(w, http.StatusConflict, err.Error())
		} else {
			writeInternal(w, r, "could not link OIDC identity", err)
		}
		return
	}
	if linked {
		s.logAuditEvent(r, db.AuditEventParams{UserID: callerID(r), Action: "link_oidc", ResourceType: "user", ResourceID: user.Username, Detail: db.AuditDetail(map[string]any{"issuer": body.Issuer, "client_id": body.ClientID, "method": "admin", "sub": body.Sub}), IPAddress: s.ClientIP(r)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"user_id": user.ID, "username": user.Username, "linked": linked, "dry_run": body.DryRun})
}

func (s *Server) saveOIDCSession(userID int64, u *oauth.OIDCUser, tok *oauth2.Token, authTime time.Time) (string, error) {
	raw, ok := tok.Extra("id_token").(string)
	if !ok {
		return "", fmt.Errorf("missing ID token")
	}
	if u.SID == "" {
		s.oidcMissingSIDWarning.Do(func() { slog.Warn("oidc_frontchannel_session_id_missing") })
	}
	// Confirm discovery supports logout before persisting the encrypted hint.
	if _, err := s.oidcProvider.LogoutURL(raw, s.oidcLogoutReturnURL()); err != nil {
		return "", err
	}
	encrypted, err := secrets.Encrypt(secrets.DeriveKeyWithInfo(s.cfg.Auth.Secret, "shinyhub-oidc-logout-v1"), []byte(raw))
	if err != nil {
		return "", err
	}
	id, err := newBrowserLogoutCode()
	if err != nil {
		return "", err
	}
	err = s.store.SaveOIDCSession(db.OIDCSession{JTI: id, UserID: userID, Issuer: s.oidcProvider.IssuerURL, SID: u.SID, IDToken: base64.RawURLEncoding.EncodeToString(encrypted), ExpiresAt: authTime.Add(s.cfg.Auth.BrowserSessionMaxAge()).Truncate(time.Second)})
	return id, err
}
func (s *Server) oidcLogoutReturnURL() string {
	u, err := url.Parse(s.cfg.OAuth.OIDC.CallbackURL)
	if err != nil || u.Host == "" {
		return ""
	}
	u.Path = "/"
	u.RawQuery = "signed_out=1"
	u.Fragment = ""
	return u.String()
}

func (s *Server) oidcLogoutURL(r *http.Request) (string, error) {
	if !s.cfg.OAuth.OIDC.EndSession || s.oidcProvider == nil || auth.ForwardAuthFromContext(r.Context()) {
		return "", nil
	}
	c, err := r.Cookie(auth.SessionCookieName)
	if err != nil {
		return "", nil
	}
	claims, err := auth.ValidateJWT(c.Value, s.cfg.Auth.Secret, nil)
	if err != nil {
		return "", nil
	}
	v, err := s.store.GetOIDCSession(claims.ID)
	if errors.Is(err, db.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	encrypted, err := base64.RawURLEncoding.DecodeString(v.IDToken)
	if err != nil {
		return "", err
	}
	raw, err := secrets.Decrypt(secrets.DeriveKeyWithInfo(s.cfg.Auth.Secret, "shinyhub-oidc-logout-v1"), encrypted)
	if err != nil {
		return "", err
	}
	return s.oidcProvider.LogoutURL(string(raw), s.oidcLogoutReturnURL())
}

// Front-channel notifications are correlated by issuer and the IdP's opaque
// session ID. Never clear an unrelated cookie merely because this URL is hit.
func (s *Server) handleOIDCFrontChannelLogout(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.cfg.OAuth.OIDC.EndSession || s.oidcProvider == nil {
		writeError(w, http.StatusNotFound, "OIDC logout is disabled")
		return
	}
	issuer, sid := r.URL.Query().Get("iss"), r.URL.Query().Get("sid")
	if issuer != s.oidcProvider.IssuerURL || sid == "" || len(sid) > 1024 {
		writeError(w, http.StatusBadRequest, "issuer and session id are required")
		return
	}
	if err := s.store.RevokeOIDCSessions(issuer, sid, time.Now().Add(s.cfg.Auth.BrowserSessionMaxAge())); err != nil {
		writeInternal(w, r, "could not revoke OIDC sessions", err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// Cleanup is best-effort: revocation is already durable before this is called.
func (s *Server) clearOIDCLogoutHint(r *http.Request) {
	c, err := r.Cookie(auth.SessionCookieName)
	if err != nil {
		return
	}
	claims, err := auth.ValidateJWT(c.Value, s.cfg.Auth.Secret, nil)
	if err != nil {
		return
	}
	if err := s.store.DeleteOIDCSession(claims.ID); err != nil {
		reqLog(r).Warn("oidc_logout_hint_cleanup_failed", "err", err)
	}
}

// Call only after ID-token verification and an authoritative policy refusal.
// Provider errors and malformed tokens are not evidence of authorization loss.
func (s *Server) rejectOIDCAuthorization(w http.ResponseWriter, r *http.Request, reason, message, sub string) {
	if err := s.store.RevokeOIDCUserSessions(s.oidcProvider.IssuerURL, s.oidcProvider.ClientID, sub); err != nil {
		writeInternal(w, r, "could not revoke refused OIDC sessions", err)
		return
	}
	s.auditOIDCRefusal(r, reason, sub)
	writeError(w, http.StatusForbidden, message)
}

// Refusals after token verification are actionable migration audit events.
func (s *Server) auditOIDCRefusal(r *http.Request, reason, sub string) {
	event := db.AuditEventParams{Action: "login_refused", ResourceType: "user", IPAddress: s.ClientIP(r), Detail: db.AuditDetail(map[string]any{"reason": reason, "provider": providerOIDC, "sub": sub})}
	if user, err := s.store.GetUserByOAuthAccount(providerOIDC, sub); err == nil {
		event.UserID = &user.ID
		event.ResourceID = user.Username
	}
	s.logAuditEvent(r, event)
}
