package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
)

// deployPreflightRequest describes a deploy that has not happened yet: the
// bundle manifest that would be uploaded, the app type the bundle detects as,
// and the fleet-layer settings a `fleet apply` would PATCH after the deploy.
type deployPreflightRequest struct {
	Manifest string                   `json:"manifest"`
	AppType  string                   `json:"app_type"`
	Settings *deployPreflightSettings `json:"settings"`
}

// deployPreflightSettings is the subset of fleet [app.config] the server
// validates against its own policy (replica and autoscale ceilings). Keys the
// server has no policy for are rejected by the strict decoder rather than
// silently accepted, so a client never mistakes "ignored" for "checked".
type deployPreflightSettings struct {
	Replicas  *int `json:"replicas"`
	Autoscale *struct {
		Enabled     *bool    `json:"enabled"`
		MinReplicas *int     `json:"min_replicas"`
		MaxReplicas *int     `json:"max_replicas"`
		Target      *float64 `json:"target"`
	} `json:"autoscale"`
}

func (p *deployPreflightSettings) appSettings() deploy.AppSettings {
	var m deploy.AppSettings
	if p == nil {
		return m
	}
	m.Replicas = p.Replicas
	if p.Autoscale != nil {
		as := &deploy.AutoscaleSettings{Enabled: p.Autoscale.Enabled}
		if p.Autoscale.MinReplicas != nil {
			as.MinReplicas = *p.Autoscale.MinReplicas
		}
		if p.Autoscale.MaxReplicas != nil {
			as.MaxReplicas = *p.Autoscale.MaxReplicas
		}
		if p.Autoscale.Target != nil {
			as.Target = *p.Autoscale.Target
		}
		m.Autoscale = as
	}
	return m
}

// deployPreflightProblem is one rejection the deploy would produce. Stage
// "deploy" carries the exact message POST /api/apps/{slug}/deploy would return;
// stage "settings" is the post-deploy fleet config PATCH judged against the
// same server policy.
type deployPreflightProblem struct {
	Stage   string `json:"stage"`
	Message string `json:"message"`
}

type deployPreflightResponse struct {
	Valid     bool                     `json:"valid"`
	Isolation string                   `json:"isolation"`
	Problems  []deployPreflightProblem `json:"problems"`
}

// rOnFargateDeployMsg is the 400 body for an R bundle placed on a Fargate
// tier. The reference Fargate runner image is Python-only and the runtime does
// not prepare dependencies on the host, so the task would start and fail at
// exec with no R interpreter or restored renv.
const rOnFargateDeployMsg = "R apps are not supported on Fargate tiers: the Fargate runner image is Python-only. Place this app on a native or docker tier."

