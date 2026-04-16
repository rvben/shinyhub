package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/process"
)

// slugRE enforces a safe, DNS-compatible slug format.
var slugRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

func (s *Server) handleListApps(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	limit, offset := parsePagination(r)

	var (
		apps []*db.App
		err  error
	)
	if isPrivilegedAppOperator(u) {
		apps, err = s.store.ListApps(limit, offset)
	} else {
		apps, err = s.store.ListAppsVisibleToUser(u.ID, limit, offset)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if apps == nil {
		apps = []*db.App{}
	}
	writeJSON(w, http.StatusOK, apps)
}

type createAppRequest struct {
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	ProjectSlug string `json:"project_slug"`
}

func (s *Server) handleCreateApp(w http.ResponseWriter, r *http.Request) {
	var req createAppRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request")
		return
	}
	if req.Slug == "" || req.Name == "" {
		writeError(w, http.StatusBadRequest, "slug and name are required")
		return
	}
	if !slugRE.MatchString(req.Slug) {
		writeError(w, http.StatusBadRequest, "slug must match ^[a-z0-9][a-z0-9-]{0,62}$")
		return
	}
	if len(req.Name) > 128 {
		writeError(w, http.StatusBadRequest, "name must be 128 characters or fewer")
		return
	}

	u := auth.UserFromContext(r.Context())
	if u == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if !canCreateApps(u) {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}

	if err := s.store.CreateApp(db.CreateAppParams{
		Slug:        req.Slug,
		Name:        req.Name,
		ProjectSlug: req.ProjectSlug,
		OwnerID:     u.ID,
	}); err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			writeError(w, http.StatusConflict, "slug already taken")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	app, err := s.store.GetAppBySlug(req.Slug)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	s.store.LogAuditEvent(db.AuditEventParams{
		UserID:       &u.ID,
		Action:       "create_app",
		ResourceType: "app",
		ResourceID:   req.Slug,
		IPAddress:    s.clientIP(r),
	})
	writeJSON(w, http.StatusCreated, app)
}

func (s *Server) handleGetApp(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	app, _, ok := s.requireViewApp(w, r, slug)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, app)
}

