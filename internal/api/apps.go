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
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var (
		apps []*db.App
		err  error
	)
	if isPrivilegedAppOperator(u) {
		apps, err = s.store.ListApps()
	} else {
		apps, err = s.store.ListAppsVisibleToUser(u.ID)
	}
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
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
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if req.Slug == "" || req.Name == "" {
		http.Error(w, "slug and name are required", http.StatusBadRequest)
		return
	}
	if !slugRE.MatchString(req.Slug) {
		http.Error(w, "slug must match ^[a-z0-9][a-z0-9-]{0,62}$", http.StatusBadRequest)
		return
	}

	u := auth.UserFromContext(r.Context())
	if u == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !canCreateApps(u) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	if err := s.store.CreateApp(db.CreateAppParams{
		Slug:        req.Slug,
		Name:        req.Name,
		ProjectSlug: req.ProjectSlug,
		OwnerID:     u.ID,
	}); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	app, err := s.store.GetAppBySlug(req.Slug)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

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

// patchAppRequest holds the updatable fields for an app.
// HibernateTimeoutMinutes uses *int so that both a missing field and an
// explicit JSON null are treated identically: store SQL NULL, which means
// "inherit the global hibernate_timeout config".
type patchAppRequest struct {
	HibernateTimeoutMinutes *int `json:"hibernate_timeout_minutes"`
}

func (s *Server) handlePatchApp(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	if _, ok := s.requireManageApp(w, r, slug); !ok {
		return
	}

	var req patchAppRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	if err := s.store.UpdateHibernateTimeout(slug, req.HibernateTimeoutMinutes); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	app, err := s.store.GetAppBySlug(slug)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
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
		http.Error(w, "process manager not available", http.StatusServiceUnavailable)
		return
	}

	file, err := readBundleUpload(w, r, maxBundleUploadSize)
	if err != nil {
		switch err {
		case errBundleTooLarge:
			http.Error(w, "bundle exceeds 128 MiB limit", http.StatusRequestEntityTooLarge)
		case errBundleMissing:
			http.Error(w, "bundle file required", http.StatusBadRequest)
		default:
			http.Error(w, "bad request", http.StatusBadRequest)
		}
		return
	}
	defer file.Close()

	// Save bundle zip to disk
	version := fmt.Sprintf("%d", time.Now().UnixMilli())
	bundleZip := filepath.Join(s.cfg.Storage.AppsDir, slug, "bundles", version+".zip")
	if err := os.MkdirAll(filepath.Dir(bundleZip), 0755); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	out, err := os.Create(bundleZip)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if _, err := io.Copy(out, file); err != nil {
		out.Close()
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	out.Close()

	// Extract bundle zip into a versioned directory
	bundleDir := filepath.Join(s.cfg.Storage.AppsDir, slug, "versions", version)
	if err := deploy.ExtractBundle(bundleZip, bundleDir); err != nil {
		fmt.Fprintf(os.Stderr, "extract bundle %s: %v\n", slug, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Stop existing instance before re-deploying; ignore the error since the
	// app may not have been running yet.
	_ = s.manager.Stop(slug)

	if s.proxy != nil {
		s.proxy.Deregister(slug)
	}

	result, err := deploy.Run(deploy.Params{
		Slug:      slug,
		BundleDir: bundleDir,
		Manager:   s.manager,
		Proxy:     s.proxy,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "deploy.Run %s: %v\n", slug, err)
		if err := s.store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: slug, Status: "degraded"}); err != nil {
			fmt.Fprintf(os.Stderr, "update app status for %s: %v\n", slug, err)
		}
		http.Error(w, "deploy failed", http.StatusInternalServerError)
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
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	if err := s.store.IncrementDeployCount(slug); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	if _, err := s.store.CreateDeployment(db.CreateDeploymentParams{
		AppID:     app.ID,
		Version:   version,
		BundleDir: bundleDir,
	}); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"pid":  result.PID,
		"port": result.Port,
	})
}

