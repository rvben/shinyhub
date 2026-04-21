package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/secrets"
)

type envListItem struct {
	Key       string `json:"key"`
	Value     string `json:"value,omitempty"`
	Secret    bool   `json:"secret"`
	Set       bool   `json:"set"`
	UpdatedAt int64  `json:"updated_at"`
}

func (s *Server) handleListAppEnv(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	app, _, ok := s.requireViewApp(w, r, slug)
	if !ok {
		return
	}

	vars, err := s.store.ListAppEnvVars(app.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list env")
		return
	}
	out := make([]envListItem, 0, len(vars))
	for _, v := range vars {
		item := envListItem{
			Key:       v.Key,
			Secret:    v.IsSecret,
			Set:       true,
			UpdatedAt: v.UpdatedAt.Unix(),
		}
		if !v.IsSecret {
			item.Value = string(v.Value)
		}
		out = append(out, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"env": out})
}

// envKeyRegex enforces POSIX-style env var naming: uppercase letters, digits,
// and underscores only, with a non-digit first character.
var envKeyRegex = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

const (
	// maxEnvValueBytes is the maximum size of a single env var value (64 KiB).
	maxEnvValueBytes = 64 * 1024
	// maxEnvKeysPerApp caps the number of distinct env vars per app.
	maxEnvKeysPerApp = 100
)

type upsertEnvRequest struct {
	Value  string `json:"value"`
	Secret bool   `json:"secret"`
}

// handleUpsertAppEnv creates or updates a single per-app environment variable.
// Keys must match [A-Z_][A-Z0-9_]* and may not start with "SHINYHUB_".
// Secret values are encrypted at rest using the server's secrets key.
// Pass ?restart=true to restart the app after the update (no-op when the app
// is stopped or no manager is present).
func (s *Server) handleUpsertAppEnv(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	key := chi.URLParam(r, "key")

	// Key validation before the DB lookup so invalid requests fail fast.
	if !envKeyRegex.MatchString(key) {
		writeError(w, http.StatusUnprocessableEntity, "invalid key: must match [A-Z_][A-Z0-9_]*")
		return
	}
	if strings.HasPrefix(key, "SHINYHUB_") {
		writeError(w, http.StatusUnprocessableEntity, "keys with prefix SHINYHUB_ are reserved")
		return
	}

	app, ok := s.requireManageApp(w, r, slug)
	if !ok {
		return
	}

	var body upsertEnvRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if len(body.Value) > maxEnvValueBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "value exceeds 64 KB")
		return
	}

	// Count cap: only checked for new keys, not updates.
	existing, _ := s.store.GetAppEnvVar(app.ID, key)
	if existing == nil {
		n, err := s.store.CountAppEnvVars(app.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "count env vars")
			return
		}
		if n >= maxEnvKeysPerApp {
			writeError(w, http.StatusUnprocessableEntity, "per-app env var limit reached (100)")
			return
		}
	}

	storedValue := []byte(body.Value)
	if body.Secret {
		ct, err := secrets.Encrypt(s.secretsKey, []byte(body.Value))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "encrypt")
			return
		}
		storedValue = ct
	}

	if err := s.store.UpsertAppEnvVar(app.ID, key, storedValue, body.Secret); err != nil {
		writeError(w, http.StatusInternalServerError, "store")
		return
	}

	action := "created"
	if existing != nil {
		action = "updated"
	}
	detail, _ := json.Marshal(map[string]any{
		"key":    key,
		"secret": body.Secret,
		"action": action,
	})

	u := auth.UserFromContext(r.Context())
	var userID *int64
	if u != nil {
		userID = &u.ID
	}
	s.store.LogAuditEvent(db.AuditEventParams{
		UserID:       userID,
		Action:       "env.set",
		ResourceType: "app",
		ResourceID:   slug,
		Detail:       string(detail),
		IPAddress:    s.clientIP(r),
	})

	restarted, restartErr := s.maybeRestartForChange(r, app, slug)
	resp := map[string]any{
		"key":       key,
		"secret":    body.Secret,
		"set":       true,
		"restarted": restarted,
	}
	if restartErr != nil {
		resp["restart_error"] = restartErr.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleDeleteAppEnv removes a single per-app environment variable.
// Pass ?restart=true to restart the app after the deletion (no-op when the app
// is stopped or no manager is present).
func (s *Server) handleDeleteAppEnv(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	key := chi.URLParam(r, "key")

	app, ok := s.requireManageApp(w, r, slug)
	if !ok {
		return
	}

	if err := s.store.DeleteAppEnvVar(app.ID, key); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "env var not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "delete env var")
		return
	}

	detail, _ := json.Marshal(map[string]any{"key": key})
	u := auth.UserFromContext(r.Context())
	var userID *int64
	if u != nil {
		userID = &u.ID
	}
	s.store.LogAuditEvent(db.AuditEventParams{
		UserID:       userID,
		Action:       "env.delete",
		ResourceType: "app",
		ResourceID:   slug,
		Detail:       string(detail),
		IPAddress:    s.clientIP(r),
	})

	s.maybeRestartForChange(r, app, slug)

	w.WriteHeader(http.StatusNoContent)
}

// maybeRestartForChange optionally restarts an app to apply env-var changes.
// It only acts when ?restart=true is set, a manager is configured, and the app
// is currently running — stopped or hibernated apps are intentionally left
// alone so an env-var change doesn't unexpectedly wake them.
//
// Returns (true, nil) on a successful restart, (false, nil) when the restart
// was skipped, and (false, err) when the old process was stopped but the
// re-launch failed. In the failure case the app's DB status is reset to
// "stopped" so it reflects reality.
func (s *Server) maybeRestartForChange(r *http.Request, app *db.App, slug string) (bool, error) {
	if r.URL.Query().Get("restart") != "true" {
		return false, nil
	}
	if s.manager == nil {
		return false, nil
	}
	if app.Status != "running" {
		return false, nil
	}
	deployments, err := s.store.ListDeployments(app.ID)
	if err != nil || len(deployments) == 0 {
		return false, nil
	}
	current := deployments[0]
	_ = s.manager.Stop(slug)
	if s.proxy != nil {
		s.proxy.Deregister(slug)
	}
	result, runErr := s.deployRun(deploy.Params{
		Slug:            slug,
		BundleDir:       current.BundleDir,
		Replicas:        app.Replicas,
		Manager:         s.manager,
		Proxy:           s.proxy,
		MemoryLimitMB:   deploy.ResolveMemoryLimitMB(app.MemoryLimitMB, s.cfg.Runtime.Docker.DefaultMemoryMB),
		CPUQuotaPercent: deploy.ResolveCPUQuotaPercent(app.CPUQuotaPercent, s.cfg.Runtime.Docker.DefaultCPUPercent),
	})
	if runErr != nil {
		// The old process is gone; reflect that in the DB so callers don't
		// see a stale "running" status with a dead PID.
		_ = s.store.UpdateAppStatus(db.UpdateAppStatusParams{
			Slug:   slug,
			Status: "stopped",
		})
		return false, runErr
	}
	for _, r := range result.Replicas {
		pid, port := r.PID, r.Port
		_ = s.store.UpsertReplica(db.UpsertReplicaParams{
			AppID:  app.ID,
			Index:  r.Index,
			PID:    &pid,
			Port:   &port,
			Status: "running",
		})
	}
	_ = s.store.UpdateAppStatus(db.UpdateAppStatusParams{
		Slug:   slug,
		Status: "running",
	})
	return true, nil
}