func (s *Server) handlePatchApp(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	if _, ok := s.requireManageApp(w, r, slug); !ok {
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad request")
		return
	}

	var raw map[string]json.RawMessage
	if len(body) > 0 {
		if err := json.Unmarshal(body, &raw); err != nil {
			writeError(w, http.StatusBadRequest, "bad request")
			return
		}
	}

	// Parse and validate all fields first so a bad request never causes a
	// partial write (e.g. hibernate_timeout persisted while name rejected).
	var (
		hibernateTimeout        *int
		setHibernateTimeout     bool
		newName                 string
		setName                 bool
		newProjectSlug          string
		setProjectSlug          bool
		memoryLimitMB           *int
		setMemoryLimitMB        bool
		cpuQuotaPercent         *int
		setCPUQuotaPercent      bool
	)

	if rawVal, present := raw["hibernate_timeout_minutes"]; present {
		var timeout *int
		if err := json.Unmarshal(rawVal, &timeout); err != nil {
			writeError(w, http.StatusBadRequest, "hibernate_timeout_minutes must be an integer or null")
			return
		}
		if timeout != nil && *timeout < 0 {
			writeError(w, http.StatusBadRequest, "hibernate_timeout_minutes must be >= 0")
			return
		}
		hibernateTimeout, setHibernateTimeout = timeout, true
	}

	if rawVal, present := raw["name"]; present {
		var name string
		if err := json.Unmarshal(rawVal, &name); err != nil {
			writeError(w, http.StatusBadRequest, "name must be a string")
			return
		}
		name = strings.TrimSpace(name)
		if len(name) < 1 || len(name) > 128 {
			writeError(w, http.StatusBadRequest, "name must be between 1 and 128 characters")
			return
		}
		newName, setName = name, true
	}

	if rawVal, present := raw["project_slug"]; present {
		var projectSlug string
		if err := json.Unmarshal(rawVal, &projectSlug); err != nil {
			writeError(w, http.StatusBadRequest, "project_slug must be a string")
			return
		}
		newProjectSlug, setProjectSlug = strings.TrimSpace(projectSlug), true
	}

	if rawVal, present := raw["memory_limit_mb"]; present {
		var v *int
		if err := json.Unmarshal(rawVal, &v); err != nil {
			writeError(w, http.StatusBadRequest, "memory_limit_mb must be an integer or null")
			return
		}
		memoryLimitMB, setMemoryLimitMB = v, true
	}

	if rawVal, present := raw["cpu_quota_percent"]; present {
		var v *int
		if err := json.Unmarshal(rawVal, &v); err != nil {
			writeError(w, http.StatusBadRequest, "cpu_quota_percent must be an integer or null")
			return
		}
		cpuQuotaPercent, setCPUQuotaPercent = v, true
	}

	// Apply all validated writes.
	if setHibernateTimeout {
		if err := s.store.UpdateHibernateTimeout(slug, hibernateTimeout); err != nil {
			if errors.Is(err, db.ErrNotFound) {
				writeError(w, http.StatusNotFound, "not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
	}
	if setName {
		if err := s.store.UpdateAppName(slug, newName); err != nil {
			if errors.Is(err, db.ErrNotFound) {
				writeError(w, http.StatusNotFound, "not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
	}
	if setProjectSlug {
		if err := s.store.UpdateAppProjectSlug(slug, newProjectSlug); err != nil {
			if errors.Is(err, db.ErrNotFound) {
				writeError(w, http.StatusNotFound, "not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
	}
	if setMemoryLimitMB || setCPUQuotaPercent {
		// Read current state to preserve whichever field is not being updated,
		// since UpdateResourceLimits writes both columns in a single statement.
		existing, err := s.store.GetApp(slug)
		if err != nil {
			if errors.Is(err, db.ErrNotFound) {
				writeError(w, http.StatusNotFound, "not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		newMemory := existing.MemoryLimitMB
		if setMemoryLimitMB {
			newMemory = memoryLimitMB
		}
		newCPU := existing.CPUQuotaPercent
		if setCPUQuotaPercent {
			newCPU = cpuQuotaPercent
		}
		if err := s.store.UpdateResourceLimits(db.UpdateResourceLimitsParams{
			Slug:            slug,
			MemoryLimitMB:   newMemory,
			CPUQuotaPercent: newCPU,
		}); err != nil {
			if errors.Is(err, db.ErrNotFound) {
				writeError(w, http.StatusNotFound, "not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
	}

	app, err := s.store.GetAppBySlug(slug)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if u := auth.UserFromContext(r.Context()); u != nil {
		s.store.LogAuditEvent(db.AuditEventParams{
			UserID: &u.ID, Action: "update_app", ResourceType: "app",
			ResourceID: slug, IPAddress: s.clientIP(r),
		})
	}
	writeJSON(w, http.StatusOK, app)
}

func (s *Server) handleDeployApp(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")

	app, ok := s.requireManageApp(w, r, slug)
	if !ok {
		return
	}

	if s.manager == nil {
		writeError(w, http.StatusServiceUnavailable, "process manager not available")
		return
	}

	file, err := readBundleUpload(w, r, maxBundleUploadSize)
	if err != nil {
		switch err {
		case errBundleTooLarge:
			writeError(w, http.StatusRequestEntityTooLarge, "bundle exceeds 128 MiB limit")
		case errBundleMissing:
			writeError(w, http.StatusBadRequest, "bundle file required")
		default:
			writeError(w, http.StatusBadRequest, "bad request")
		}
		return
	}
	defer file.Close()

	// Save bundle zip to disk
	version := fmt.Sprintf("%d", time.Now().UnixMilli())
	bundleZip := filepath.Join(s.cfg.Storage.AppsDir, slug, "bundles", version+".zip")
	if err := os.MkdirAll(filepath.Dir(bundleZip), 0755); err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out, err := os.Create(bundleZip)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if _, err := io.Copy(out, file); err != nil {
		out.Close()
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out.Close()

	// Extract bundle zip into a versioned directory
	bundleDir := filepath.Join(s.cfg.Storage.AppsDir, slug, "versions", version)
	if err := deploy.ExtractBundle(bundleZip, bundleDir); err != nil {
		fmt.Fprintf(os.Stderr, "extract bundle %s: %v\n", slug, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Stop existing instance before re-deploying; ignore the error since the
	// app may not have been running yet.
	_ = s.manager.Stop(slug)

	if s.proxy != nil {
		s.proxy.Deregister(slug)
	}

	result, err := deploy.Run(deploy.Params{
		Slug:            slug,
		BundleDir:       bundleDir,
		Manager:         s.manager,
		Proxy:           s.proxy,
		MemoryLimitMB:   deploy.ResolveMemoryLimitMB(app.MemoryLimitMB, s.cfg.Runtime.Docker.DefaultMemoryMB),
		CPUQuotaPercent: deploy.ResolveCPUQuotaPercent(app.CPUQuotaPercent, s.cfg.Runtime.Docker.DefaultCPUPercent),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "deploy.Run %s: %v\n", slug, err)
		if err := s.store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: slug, Status: "degraded"}); err != nil {
			fmt.Fprintf(os.Stderr, "update app status for %s: %v\n", slug, err)
		}
		writeError(w, http.StatusInternalServerError, "deploy failed")
		return
	}

	port := result.Port
	pid := result.PID
	if err := s.store.UpdateAppStatus(db.UpdateAppStatusParams{
		Slug:   slug,
		Status: "running",
		Port:   &port,
		PID:    &pid,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if err := s.store.IncrementDeployCount(slug); err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if _, err := s.store.CreateDeployment(db.CreateDeploymentParams{
		AppID:     app.ID,
		Version:   version,
		BundleDir: bundleDir,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	// Prune old version directories beyond the retention limit.
	go func() {
		if err := deploy.PruneOldVersions(s.cfg.Storage.AppsDir, slug, s.cfg.Storage.VersionRetention, bundleDir); err != nil {
			fmt.Fprintf(os.Stderr, "prune old versions %s: %v\n", slug, err)
		}
	}()

	updatedApp, err := s.store.GetAppBySlug(slug)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if u := auth.UserFromContext(r.Context()); u != nil {
		s.store.LogAuditEvent(db.AuditEventParams{
			UserID:       &u.ID,
			Action:       "deploy",
			ResourceType: "app",
			ResourceID:   slug,
			IPAddress:    s.clientIP(r),
		})
	}
	writeJSON(w, http.StatusOK, updatedApp)
}

func (s *Server) handleRollbackApp(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")

	app, ok := s.requireManageApp(w, r, slug)
	if !ok {
		return
	}

	// Parse optional body to support targeted rollback by deployment ID.
	var reqBody struct {
		DeploymentID *int64 `json:"deployment_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "bad request")
		return
	}

	var prev *db.Deployment

	if reqBody.DeploymentID != nil {
		// Targeted rollback: fetch the specific deployment and verify it belongs to this app.
		dep, err := s.store.GetDeploymentBySlugAndID(slug, *reqBody.DeploymentID)
		if err != nil {
			if errors.Is(err, db.ErrNotFound) {
				writeError(w, http.StatusNotFound, "deployment not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		prev = dep
	} else {
		// Default rollback: use the previous deployment (index 1, newest-first).
		deployments, err := s.store.ListDeployments(app.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		if len(deployments) < 2 {
			writeError(w, http.StatusConflict, "no previous deployment to roll back to")
			return
		}
		prev = deployments[1]
	}

	if s.manager == nil {
		writeError(w, http.StatusServiceUnavailable, "process manager not available")
		return
	}

	// Stop current instance; ignore the error if it wasn't running.
	_ = s.manager.Stop(slug)
	if s.proxy != nil {
		s.proxy.Deregister(slug)
	}

	result, err := deploy.Run(deploy.Params{
		Slug:            slug,
		BundleDir:       prev.BundleDir,
		Manager:         s.manager,
		Proxy:           s.proxy,
		MemoryLimitMB:   deploy.ResolveMemoryLimitMB(app.MemoryLimitMB, s.cfg.Runtime.Docker.DefaultMemoryMB),
		CPUQuotaPercent: deploy.ResolveCPUQuotaPercent(app.CPUQuotaPercent, s.cfg.Runtime.Docker.DefaultCPUPercent),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "rollback %s: %v\n", slug, err)
		if err := s.store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: slug, Status: "stopped"}); err != nil {
			fmt.Fprintf(os.Stderr, "update app status for %s: %v\n", slug, err)
		}
		writeError(w, http.StatusInternalServerError, "rollback failed")
		return
	}

	port := result.Port
	pid := result.PID
	if err := s.store.UpdateAppStatus(db.UpdateAppStatusParams{
		Slug:   slug,
		Status: "running",
		Port:   &port,
		PID:    &pid,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if _, err := s.store.CreateDeployment(db.CreateDeploymentParams{
		AppID:     app.ID,
		Version:   prev.Version,
		BundleDir: prev.BundleDir,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "record rollback deployment for %s: %v\n", slug, err)
		// app is running; don't fail the request over a record error
	}

	updatedApp, err := s.store.GetAppBySlug(slug)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if u := auth.UserFromContext(r.Context()); u != nil {
		s.store.LogAuditEvent(db.AuditEventParams{
			UserID:       &u.ID,
			Action:       "rollback",
			ResourceType: "app",
			ResourceID:   slug,
			IPAddress:    s.clientIP(r),
		})
	}
	// Rollbacks are not counted as deploys — deploy_count tracks forward deployments only.
	writeJSON(w, http.StatusOK, updatedApp)
}

func (s *Server) handleRestartApp(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")

	app, ok := s.requireManageApp(w, r, slug)
	if !ok {
		return
	}

	if s.manager == nil {
		writeError(w, http.StatusServiceUnavailable, "process manager not available")
		return
	}

	deployments, err := s.store.ListDeployments(app.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if len(deployments) == 0 {
		writeError(w, http.StatusConflict, "app has never been deployed")
		return
	}
	current := deployments[0]

	// Stop current instance; ignore the error if it wasn't running.
	_ = s.manager.Stop(slug)
	if s.proxy != nil {
		s.proxy.Deregister(slug)
	}

	result, err := deploy.Run(deploy.Params{
		Slug:            slug,
		BundleDir:       current.BundleDir,
		Manager:         s.manager,
		Proxy:           s.proxy,
		MemoryLimitMB:   deploy.ResolveMemoryLimitMB(app.MemoryLimitMB, s.cfg.Runtime.Docker.DefaultMemoryMB),
		CPUQuotaPercent: deploy.ResolveCPUQuotaPercent(app.CPUQuotaPercent, s.cfg.Runtime.Docker.DefaultCPUPercent),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "restart %s: %v\n", slug, err)
		if err := s.store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: slug, Status: "stopped"}); err != nil {
			fmt.Fprintf(os.Stderr, "update app status for %s: %v\n", slug, err)
		}
		writeError(w, http.StatusInternalServerError, "restart failed")
		return
	}

	port := result.Port
	pid := result.PID
	if err := s.store.UpdateAppStatus(db.UpdateAppStatusParams{
		Slug:   slug,
		Status: "running",
		Port:   &port,
		PID:    &pid,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	updatedApp, err := s.store.GetAppBySlug(slug)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if u := auth.UserFromContext(r.Context()); u != nil {
		s.store.LogAuditEvent(db.AuditEventParams{
			UserID:       &u.ID,
			Action:       "restart",
			ResourceType: "app",
			ResourceID:   slug,
			IPAddress:    s.clientIP(r),
		})
	}
	writeJSON(w, http.StatusOK, updatedApp)
}

func (s *Server) handleDeleteApp(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	if _, ok := s.requireManageApp(w, r, slug); !ok {
		return
	}

	// Stop the process if it is running; ignore the error (may not be running).
	if s.manager != nil {
		_ = s.manager.Stop(slug)
	}
	if s.proxy != nil {
		s.proxy.Deregister(slug)
	}

	if err := s.store.DeleteApp(slug); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if u := auth.UserFromContext(r.Context()); u != nil {
		s.store.LogAuditEvent(db.AuditEventParams{
			UserID:       &u.ID,
			Action:       "delete_app",
			ResourceType: "app",
			ResourceID:   slug,
			IPAddress:    s.clientIP(r),
		})
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleStopApp(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	if _, ok := s.requireManageApp(w, r, slug); !ok {
		return
	}

	// Stop the process if managed; ignore error if already stopped.
	if s.manager != nil {
		_ = s.manager.Stop(slug)
	}
	if s.proxy != nil {
		s.proxy.Deregister(slug)
	}

	// Update DB status and clear port/PID.
	if err := s.store.UpdateAppStatus(db.UpdateAppStatusParams{
		Slug:   slug,
		Status: "stopped",
		// Port and PID left nil to clear them in the DB.
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	app, err := s.store.GetAppBySlug(slug)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if u := auth.UserFromContext(r.Context()); u != nil {
		s.store.LogAuditEvent(db.AuditEventParams{
			UserID:       &u.ID,
			Action:       "stop",
			ResourceType: "app",
			ResourceID:   slug,
			IPAddress:    s.clientIP(r),
		})
	}
	writeJSON(w, http.StatusOK, app)
}

func (s *Server) handleSetAppAccess(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	if _, ok := s.requireManageApp(w, r, slug); !ok {
		return
	}
	var req struct {
		Access string `json:"access"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request")
		return
	}
	if req.Access != "public" && req.Access != "private" && req.Access != "shared" {
		writeError(w, http.StatusBadRequest, "access must be public, private, or shared")
		return
	}
	if err := s.store.SetAppAccess(slug, req.Access); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	app, err := s.store.GetAppBySlug(slug)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if u := auth.UserFromContext(r.Context()); u != nil {
		s.store.LogAuditEvent(db.AuditEventParams{
			UserID: &u.ID, Action: "set_access", ResourceType: "app",
			ResourceID: slug, IPAddress: s.clientIP(r),
		})
	}
	writeJSON(w, http.StatusOK, app)
}

func (s *Server) handleGrantAppAccess(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	if _, ok := s.requireManageApp(w, r, slug); !ok {
		return
	}
	var req struct {
		UserID int64 `json:"user_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request")
		return
	}
	if req.UserID == 0 {
		writeError(w, http.StatusBadRequest, "user_id is required")
		return
	}
	if _, err := s.store.GetUserByID(req.UserID); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "user not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if err := s.store.GrantAppAccess(slug, req.UserID); err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if u := auth.UserFromContext(r.Context()); u != nil {
		s.store.LogAuditEvent(db.AuditEventParams{
			UserID:       &u.ID,
			Action:       "grant_access",
			ResourceType: "app",
			ResourceID:   slug,
			Detail:       fmt.Sprintf("user_id=%d", req.UserID),
			IPAddress:    s.clientIP(r),
		})
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRevokeAppAccess(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	if _, ok := s.requireManageApp(w, r, slug); !ok {
		return
	}

	var userID int64

	// Prefer the path parameter when present (DELETE /api/apps/{slug}/members/{user_id}).
	// Fall back to parsing the JSON body for backward compatibility.
	if pathUserID := chi.URLParam(r, "user_id"); pathUserID != "" {
		id, err := strconv.ParseInt(pathUserID, 10, 64)
		if err != nil || id == 0 {
			writeError(w, http.StatusBadRequest, "invalid user_id")
			return
		}
		userID = id
	} else {
		var req struct {
			UserID int64 `json:"user_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad request")
			return
		}
		if req.UserID == 0 {
			writeError(w, http.StatusBadRequest, "user_id is required")
			return
		}
		userID = req.UserID
	}

	if err := s.store.RevokeAppAccess(slug, userID); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "member not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if u := auth.UserFromContext(r.Context()); u != nil {
		s.store.LogAuditEvent(db.AuditEventParams{
			UserID:       &u.ID,
			Action:       "revoke_access",
			ResourceType: "app",
			ResourceID:   slug,
			Detail:       fmt.Sprintf("user_id=%d", userID),
			IPAddress:    s.clientIP(r),
		})
	}
	w.WriteHeader(http.StatusNoContent)
}

type appMemberResponse struct {
	UserID   int64  `json:"user_id"`
	Username string `json:"username"`
	Role     string `json:"role"`
}

func (s *Server) handleGetMembers(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	if _, ok := s.requireManageApp(w, r, slug); !ok {
		return
	}
	limit, offset := parsePagination(r)
	members, err := s.store.ListAppMembers(slug, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	resp := make([]appMemberResponse, len(members))
	for i, m := range members {
		resp[i] = appMemberResponse{UserID: m.UserID, Username: m.Username, Role: m.Role}
	}
	writeJSON(w, http.StatusOK, resp)
}

type userLookupResponse struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

func (s *Server) handleGetUser(w http.ResponseWriter, r *http.Request) {
	username := chi.URLParam(r, "username")
	if username == "" {
		writeError(w, http.StatusBadRequest, "username is required")
		return
	}
	user, err := s.store.GetUserByUsername(username)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	writeJSON(w, http.StatusOK, userLookupResponse{ID: user.ID, Username: user.Username})
}

type metricsResponse struct {
	Status     string  `json:"status"`
	PID        int     `json:"pid,omitempty"`
	CPUPercent float64 `json:"cpu_percent,omitempty"`
	RSSBytes   int64   `json:"rss_bytes,omitempty"`
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	app, _, ok := s.requireViewApp(w, r, slug)
	if !ok {
		return
	}

	if s.manager == nil {
		writeJSON(w, http.StatusOK, metricsResponse{Status: app.Status})
		return
	}

	info, ok := s.manager.Get(slug)
	if !ok {
		writeJSON(w, http.StatusOK, metricsResponse{Status: app.Status})
		return
	}
	if info.Status != process.StatusRunning {
		writeJSON(w, http.StatusOK, metricsResponse{Status: string(info.Status)})
		return
	}

	handle, ok := s.manager.Handle(slug)
	if !ok {
		http.Error(w, "app not running", http.StatusServiceUnavailable)
		return
	}
	stats, err := s.sampler.Sample(handle)
	if err != nil {
		// Process may have exited between status check and sample.
		writeJSON(w, http.StatusOK, metricsResponse{Status: string(process.StatusStopped)})
		return
	}

	writeJSON(w, http.StatusOK, metricsResponse{
		Status:     string(info.Status),
		PID:        info.PID,
		CPUPercent: stats.CPUPercent,
		RSSBytes:   stats.RSSBytes,
	})
}

func (s *Server) handleListDeployments(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	if _, _, ok := s.requireViewApp(w, r, slug); !ok {
		return
	}
	deployments, err := s.store.ListDeploymentsBySlug(slug)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	writeJSON(w, http.StatusOK, deployments)
}

// parsePagination extracts optional ?limit= and ?offset= query parameters.
// Returns 0 for both when absent, which callers interpret as "no pagination".
func parsePagination(r *http.Request) (limit, offset int) {
	if s := r.URL.Query().Get("limit"); s != "" {
		if v, err := strconv.Atoi(s); err == nil && v > 0 {
			limit = v
		}
	}
	if s := r.URL.Query().Get("offset"); s != "" {
		if v, err := strconv.Atoi(s); err == nil && v >= 0 {
			offset = v
		}
	}
	return limit, offset
}
