package api

import (
	"net/http"
	"sort"

	"github.com/go-chi/chi/v5"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/schedulespec"
)

type runtimeCapability struct {
	Supported bool   `json:"supported"`
	Reason    string `json:"reason,omitempty"`
	Remedy    string `json:"remedy,omitempty"`
}

func capabilityResult(err error, remedy string) runtimeCapability {
	if err == nil {
		return runtimeCapability{Supported: true}
	}
	return runtimeCapability{Reason: err.Error(), Remedy: remedy}
}

// This read-only view uses the write-boundary validators. A proposed isolation
// mode can be checked without changing the app. A missing slug uses defaults.
func (s *Server) handleRuntimeCapabilities(w http.ResponseWriter, r *http.Request) {
	app := &db.App{Replicas: 1}
	if name := chi.URLParam(r, "slug"); name != "" {
		current, ok := s.requireManageApp(w, r, name)
		if !ok {
			return
		}
		copy := *current
		app = &copy
	}
	if mode := r.URL.Query().Get("isolation"); mode != "" {
		switch mode {
		case "multiplex", "grouped", "per_session":
			app.WorkerIsolation = mode
		default:
			writeError(w, http.StatusBadRequest, "isolation must be multiplex, grouped, or per_session")
			return
		}
	}
	mode := deploy.ResolveWorkerIsolation(app.WorkerIsolation, s.cfg.Runtime.DefaultWorkerIsolation)
	features := map[string]runtimeCapability{}
	for _, candidate := range []config.WorkerIsolationMode{config.IsolationMultiplex, config.IsolationGrouped, config.IsolationPerSession} {
		features[string(candidate)] = capabilityResult(config.ValidateWorkerSettings(config.WorkerSettings{Isolation: candidate, GroupedSize: 1, MaxWorkers: 1}, s.clustered, 0, 0), "Use multiplex on a clustered server, or a single-node server for grouped/per-session workers.")
	}
	producerErr := s.validateScheduleProducerTopology(app, schedulespec.DeployTriggerBundleChange, "none")
	features["deploy_producers"] = capabilityResult(producerErr, "Use local native tiers and multiplex isolation. If elastic workers ran before, stop the app and explicitly set multiplex to clear its orphan fence.")
	activationErr := s.validateScheduleActivationForApp(app, "roll")
	if activationErr == nil {
		activationErr = s.validateScheduleProducerTopology(app, schedulespec.DeployTriggerNever, "roll")
	}
	features["data_activation"] = capabilityResult(activationErr, "Use a single-node server with local native tiers and multiplex isolation for on_success=roll.")
	placement := app.PlacementMap()
	if len(placement) == 0 {
		placement = map[string]int{s.cfg.Runtime.DefaultTierName(): 1}
	}
	tiers := make([]string, 0, len(placement))
	requiresHostRuntime := false
	for tier := range placement {
		tiers = append(tiers, tier)
		runtimeName, ok := s.cfg.Runtime.RuntimeForTier(tier)
		if !ok && len(s.cfg.Runtime.Tiers) == 0 {
			runtimeName = s.cfg.Runtime.Mode
		}
		local := s.nodeForTier == nil || s.nodeForTier(tier) == ""
		if local && (runtimeName == "native" || runtimeName == "") {
			requiresHostRuntime = true
		}
	}
	sort.Strings(tiers)
	writeJSON(w, http.StatusOK, map[string]any{"isolation": mode, "clustered": s.clustered, "tiers": tiers, "features": features, "requires_host_runtime": requiresHostRuntime})
}