// handleDeployPreflight answers "would this deploy be accepted?" without
// changing anything. It runs the validators the deploy handler runs, in the
// same order and against the same stored app, so a rejection here is the
// rejection the deploy would have produced. A slug that does not exist yet is
// judged as the app `POST /api/apps` would create, so a fleet plan can vet a
// bundle before the app is created.
func (s *Server) handleDeployPreflight(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	slug := chi.URLParam(r, "slug")
	app, err := s.loadApp(slug)
	switch {
	case err == nil:
		existing, ok := s.requireManageApp(w, r, slug)
		if !ok {
			return
		}
		app = existing
	case errors.Is(err, db.ErrNotFound):
		if !canCreateApps(u) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		app = &db.App{Slug: slug, Replicas: 1}
		if s.cfg.Runtime.DefaultReplicas > 1 {
			app.Replicas = s.cfg.Runtime.DefaultReplicas
		}
	default:
		slog.Error("deploy preflight: load app", "slug", slug, "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	var req deployPreflightRequest
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	reply, err := s.deployPreflight(app, req)
	if err != nil {
		slog.Error("deploy preflight failed", "slug", slug, "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, reply)
}

// deployPreflight is the handler body without HTTP concerns. Validation stops
// at the first problem, exactly as the deploy stops at its first rejection,
// so the reply never lists a settings problem the deploy would never reach.
func (s *Server) deployPreflight(app *db.App, req deployPreflightRequest) (deployPreflightResponse, error) {
	reply := deployPreflightResponse{Problems: []deployPreflightProblem{}}
	projected := *app

	var manifest *deploy.Manifest
	if req.Manifest != "" {
		m, err := deploy.ParseManifest([]byte(req.Manifest))
		if err != nil {
			reply.Problems = append(reply.Problems, deployPreflightProblem{Stage: "deploy", Message: "shinyhub.toml: " + err.Error()})
			reply.Isolation = deploy.ResolveWorkerIsolation(app.WorkerIsolation, s.cfg.Runtime.DefaultWorkerIsolation)
			return reply, nil
		}
		manifest = m
	}

	deployProblem := func() (string, error) {
		if manifest != nil {
			if ve := s.validateManifestForServer(app, manifest.App); ve != nil {
				return ve.Error(), nil
			}
			if ve := s.validateManifestActivationTopology(app, manifest); ve != nil {
				return ve.Error(), nil
			}
		}
		if req.AppType == "r" && s.appTargetsFargate(app) {
			return rOnFargateDeployMsg, nil
		}
		tier, blocked, err := s.ephemeralDataDeployBlock(app, manifestCommand(manifest))
		if err != nil {
			return "", err
		}
		if blocked {
			return ephemeralDataDeployMsg(tier), nil
		}
		// Colocated shared data is judged on the stored app, as the deploy
		// does: a consumer on a remote node whose shared source lives on
		// another node is refused before the pool is touched. The check only
		// reads placement and grants; a new slug has no grants yet.
		if err := s.checkColocatedShared(app.ID, s.tiersForApp(app)); err != nil {
			return err.Error(), nil
		}
		return "", nil
	}
	msg, err := deployProblem()
	if err != nil {
		return reply, err
	}
	if manifest != nil {
		projectManifestSettings(&projected, manifest.App)
	}
	if msg != "" {
		reply.Problems = append(reply.Problems, deployPreflightProblem{Stage: "deploy", Message: msg})
	} else if req.Settings != nil {
		if ve := s.validateManifestForServer(&projected, req.Settings.appSettings()); ve != nil {
			reply.Problems = append(reply.Problems, deployPreflightProblem{Stage: "settings", Message: ve.Error()})
		}
	}
	reply.Valid = len(reply.Problems) == 0
	reply.Isolation = deploy.ResolveWorkerIsolation(projected.WorkerIsolation, s.cfg.Runtime.DefaultWorkerIsolation)
	return reply, nil
}

// projectManifestSettings overlays the [app] settings a deploy would persist
// onto app, mirroring applyManifestAppSettings without writing anything. Only
// the columns the server-policy validators read are projected.
func projectManifestSettings(app *db.App, m deploy.AppSettings) {
	if m.Replicas != nil {
		app.Replicas = *m.Replicas
	}
	if m.MemoryLimitMB != nil {
		app.MemoryLimitMB = m.MemoryLimitMB
	}
	if m.CPUQuotaPercent != nil {
		app.CPUQuotaPercent = m.CPUQuotaPercent
	}
	if m.UsageIdentityMode != nil {
		app.UsageIdentityMode = m.UsageIdentityMode
	}
	if m.Worker != nil {
		if m.Worker.Isolation != nil {
			app.WorkerIsolation = *m.Worker.Isolation
		}
		if m.Worker.GroupedSize != nil {
			app.WorkerGroupedSize = *m.Worker.GroupedSize
		}
		if m.Worker.MaxWorkers != nil {
			app.WorkerMaxWorkers = *m.Worker.MaxWorkers
		}
		if m.Worker.WarmSpares != nil {
			app.WorkerWarmSpares = *m.Worker.WarmSpares
		}
		if m.Worker.MaxSessionLifetimeSecs != nil {
			app.WorkerMaxSessionLifetimeSecs = *m.Worker.MaxSessionLifetimeSecs
		}
	}
}
