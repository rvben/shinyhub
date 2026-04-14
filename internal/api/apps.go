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
	"github.com/rvben/shinyhost/internal/auth"
	"github.com/rvben/shinyhost/internal/db"
	"github.com/rvben/shinyhost/internal/deploy"
)

// slugRE enforces a safe, DNS-compatible slug format.
var slugRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

func (s *Server) handleListApps(w http.ResponseWriter, r *http.Request) {
	apps, err := s.store.ListApps()
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

	app, err := s.store.GetAppBySlug(slug)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	if s.manager == nil {
		http.Error(w, "process manager not available", http.StatusServiceUnavailable)
		return
	}

	// Accept multipart bundle upload (max 128 MB in memory)
	if err := r.ParseMultipartForm(128 << 20); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	file, _, err := r.FormFile("bundle")
	if err != nil {
		http.Error(w, "bundle file required", http.StatusBadRequest)
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

	app, err := s.store.GetAppBySlug(slug)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.Error(w, "internal server error", http.StatusInternalServerError)
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

	writeJSON(w, http.StatusOK, map[string]any{
		"pid":     result.PID,
		"port":    result.Port,
		"version": prev.Version,
	})
}

func (s *Server) handleRestartApp(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")

	app, err := s.store.GetAppBySlug(slug)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.Error(w, "internal server error", http.StatusInternalServerError)
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
