package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
)

// Repair never delays readiness or retries the index automatically. This
// budget also leaves room to deliver a response before the HTTP write deadline.
const pythonInstrumentationRepairTimeout = 90 * time.Second

var errPythonInstrumentationBusy = errors.New("instrumentation preparation is already running or the deployment changed")

func (s *Server) pythonInstrumentationDeployment(app *db.App) (*db.Deployment, string, error) {
	if !s.cfg.Tracing.Enabled || s.manager == nil || app.Status == "deleting" {
		return nil, "", nil
	}
	hostDeps := false
	for _, tier := range s.tiersForApp(app) {
		if s.manager.HostPreparesDepsFor(tier) {
			hostDeps = true
			break
		}
	}
	if !hostDeps {
		return nil, "", nil
	}
	dep, err := s.store.GetServingDeployment(app.ID)
	if errors.Is(err, db.ErrNotFound) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	if dep.Status != db.DeploymentSucceeded || dep.BundleDir == "" {
		return nil, "", nil
	}
	state, err := deploy.NativePythonInstrumentationState(dep.BundleDir,
		s.manager.AutoInstrumentAppsDefault(), s.manager.AutoInstrumentExtraPackages())
	if state == "not_prepared" {
		if _, building := s.pythonInstrumentationBuilds.Load(dep.ID); building {
			state = "preparing"
		}
	}
	return dep, state, err
}

// ReconcilePythonInstrumentation runs one pass in the ownership epoch, after
// recovery opens admission. It neither restarts workloads nor holds app locks
// while building. An operator can retry a failed preparation from Traces.
func (s *Server) ReconcilePythonInstrumentation(ctx context.Context) {
	if !s.cfg.Tracing.Enabled {
		return
	}
	apps, err := s.store.ListAppsLean(0, 0)
	if err != nil {
		slog.Warn("list apps for Python instrumentation preparation", "err", err)
		return
	}
	for _, app := range apps {
		if ctx.Err() != nil {
			return
		}
		dep, state, err := s.pythonInstrumentationDeployment(app)
		if err == nil && state == "not_prepared" {
			err = s.preparePythonInstrumentation(ctx, app, dep)
		}
		if err != nil && !errors.Is(err, errPythonInstrumentationBusy) && ctx.Err() == nil {
			slog.Warn("prepare app Python instrumentation", "slug", app.Slug, "err", err)
		}
	}
}

func (s *Server) preparePythonInstrumentation(ctx context.Context, app *db.App, dep *db.Deployment) error {
	if _, busy := s.pythonInstrumentationBuilds.LoadOrStore(dep.ID, struct{}{}); busy {
		return errPythonInstrumentationBusy
	}
	defer s.pythonInstrumentationBuilds.Delete(dep.ID)
	ctx, cancel := context.WithTimeout(ctx, pythonInstrumentationRepairTimeout)
	defer cancel()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	confirmed := false
	err := s.preparePythonOverlay(ctx,
		deploy.Params{Slug: app.Slug, BundleDir: dep.BundleDir, Manager: s.manager},
		func(commit func() error) error {
			release, ok := s.TryAcquireAppOperation(app.Slug)
			if !ok {
				return errPythonInstrumentationBusy
			}
			defer release()
			latest, err := s.store.GetAppByID(app.ID)
			if errors.Is(err, db.ErrNotFound) {
				return errPythonInstrumentationBusy
			}
			if err != nil {
				return err
			}
			current, state, err := s.pythonInstrumentationDeployment(latest)
			if err != nil {
				return err
			}
			if current == nil || current.ID != dep.ID || current.BundleDir != dep.BundleDir {
				return errPythonInstrumentationBusy
			}
			switch state {
			case "ready":
				confirmed = true
				return nil
			case "preparing":
				err := commit()
				confirmed = err == nil
				return err
			default:
				return errPythonInstrumentationBusy
			}
		})
	if err != nil || confirmed {
		return err
	}
	current, state, err := s.pythonInstrumentationDeployment(app)
	if err == nil && (current == nil || current.ID != dep.ID || state != "ready") {
		return errPythonInstrumentationBusy
	}
	return err
}

func (s *Server) handlePreparePythonInstrumentation(w http.ResponseWriter, r *http.Request) {
	app, ok := s.requireManageApp(w, r, chi.URLParam(r, "slug"))
	if !ok {
		return
	}
	dep, state, err := s.pythonInstrumentationDeployment(app)
	if err != nil {
		writeInternal(w, r, "could not inspect Python instrumentation", err)
		return
	}
	switch state {
	case "ready":
		w.WriteHeader(http.StatusNoContent)
		return
	case "preparing":
		writeError(w, http.StatusConflict, "Python instrumentation preparation is already running")
		return
	case "not_prepared":
	default:
		writeError(w, http.StatusConflict, "this app has no eligible native Python deployment")
		return
	}
	if err := s.preparePythonInstrumentation(r.Context(), app, dep); errors.Is(err, errPythonInstrumentationBusy) {
		writeError(w, http.StatusConflict, "Python instrumentation preparation could not complete while the app changed; retry")
		return
	} else if err != nil {
		writeInternal(w, r, "could not prepare Python instrumentation; check the server logs and retry", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
