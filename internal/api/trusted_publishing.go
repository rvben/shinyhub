package api

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/trustedpublish"
)

func (s *Server) trustedCredentialAllowed(key db.APIKeyInfo) bool {
	parts := strings.Split(key.ExternalID, ":")
	if len(parts) != 3 || key.CredentialType != "service" || key.CredentialRole != "developer" || key.Unrestricted {
		return false
	}
	for _, p := range s.cfg.Auth.TrustedPublishers {
		if p.Fingerprint() == parts[1] && slices.Equal(p.Apps, key.AppScope) {
			return true
		}
	}
	return false
}

// handleTrustedPublishing exchanges a signed CI assertion for an app-scoped
// service credential. The unique name serializes exchanges across HA instances.
func (s *Server) handleTrustedPublishing(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	if len(s.cfg.Auth.TrustedPublishers) == 0 {
		writeError(w, http.StatusNotFound, "trusted publishing is not configured")
		return
	}
	var request struct {
		Policy string `json:"policy"`
		Token  string `json:"identity_token"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 40<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid trusted publishing request")
		return
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		writeError(w, http.StatusBadRequest, "expected one JSON object")
		return
	}
	var policy *trustedpublish.Policy
	for i := range s.cfg.Auth.TrustedPublishers {
		if s.cfg.Auth.TrustedPublishers[i].Name == request.Policy {
			policy = &s.cfg.Auth.TrustedPublishers[i]
			break
		}
	}
	if policy == nil {
		writeError(w, http.StatusUnauthorized, "workload identity is not authorized")
		return
	}
	identity, err := s.trustedVerifier.Verify(r.Context(), *policy, request.Token)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "workload identity is not authorized")
		return
	}
	account, err := s.store.UpsertSystemUser(db.SystemUsernameDeploy, "developer")
	// Consume before minting, failing closed even when a later database write
	// fails. A fresh CI assertion is required after an uncertain exchange.
	if err == nil {
		var consumed bool
		consumed, err = s.store.ConsumeTrustedAssertion(identity.ReplayID)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "identity replay protection is unavailable")
			return
		}
		if !consumed {
			writeError(w, http.StatusConflict, "identity token was already exchanged; request a fresh CI identity token")
			return
		}
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not prepare deployment identity")
		return
	}
	raw, hash, err := generateAPIKey()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not issue deployment credential")
		return
	}
	expires := time.Now().UTC().Add(trustedpublish.CredentialLifetime)
	// Independent of policy: an issuer/jti may be exchanged only once, even
	// when multiple policies match. Expired rows retain the replay marker.
	name := "ci-" + identity.ReplayID
	id, _, err := s.store.CreateAPIKey(db.CreateAPIKeyParams{
		UserID: account.ID, KeyHash: hash, Name: name, ExpiresAt: &expires,
		CredentialType: "service", CredentialRole: "developer", AppScope: policy.Apps,
		ExternalID: "trusted:" + policy.Fingerprint() + ":" + identity.ReplayID,
	})
	if err != nil {
		writeError(w, http.StatusConflict, "credential exchange failed; request a fresh CI identity token before retrying")
		return
	}
	s.logAuditEvent(r, db.AuditEventParams{UserID: &account.ID, Action: "trusted_publish", ResourceType: "token", ResourceID: policy.Name, Detail: auditDetailJSON(map[string]any{"credential_id": id, "apps": policy.Apps, "expires_at": expires}), IPAddress: s.ClientIP(r)})
	writeJSON(w, http.StatusCreated, map[string]any{"token": raw, "token_type": "Token", "expires_at": expires, "expires_in": int(trustedpublish.CredentialLifetime.Seconds()), "apps": policy.Apps, "id": id})
}