func (s *Server) handleRollbackApp(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")

	app, ok := s.requireManageApp(w, r, slug)
	if !ok {
		return
	}

	if s.manager == nil {
		http.Error(w, "process manager not available", http.StatusServiceUnavailable)
		return
	}

	deployments, err := s.store.ListDeployments(app.ID)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	// deployments are ordered newest-first; index 1 is the previous deploy.
	if len(deployments) < 2 {
		http.Error(w, "no previous deployment to roll back to", http.StatusConflict)
		return
	}
	prev := deployments[1]

	// Stop current instance; ignore the error if it wasn't running.
	_ = s.manager.Stop(slug)
	if s.proxy != nil {
		s.proxy.Deregister(slug)
	}

	result, err := deploy.Run(deploy.Params{
		Slug:      slug,
		BundleDir: prev.BundleDir,
		Manager:   s.manager,
		Proxy:     s.proxy,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "rollback %s: %v\n", slug, err)
		if err := s.store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: slug, Status: "stopped"}); err != nil {
			fmt.Fprintf(os.Stderr, "update app status for %s: %v\n", slug, err)
		}
		http.Error(w, "rollback failed", http.StatusInternalServerError)
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
		http.Error(w, "internal server error", http.StatusInternalServerError)
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

	writeJSON(w, http.StatusOK, map[string]any{
		"pid":     result.PID,
		"port":    result.Port,
		"version": prev.Version,
	})
}

func (s *Server) handleRestartApp(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")

	app, ok := s.requireManageApp(w, r, slug)
	if !ok {
		return
	}

	if s.manager == nil {
		http.Error(w, "process manager not available", http.StatusServiceUnavailable)
		return
	}

	deployments, err := s.store.ListDeployments(app.ID)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if len(deployments) == 0 {
		http.Error(w, "app has never been deployed", http.StatusConflict)
		return
	}
	current := deployments[0]

	// Stop current instance; ignore the error if it wasn't running.
	_ = s.manager.Stop(slug)
	if s.proxy != nil {
		s.proxy.Deregister(slug)
	}

	result, err := deploy.Run(deploy.Params{
		Slug:      slug,
		BundleDir: current.BundleDir,
		Manager:   s.manager,
		Proxy:     s.proxy,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "restart %s: %v\n", slug, err)
		if err := s.store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: slug, Status: "stopped"}); err != nil {
			fmt.Fprintf(os.Stderr, "update app status for %s: %v\n", slug, err)
		}
		http.Error(w, "restart failed", http.StatusInternalServerError)
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
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"pid":  result.PID,
		"port": result.Port,
	})
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
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if req.Access != "public" && req.Access != "private" && req.Access != "shared" {
		http.Error(w, "access must be public, private, or shared", http.StatusBadRequest)
		return
	}
	if err := s.store.SetAppAccess(slug, req.Access); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	app, err := s.store.GetAppBySlug(slug)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
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
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if req.UserID == 0 {
		http.Error(w, "user_id is required", http.StatusBadRequest)
		return
	}
	if _, err := s.store.GetUserByID(req.UserID); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			http.Error(w, "user not found", http.StatusNotFound)
			return
		}
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if err := s.store.GrantAppAccess(slug, req.UserID); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRevokeAppAccess(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	if _, ok := s.requireManageApp(w, r, slug); !ok {
		return
	}
	var req struct {
		UserID int64 `json:"user_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if req.UserID == 0 {
		http.Error(w, "user_id is required", http.StatusBadRequest)
		return
	}
	if err := s.store.RevokeAppAccess(slug, req.UserID); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type metricsResponse struct {
	Status     string  `json:"status"`
	PID        int     `json:"pid,omitempty"`
	CPUPercent float64 `json:"cpu_percent,omitempty"`
	RSSBytes   int64   `json:"rss_bytes,omitempty"`
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	if _, _, ok := s.requireViewApp(w, r, slug); !ok {
		return
	}

	if s.manager == nil {
		writeJSON(w, http.StatusOK, metricsResponse{Status: string(process.StatusUnknown)})
		return
	}

	info, ok := s.manager.Get(slug)
	if !ok {
		writeJSON(w, http.StatusOK, metricsResponse{Status: string(process.StatusUnknown)})
		return
	}
	if info.Status != process.StatusRunning {
		writeJSON(w, http.StatusOK, metricsResponse{Status: string(info.Status)})
		return
	}

	stats, err := s.sampler.Sample(info.PID)
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
