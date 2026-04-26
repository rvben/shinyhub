package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/process"
	slugpkg "github.com/rvben/shinyhub/internal/slug"
	"github.com/rvben/shinyhub/internal/storage"
)

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
	if !slugpkg.Valid(req.Slug) {
		writeError(w, http.StatusBadRequest, "slug must be "+slugpkg.HumanRule)
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

	if err := storage.RequireFreeSlug(s.cfg, req.Slug); err != nil {
		if errors.Is(err, storage.ErrSlugInUse) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "internal server error")
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

	// Apply the operator-configured default replica count when it exceeds the
	// SQL DEFAULT of 1. Zero and one are left alone (zero is invalid; one
	// matches the default).
	if s.cfg.Runtime.DefaultReplicas > 1 {
		created, err := s.store.GetAppBySlug(req.Slug)
		if err == nil {
			if err := s.store.UpdateAppReplicas(created.ID, s.cfg.Runtime.DefaultReplicas); err != nil {
				slog.Error("set default replicas on create", "slug", req.Slug, "err", err)
			}
		}
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
		IPAddress:    s.ClientIP(r),
	})
	writeJSON(w, http.StatusCreated, app)
}

func (s *Server) handleGetApp(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	app, _, ok := s.requireViewApp(w, r, slug)
	if !ok {
		return
	}

	replicas, err := s.store.ListReplicas(app.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if replicas == nil {
		replicas = []*db.Replica{}
	}

	// Merge live process state into DB rows when the manager is available.
	if s.manager != nil {
		live := s.manager.AllForSlug(slug)
		for i, rep := range replicas {
			if rep.Index < len(live) && live[rep.Index] != nil {
				replicas[i].Status = string(live[rep.Index].Status)
				if live[rep.Index].PID != 0 {
					pid := live[rep.Index].PID
					replicas[i].PID = &pid
				}
				if live[rep.Index].Port != 0 {
					port := live[rep.Index].Port
					replicas[i].Port = &port
				}
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"app": app, "replicas_status": replicas})
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
		hibernateTimeout         *int
		setHibernateTimeout      bool
		newName                  string
		setName                  bool
		newProjectSlug           string
		setProjectSlug           bool
		memoryLimitMB            *int
		setMemoryLimitMB         bool
		cpuQuotaPercent          *int
		setCPUQuotaPercent       bool
		newReplicas              int
		setReplicas              bool
		newMaxSessions           int
		setMaxSessions           bool
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
		if v != nil && *v < 0 {
			http.Error(w, "memory_limit_mb must be non-negative", http.StatusBadRequest)
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
		if v != nil && (*v < 0 || *v > 100) {
			http.Error(w, "cpu_quota_percent must be between 0 and 100", http.StatusBadRequest)
			return
		}
		cpuQuotaPercent, setCPUQuotaPercent = v, true
	}

	if rawVal, present := raw["replicas"]; present {
		var n int
		if err := json.Unmarshal(rawVal, &n); err != nil {
			writeError(w, http.StatusBadRequest, "replicas must be an integer")
			return
		}
		if n < 1 {
			writeError(w, http.StatusBadRequest, "replicas must be >= 1")
			return
		}
		if s.cfg.Runtime.MaxReplicas > 0 && n > s.cfg.Runtime.MaxReplicas {
			writeError(w, http.StatusBadRequest,
				fmt.Sprintf("replicas must be between 1 and %d", s.cfg.Runtime.MaxReplicas))
			return
		}
		newReplicas, setReplicas = n, true
	}

	if rawVal, present := raw["max_sessions_per_replica"]; present {
		var n int
		if err := json.Unmarshal(rawVal, &n); err != nil {
			writeError(w, http.StatusBadRequest, "max_sessions_per_replica must be an integer")
			return
		}
		// 0 explicitly means "fall back to the runtime default"; upper bound
		// mirrors the DB CHECK constraint (migration 012).
		if n < 0 || n > 1000 {
			writeError(w, http.StatusBadRequest, "max_sessions_per_replica must be between 0 and 1000")
			return
		}
		newMaxSessions, setMaxSessions = n, true
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

	if setReplicas {
		// Load current app state to check for shrink and running status.
		existing, err := s.store.GetApp(slug)
		if err != nil {
			if errors.Is(err, db.ErrNotFound) {
				writeError(w, http.StatusNotFound, "not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		// On shrink, prune obsolete replica rows before updating the count so
		// ListReplicas stays consistent during the transition.
		if newReplicas < existing.Replicas {
			if err := s.store.DeleteReplicasAbove(existing.ID, newReplicas); err != nil {
				writeError(w, http.StatusInternalServerError, "internal server error")
				return
			}
		}
		if err := s.store.UpdateAppReplicas(existing.ID, newReplicas); err != nil {
			if errors.Is(err, db.ErrNotFound) {
				writeError(w, http.StatusNotFound, "not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		if existing.Status == "running" {
			go s.redeployApp(slug)
		}
	}

	if setMaxSessions {
		existing, err := s.store.GetApp(slug)
		if err != nil {
			if errors.Is(err, db.ErrNotFound) {
				writeError(w, http.StatusNotFound, "not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		if err := s.store.UpdateAppMaxSessionsPerReplica(existing.ID, newMaxSessions); err != nil {
			if errors.Is(err, db.ErrNotFound) {
				writeError(w, http.StatusNotFound, "not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		if s.proxy != nil {
			s.proxy.SetPoolCap(slug,
				deploy.ResolveMaxSessionsPerReplica(newMaxSessions, s.cfg.Runtime.DefaultMaxSessionsPerReplica))
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
			ResourceID: slug, IPAddress: s.ClientIP(r),
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

	maxSize := maxBundleUploadSize
	if cap := int64(s.cfg.Storage.MaxBundleMB); cap > 0 {
		maxSize = cap * 1024 * 1024
	}
	file, cleanup, err := readBundleUpload(w, r, maxSize)
	defer cleanup()
	if err != nil {
		switch err {
		case errBundleTooLarge:
			capMB := s.cfg.Storage.MaxBundleMB
			if capMB == 0 {
				capMB = 128
			}
			writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("bundle exceeds %d MiB cap", capMB))
		case errBundleMissing:
			writeError(w, http.StatusBadRequest, "bundle file required")
		default:
			writeError(w, http.StatusBadRequest, "bad request")
		}
		return
	}

	// Compute paths up front so a single defer can clean up the on-disk
	// artefacts on any failure path before the deploy is committed.
	version := fmt.Sprintf("%d", time.Now().UnixMilli())
	bundleZip := filepath.Join(s.cfg.Storage.AppsDir, slug, "bundles", version+".zip")
	bundleDir := filepath.Join(s.cfg.Storage.AppsDir, slug, "versions", version)

	// keepFiles is flipped to true only once deploy.Run succeeds and the new
	// pool is actually serving the bundle. Any earlier failure — write,
	// extract, quota, deploy — leaves the apps tree as we found it.
	keepFiles := false
	defer func() {
		if !keepFiles {
			_ = os.RemoveAll(bundleDir)
			_ = os.Remove(bundleZip)
		}
	}()

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

	if err := deploy.ExtractBundle(bundleZip, bundleDir); err != nil {
		fmt.Fprintf(os.Stderr, "extract bundle %s: %v\n", slug, err)
		if errors.Is(err, deploy.ErrBundleRejected) {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		if errors.Is(err, deploy.ErrBundleTooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "bundle extracted size exceeds limit")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Enforce per-app disk quota: the new extracted version has already been
	// written, so DirSize now reflects the post-deploy footprint. The defer
	// above rolls the new files back if we reject here.
	if s.cfg.Storage.AppQuotaMB > 0 {
		used, qErr := deploy.CheckAppQuota(s.cfg.Storage.AppsDir, s.cfg.Storage.AppDataDir, slug, s.cfg.Storage.AppQuotaMB)
		if qErr != nil {
			if errors.Is(qErr, deploy.ErrQuotaExceeded) {
				s.logQuotaRejected(r, slug, used)
				writeQuotaExceeded(w, used, s.cfg.Storage.AppQuotaMB)
				return
			}
			slog.Warn("quota check failed", "slug", slug, "err", qErr)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
	}

	// Serialize the mutation phase so a concurrent restart/rollback/stop on
	// the same slug can't tear down the pool we are about to bring up.
	release := s.acquireDeployLock(slug)
	defer release()

	// Stop existing instance before re-deploying; ignore the error since the
	// app may not have been running yet.
	_ = s.manager.Stop(slug)

	if s.proxy != nil {
		s.proxy.Deregister(slug)
	}

	result, err := deploy.Run(deploy.Params{
		Slug:                  slug,
		BundleDir:             bundleDir,
		Replicas:              app.Replicas,
		Manager:               s.manager,
		Proxy:                 s.proxy,
		MemoryLimitMB:         deploy.ResolveMemoryLimitMB(app.MemoryLimitMB, s.cfg.Runtime.Docker.DefaultMemoryMB),
		CPUQuotaPercent:       deploy.ResolveCPUQuotaPercent(app.CPUQuotaPercent, s.cfg.Runtime.Docker.DefaultCPUPercent),
		MaxSessionsPerReplica: deploy.ResolveMaxSessionsPerReplica(app.MaxSessionsPerReplica, s.cfg.Runtime.DefaultMaxSessionsPerReplica),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "deploy.Run %s: %v\n", slug, err)
		if err := s.store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: slug, Status: "degraded"}); err != nil {
			fmt.Fprintf(os.Stderr, "update app status for %s: %v\n", slug, err)
		}
		writeError(w, http.StatusInternalServerError, "deploy failed")
		return
	}
	// The pool is now serving the new bundle; from here onwards the on-disk
	// artefacts must survive any subsequent error so a follow-up rollback or
	// recovery still has the directory to point at.
	keepFiles = true

	for _, r := range result.Replicas {
		pid, port := r.PID, r.Port
		if err := s.store.UpsertReplica(db.UpsertReplicaParams{
			AppID:  app.ID,
			Index:  r.Index,
			PID:    &pid,
			Port:   &port,
			Status: "running",
		}); err != nil {
			fmt.Fprintf(os.Stderr, "upsert replica %s[%d]: %v\n", slug, r.Index, err)
		}
	}
	// Bookkeeping after the proxy switch: the new pool is already serving
	// traffic, so a transient DB hiccup here must NOT surface as "deploy
	// failed" — that would push the caller into a retry loop on top of an
	// already-running deploy. Log loudly so an operator notices the
	// reconciliation gap (status watchdog will eventually correct
	// running-status; deploy_count and the deployment history row are
	// genuinely lost on failure but the app is fine).
	if err := s.store.UpdateAppStatus(db.UpdateAppStatusParams{
		Slug:   slug,
		Status: "running",
	}); err != nil {
		slog.Error("deploy: persist running status failed; pool is live", "slug", slug, "err", err)
	}

	if err := s.store.IncrementDeployCount(slug); err != nil {
		slog.Error("deploy: increment deploy_count failed; pool is live", "slug", slug, "err", err)
	}

	if _, err := s.store.CreateDeployment(db.CreateDeploymentParams{
		AppID:     app.ID,
		Version:   version,
		BundleDir: bundleDir,
		Status:    "succeeded",
	}); err != nil {
		slog.Error("deploy: record deployment row failed; pool is live", "slug", slug, "version", version, "err", err)
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
			IPAddress:    s.ClientIP(r),
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

	// Serialize against concurrent deploy/restart/stop on the same slug.
	release := s.acquireDeployLock(slug)
	defer release()

	// Validate that the target bundle still exists on disk BEFORE we tear
	// down the running app. If the directory was pruned out from under us
	// (manual cleanup, disk failure, etc.) deploy.Run would fail and we'd
	// be left with the live app stopped and no path back to running.
	if info, err := os.Stat(prev.BundleDir); err != nil || !info.IsDir() {
		writeError(w, http.StatusConflict, "target deployment bundle is missing or invalid")
		return
	}

	// Stop current instance; ignore the error if it wasn't running.
	_ = s.manager.Stop(slug)
	if s.proxy != nil {
		s.proxy.Deregister(slug)
	}

	result, err := deploy.Run(deploy.Params{
		Slug:                  slug,
		BundleDir:             prev.BundleDir,
		Replicas:              app.Replicas,
		Manager:               s.manager,
		Proxy:                 s.proxy,
		MemoryLimitMB:         deploy.ResolveMemoryLimitMB(app.MemoryLimitMB, s.cfg.Runtime.Docker.DefaultMemoryMB),
		CPUQuotaPercent:       deploy.ResolveCPUQuotaPercent(app.CPUQuotaPercent, s.cfg.Runtime.Docker.DefaultCPUPercent),
		MaxSessionsPerReplica: deploy.ResolveMaxSessionsPerReplica(app.MaxSessionsPerReplica, s.cfg.Runtime.DefaultMaxSessionsPerReplica),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "rollback %s: %v\n", slug, err)
		if err := s.store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: slug, Status: "stopped"}); err != nil {
			fmt.Fprintf(os.Stderr, "update app status for %s: %v\n", slug, err)
		}
		writeError(w, http.StatusInternalServerError, "rollback failed")
		return
	}

	for _, r := range result.Replicas {
		pid, port := r.PID, r.Port
		if err := s.store.UpsertReplica(db.UpsertReplicaParams{
			AppID:  app.ID,
			Index:  r.Index,
			PID:    &pid,
			Port:   &port,
			Status: "running",
		}); err != nil {
			fmt.Fprintf(os.Stderr, "upsert replica %s[%d]: %v\n", slug, r.Index, err)
		}
	}
	// Bookkeeping after the proxy switch: the rolled-back pool is already
	// serving traffic, so a transient DB hiccup here must NOT surface as
	// "rollback failed" — that would push the caller into a retry loop on top
	// of an already-running rollback. Log loudly so an operator notices the
	// reconciliation gap (status watchdog will eventually correct
	// running-status; the rollback history row is genuinely lost on failure
	// but the app is fine).
	if err := s.store.UpdateAppStatus(db.UpdateAppStatusParams{
		Slug:   slug,
		Status: "running",
	}); err != nil {
		slog.Error("rollback: persist running status failed; pool is live", "slug", slug, "err", err)
	}

	if _, err := s.store.CreateDeployment(db.CreateDeploymentParams{
		AppID:     app.ID,
		Version:   prev.Version,
		BundleDir: prev.BundleDir,
		Status:    "succeeded",
	}); err != nil {
		slog.Error("rollback: record deployment row failed; pool is live", "slug", slug, "version", prev.Version, "err", err)
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
			IPAddress:    s.ClientIP(r),
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

	// Serialize against concurrent deploy/rollback/stop on the same slug.
	release := s.acquireDeployLock(slug)
	defer release()

	// Stop current instance; ignore the error if it wasn't running.
	_ = s.manager.Stop(slug)
	if s.proxy != nil {
		s.proxy.Deregister(slug)
	}

	result, err := deploy.Run(deploy.Params{
		Slug:                  slug,
		BundleDir:             current.BundleDir,
		Replicas:              app.Replicas,
		Manager:               s.manager,
		Proxy:                 s.proxy,
		MemoryLimitMB:         deploy.ResolveMemoryLimitMB(app.MemoryLimitMB, s.cfg.Runtime.Docker.DefaultMemoryMB),
		CPUQuotaPercent:       deploy.ResolveCPUQuotaPercent(app.CPUQuotaPercent, s.cfg.Runtime.Docker.DefaultCPUPercent),
		MaxSessionsPerReplica: deploy.ResolveMaxSessionsPerReplica(app.MaxSessionsPerReplica, s.cfg.Runtime.DefaultMaxSessionsPerReplica),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "restart %s: %v\n", slug, err)
		if err := s.store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: slug, Status: "stopped"}); err != nil {
			fmt.Fprintf(os.Stderr, "update app status for %s: %v\n", slug, err)
		}
		writeError(w, http.StatusInternalServerError, "restart failed")
		return
	}

	for _, r := range result.Replicas {
		pid, port := r.PID, r.Port
		if err := s.store.UpsertReplica(db.UpsertReplicaParams{
			AppID:  app.ID,
			Index:  r.Index,
			PID:    &pid,
			Port:   &port,
			Status: "running",
		}); err != nil {
			fmt.Fprintf(os.Stderr, "upsert replica %s[%d]: %v\n", slug, r.Index, err)
		}
	}
	// Bookkeeping after the proxy switch: the restarted pool is already
	// serving traffic, so a transient DB hiccup here must NOT surface as
	// "restart failed" — that would push the caller into a retry loop on top
	// of an already-running restart. Log loudly so an operator notices the
	// reconciliation gap (status watchdog will eventually correct
	// running-status).
	if err := s.store.UpdateAppStatus(db.UpdateAppStatusParams{
		Slug:   slug,
		Status: "running",
	}); err != nil {
		slog.Error("restart: persist running status failed; pool is live", "slug", slug, "err", err)
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
			IPAddress:    s.ClientIP(r),
		})
	}
	writeJSON(w, http.StatusOK, updatedApp)
}

func (s *Server) handleDeleteApp(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	if _, ok := s.requireManageApp(w, r, slug); !ok {
		return
	}

	// Serialize against any in-flight deploy/restart on this slug so we don't
	// race the process manager into an inconsistent state mid-teardown.
	release := s.acquireDeployLock(slug)
	defer release()

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

	// Clean up on-disk state after the DB row is gone. Errors are non-fatal
	// (the record is already deleted) but are captured in the audit detail so
	// operators can investigate orphaned bytes.
	detail := ""
	if cleanupErr := storage.OnAppDelete(s.cfg, slug); cleanupErr != nil {
		detail = cleanupErr.Error()
		slog.Error("app delete cleanup failed", "slug", slug, "err", cleanupErr)
	}

	if u := auth.UserFromContext(r.Context()); u != nil {
		s.store.LogAuditEvent(db.AuditEventParams{
			UserID:       &u.ID,
			Action:       "delete_app",
			ResourceType: "app",
			ResourceID:   slug,
			Detail:       detail,
			IPAddress:    s.ClientIP(r),
		})
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleStopApp(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	app, ok := s.requireManageApp(w, r, slug)
	if !ok {
		return
	}

	// Serialize with any in-flight deploy/restart on this slug.
	release := s.acquireDeployLock(slug)
	defer release()

	// Stop the process if managed; ignore error if already stopped.
	if s.manager != nil {
		_ = s.manager.Stop(slug)
	}
	if s.proxy != nil {
		s.proxy.Deregister(slug)
	}

	// Mark all replica rows as stopped so GET /api/apps/:slug reflects
	// consistent state immediately after the manual stop.
	if replicas, err := s.store.ListReplicas(app.ID); err != nil {
		slog.Error("list replicas on stop", "slug", slug, "err", err)
	} else {
		for _, rep := range replicas {
			if err := s.store.UpsertReplica(db.UpsertReplicaParams{
				AppID:  app.ID,
				Index:  rep.Index,
				Status: "stopped",
			}); err != nil {
				slog.Error("upsert replica on stop", "slug", slug, "index", rep.Index, "err", err)
			}
		}
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
			IPAddress:    s.ClientIP(r),
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
			ResourceID: slug, IPAddress: s.ClientIP(r),
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
			IPAddress:    s.ClientIP(r),
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
			IPAddress:    s.ClientIP(r),
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

type replicaMetrics struct {
	Index      int     `json:"index"`
	Status     string  `json:"status"`
	PID        int     `json:"pid,omitempty"`
	CPUPercent float64 `json:"cpu_percent,omitempty"`
	RSSBytes   int64   `json:"rss_bytes,omitempty"`
	// Sessions is the proxy's best-effort live connection count for this
	// replica. Omitted (and -1 internally) when the replica slot is empty.
	Sessions int64 `json:"sessions"`
}

type metricsResponse struct {
	// Status is the app-level status: "running" if any replica is running,
	// otherwise the dominant replica status (or the DB-recorded status if
	// no replicas are tracked yet).
	Status string `json:"status"`
	// SessionsCap is the per-replica session cap currently in effect for
	// this pool. 0 means uncapped.
	SessionsCap int              `json:"sessions_cap"`
	Replicas    []replicaMetrics `json:"replicas"`
	// Legacy fields preserved so existing clients (dashboard card poller)
	// keep working while they adopt the per-replica view. These mirror the
	// first running replica.
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

	resp := metricsResponse{Status: app.Status, Replicas: []replicaMetrics{}}

	if s.manager == nil {
		writeJSON(w, http.StatusOK, resp)
		return
	}

	var sessionCounts []int64
	if s.proxy != nil {
		sessionCounts = s.proxy.ReplicaSessionCounts(slug)
		resp.SessionsCap = s.proxy.PoolCap(slug)
	}

	infos := s.manager.AllForSlug(slug)
	if len(infos) == 0 {
		writeJSON(w, http.StatusOK, resp)
		return
	}

	// Sessions-count slice may be shorter than infos if SetPoolSize raced
	// with a Deregister; clamp lookups to avoid out-of-range reads.
	sessionAt := func(i int) int64 {
		if i < len(sessionCounts) {
			return sessionCounts[i]
		}
		return -1
	}

	anyRunning := false
	for i, info := range infos {
		rm := replicaMetrics{Index: i, Sessions: sessionAt(i)}
		if info == nil {
			rm.Status = string(process.StatusStopped)
			resp.Replicas = append(resp.Replicas, rm)
			continue
		}
		rm.Status = string(info.Status)
		rm.PID = info.PID
		if info.Status == process.StatusRunning {
			if handle, ok := s.manager.HandleReplica(slug, i); ok {
				if stats, err := s.sampler.Sample(handle); err == nil {
					rm.CPUPercent = stats.CPUPercent
					rm.RSSBytes = stats.RSSBytes
				} else {
					rm.Status = string(process.StatusStopped)
				}
			} else {
				rm.Status = string(process.StatusStopped)
			}
			if rm.Status == string(process.StatusRunning) && !anyRunning {
				anyRunning = true
				resp.PID = rm.PID
				resp.CPUPercent = rm.CPUPercent
				resp.RSSBytes = rm.RSSBytes
			}
		}
		resp.Replicas = append(resp.Replicas, rm)
	}
	if anyRunning {
		resp.Status = string(process.StatusRunning)
	}

	writeJSON(w, http.StatusOK, resp)
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

// writeQuotaExceeded returns a 413 with structured detail so callers can
// surface the measured footprint alongside the configured quota.
func writeQuotaExceeded(w http.ResponseWriter, usedBytes int64, quotaMB int) {
	writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{
		"error":    "app disk quota exceeded",
		"used_mb":  usedBytes / deploy.MiB,
		"quota_mb": quotaMB,
	})
}

// logQuotaRejected emits an audit record so operators can see when a deploy
// was rejected for quota reasons (and by whom).
func (s *Server) logQuotaRejected(r *http.Request, slug string, usedBytes int64) {
	var userID *int64
	if u := auth.UserFromContext(r.Context()); u != nil {
		userID = &u.ID
	}
	s.store.LogAuditEvent(db.AuditEventParams{
		UserID:       userID,
		Action:       "deploy_rejected_quota",
		ResourceType: "app",
		ResourceID:   slug,
		Detail:       fmt.Sprintf("used=%d bytes, quota=%d MiB", usedBytes, s.cfg.Storage.AppQuotaMB),
		IPAddress:    s.ClientIP(r),
	})
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
