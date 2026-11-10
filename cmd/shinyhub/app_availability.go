package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	slugpkg "github.com/rvben/shinyhub/internal/slug"
)

type appAvailabilitySource interface {
	GetAppAvailability(context.Context, string) (*db.AppAvailability, error)
}

type availabilityRoutes interface {
	Draining() bool
	SyncedOnce() bool
	AvailabilityRoute(string, int64) (bool, bool)
}

type appAvailabilityResponse struct {
	Available bool   `json:"available"`
	Status    string `json:"status"`
	// Basis makes observed routing distinct from eligibility to activate. Neither
	// is a synthetic app check or a reservation of capacity for a later request.
	Basis  string `json:"basis,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// Keep slow anonymous probes from occupying the database connection pool. The
// budget is shared across every exposed slug on this server, with no wait queue.
const appAvailabilityConcurrency = 2

type fleetAvailabilitySource interface {
	ListAppAvailability(context.Context, []string, bool) ([]db.AppAvailability, error)
}

type availabilityMonitor struct {
	allowed      map[string]bool
	slugs        []string
	allowOptIn   bool
	fleetEnabled bool
	store        appAvailabilitySource
	routes       availabilityRoutes
	ready        <-chan struct{}
	lookups      chan struct{}
}

func newAvailabilityMonitor(slugs []string, allowOptIn, fleetEnabled bool, store appAvailabilitySource, routes availabilityRoutes, ready <-chan struct{}) *availabilityMonitor {
	m := &availabilityMonitor{allowed: make(map[string]bool, len(slugs)), slugs: append([]string(nil), slugs...), allowOptIn: allowOptIn, fleetEnabled: fleetEnabled, store: store, routes: routes, ready: ready, lookups: make(chan struct{}, appAvailabilityConcurrency)}
	for _, slug := range slugs {
		m.allowed[slug] = true
	}
	return m
}

func appAvailabilityHandler(slugs []string, store appAvailabilitySource, routes availabilityRoutes, ready <-chan struct{}) http.Handler {
	return http.HandlerFunc(newAvailabilityMonitor(slugs, false, false, store, routes, ready).app)
}

func availabilityMethod(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return true
	}
	w.Header().Set("Allow", "GET, HEAD")
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	return false
}

func (m *availabilityMonitor) acquire(w http.ResponseWriter, r *http.Request) bool {
	select {
	case m.lookups <- struct{}{}:
		return true
	default:
		w.Header().Set("Retry-After", "1")
		writeAppAvailability(w, r, http.StatusTooManyRequests, appAvailabilityResponse{Status: "unknown", Reason: "monitor_busy"})
		return false
	}
}

func (m *availabilityMonitor) platformAvailable() bool {
	select {
	case <-m.ready:
		return !m.routes.Draining() && m.routes.SyncedOnce()
	default:
		return false
	}
}

func (m *availabilityMonitor) evaluate(a db.AppAvailability) appAvailabilityResponse {
	if !m.platformAvailable() {
		return appAvailabilityResponse{Status: "unknown", Reason: "platform_unavailable"}
	}
	serving, eligible := m.routes.AvailabilityRoute(a.Slug, a.DeploymentID)
	return evaluateAppAvailability(a, serving, eligible)
}

func (m *availabilityMonitor) app(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	slug := r.PathValue("slug")
	if !slugpkg.Valid(slug) || (!m.allowed[slug] && !m.allowOptIn) {
		http.NotFound(w, r)
		return
	}
	if !availabilityMethod(w, r) || !m.acquire(w, r) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	a, err := func() (*db.AppAvailability, error) {
		defer func() { <-m.lookups }()
		return m.store.GetAppAvailability(ctx, slug)
	}()
	if errors.Is(err, db.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	result := appAvailabilityResponse{Status: "unknown", Reason: "state_unavailable"}
	if err == nil {
		if !m.allowed[slug] && a.Availability != "public" {
			http.NotFound(w, r)
			return
		}
		// Test sources and pre-existing implementations need not populate the slug.
		a.Slug = slug
		result = m.evaluate(*a)
	}
	code := http.StatusServiceUnavailable
	if result.Available {
		code = http.StatusOK
	}
	writeAppAvailability(w, r, code, result)
}

type fleetAvailabilityApp struct {
	Slug string `json:"slug"`
	appAvailabilityResponse
}

type fleetAvailabilityResponse struct {
	GeneratedAt time.Time              `json:"generated_at"`
	Apps        []fleetAvailabilityApp `json:"apps"`
}

func (m *availabilityMonitor) fleet(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !m.fleetEnabled {
		http.NotFound(w, r)
		return
	}
	if !availabilityMethod(w, r) || !m.acquire(w, r) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	// Hold the shared budget through snapshot evaluation, but release before
	// writing: a slow reader must never hold a monitoring/database slot.
	result, reason := func() (fleetAvailabilityResponse, string) {
		defer func() { <-m.lookups }()
		source, ok := m.store.(fleetAvailabilitySource)
		if !ok {
			return fleetAvailabilityResponse{}, "state_unavailable"
		}
		apps, err := source.ListAppAvailability(ctx, m.slugs, m.allowOptIn)
		if err != nil {
			return fleetAvailabilityResponse{}, "state_unavailable"
		}
		if !m.platformAvailable() {
			return fleetAvailabilityResponse{}, "platform_unavailable"
		}
		result := fleetAvailabilityResponse{Apps: make([]fleetAvailabilityApp, 0, len(apps))}
		for _, a := range apps {
			serving, eligible := m.routes.AvailabilityRoute(a.Slug, a.DeploymentID)
			result.Apps = append(result.Apps, fleetAvailabilityApp{Slug: a.Slug, appAvailabilityResponse: evaluateAppAvailability(a, serving, eligible)})
		}
		if ctx.Err() != nil {
			return fleetAvailabilityResponse{}, "state_unavailable"
		}
		if !m.platformAvailable() {
			return fleetAvailabilityResponse{}, "platform_unavailable"
		}
		result.GeneratedAt = time.Now().UTC()
		return result, ""
	}()
	if reason != "" {
		writeAppAvailability(w, r, http.StatusServiceUnavailable, appAvailabilityResponse{Status: "unknown", Reason: reason})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_ = json.NewEncoder(w).Encode(result)
	}
}

func writeAppAvailability(w http.ResponseWriter, r *http.Request, code int, result appAvailabilityResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if r.Method != http.MethodHead {
		_ = json.NewEncoder(w).Encode(result)
	}
}

func evaluateAppAvailability(a db.AppAvailability, serving, eligible bool) appAvailabilityResponse {
	result := appAvailabilityResponse{Status: "unknown", Reason: "state_unavailable"}
	if a.ServingBlocked {
		return appAvailabilityResponse{Status: "quarantined", Reason: "compatibility_blocked"}
	}
	// Expose only a fixed vocabulary, never arbitrary stored status or errors.
	switch a.Status {
	case "stopped":
		if a.OperatorStopped {
			return appAvailabilityResponse{Status: a.Status, Reason: "stopped_by_operator"}
		}
		return appAvailabilityResponse{Status: a.Status}
	case "deleting", "failed", "crashed":
		return appAvailabilityResponse{Status: a.Status}
	case "running", "degraded", "hibernated", "waking", "starting", "deploying", "hibernating":
		result.Status = a.Status
	default:
		return result
	}
	if a.DeploymentID == 0 {
		result.Reason = "no_serving_release"
		return result
	}
	if a.Status != "hibernated" && serving {
		return appAvailabilityResponse{Available: true, Status: a.Status, Basis: "serving_route"}
	}
	if a.ActivationBlocked {
		result.Reason = "activation_blocked"
		return result
	}
	// Normal sleep/wake transitions must not turn a healthy idle app into a
	// monitoring outage just because a real user requested its activation.
	// Eligibility belongs to the published pool, including while a replacement
	// is starting or deploying. Its candidate must not obscure that old pool.
	if a.Status == "hibernated" || a.Status == "waking" || a.Status == "hibernating" || eligible {
		return appAvailabilityResponse{Available: true, Status: a.Status, Basis: "activation_eligible"}
	}
	result.Reason = "no_serving_capacity"
	return result
}
