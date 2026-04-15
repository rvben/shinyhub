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
		if err := s.store.UpdateHibernateTimeout(slug, timeout); err != nil {
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

	updatedApp, err := s.store.GetAppBySlug(slug)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	writeJSON(w, http.StatusOK, updatedApp)
}

func (s *Server) handleRollbackApp(w http.ResponseWriter, r *http.Request) {
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
	// deployments are ordered newest-first; index 1 is the previous deploy.
	if len(deployments) < 2 {
		writeError(w, http.StatusConflict, "no previous deployment to roll back to")
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
		writeError(w, http.StatusBadRequest, "bad request")
		return
	}
	if req.UserID == 0 {
		writeError(w, http.StatusBadRequest, "user_id is required")
		return
	}
	if err := s.store.RevokeAppAccess(slug, req.UserID); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "member not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
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
	members, err := s.store.GetAppMembers(slug)
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
