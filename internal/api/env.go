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
	// Manager-only: env vars are app configuration. Even non-secret values can
	// hold connection strings, internal hostnames, or third-party keys, and key
	// names alone reveal an app's integrations. This matches the env
	// upsert/delete guards and the manager-only Configuration UI, and stops
	// any authenticated viewer of a public/shared app from reading config.
	app, ok := s.requireManageApp(w, r, slug)
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
	limit, offset := parsePagination(r)
	writeList(w, out, limit, offset, nil)
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

	// Detect an identical set: when the key already exists with the same
	// value and secret flag, return changed:false so the CLI can skip
	// --restart side effects when nothing actually changed.
	changed := true
	if existing != nil && existing.IsSecret == body.Secret {
		if body.Secret {
			// For secret values the stored bytes are encrypted; decrypt to compare.
			if plain, err := secrets.Decrypt(s.secretsKey, existing.Value); err == nil {
				changed = string(plain) != body.Value
			}
		} else {
			changed = string(existing.Value) != body.Value
		}
	}

	if changed {
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
			IPAddress:    s.ClientIP(r),
		})
	}

	restarted := false
	var restartErr error
	if changed {
		restarted, restartErr = s.maybeRestartForChange(r, app, slug)
	}
	resp := map[string]any{
		"key":              key,
		"secret":           body.Secret,
		"set":              true,
		"changed":          changed,
		"restarted":        restarted,
		"restart_required": changed && !restarted && app.Status == "running",
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
		IPAddress:    s.ClientIP(r),
	})

	restarted, _ := s.maybeRestartForChange(r, app, slug)
	// The 204 carries no body, so advertise a needed restart via a header: a
	// running app keeps the removed variable in its environment until cycled.
	if !restarted && app.Status == "running" {
		w.Header().Set("X-Shinyhub-Restart-Required", "true")
	}

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
	// Serialize with any deploy/restart on the same slug. The active
	// deployment is read inside the lock so a concurrent deploy can't make
	// us relaunch a stale bundle after it promoted a newer one.
	release := s.acquireDeployLock(slug)
	defer release()

	deployments, err := s.store.ListDeployments(app.ID)
	if err != nil || len(deployments) == 0 {
		return false, nil
	}
	current := deployments[0]

	// Reject an infeasible shared-mount colocation before disrupting the running
	// pool, matching the user-facing deploy/restart/rollback paths. Otherwise the
	// stop/deregister below would tear the app down and the redeploy would land it
	// without a colocation pin, away from its source data.
	if err := s.checkColocatedShared(app.ID, s.tiersForApp(app)); err != nil {
		return false, err
	}

	_ = s.manager.Stop(slug)
	if s.proxy != nil {
		s.proxy.Deregister(slug)
	}
	envDefaultMem, envDefaultCPU := s.cfg.Runtime.DefaultResourcesForApp(app)
	result, runErr := s.deployRun(s.withTierPlacement(deploy.Params{
		Slug:                  slug,
		BundleDir:             current.BundleDir,
		Replicas:              app.Replicas,
		Manager:               s.manager,
		Proxy:                 s.proxy,
		MemoryLimitMB:         deploy.ResolveMemoryLimitMB(app.MemoryLimitMB, envDefaultMem),
		CPUQuotaPercent:       deploy.ResolveCPUQuotaPercent(app.CPUQuotaPercent, envDefaultCPU),
		MaxSessionsPerReplica: deploy.ResolveMaxSessionsPerReplica(app.MaxSessionsPerReplica, s.cfg.Runtime.DefaultMaxSessionsPerReplica),
		IdentityHeaders:       deploy.ResolveIdentityHeaders(app.IdentityHeaders, s.cfg.Auth.IdentityHeadersEnabled()),
		ContentDigest:         current.ContentDigest,
		DeploymentID:          current.ID,
		AppVersion:            current.Version,
	}, app))
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
		depID := current.ID
		_ = s.store.UpsertReplica(db.UpsertReplicaParams{
			AppID:        app.ID,
			Index:        r.Index,
			PID:          &pid,
			Port:         &port,
			Status:       "running",
			Provider:     r.Provider,
			Tier:         r.Tier,
			EndpointURL:  r.EndpointURL,
			WorkerID:     r.WorkerID,
			AppVersion:   current.Version,
			DesiredState: "running",
			DeploymentID: &depID,
		})
	}
	for _, idx := range result.Failed {
		_ = s.store.UpsertReplica(db.UpsertReplicaParams{
			AppID:  app.ID,
			Index:  idx,
			Status: "crashed",
		})
	}
	_ = s.store.UpdateAppStatus(db.UpdateAppStatusParams{
		Slug:   slug,
		Status: "running",
	})
	return true, nil
}
