package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/rvben/shinyhub/internal/db"
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

func appAvailabilityHandler(slugs []string, store appAvailabilitySource, routes availabilityRoutes, ready <-chan struct{}) http.Handler {
	lookups := make(chan struct{}, appAvailabilityConcurrency)
	allowed := make(map[string]bool, len(slugs))
	for _, slug := range slugs {
		allowed[slug] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		slug := r.PathValue("slug")
		if !allowed[slug] {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		select {
		case lookups <- struct{}{}:
		default:
			w.Header().Set("Retry-After", "1")
			writeAppAvailability(w, r, http.StatusTooManyRequests, appAvailabilityResponse{Status: "unknown", Reason: "monitor_busy"})
			return
		}
		a, err := func() (*db.AppAvailability, error) {
			defer func() { <-lookups }()
			return store.GetAppAvailability(ctx, slug)
		}()
		if errors.Is(err, db.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		result := appAvailabilityResponse{Status: "unknown", Reason: "state_unavailable"}
		if err == nil {
			select {
			case <-ready:
				if routes.Draining() || !routes.SyncedOnce() {
					result.Reason = "platform_unavailable"
				} else {
					serving, eligible := routes.AvailabilityRoute(slug, a.DeploymentID)
					result = evaluateAppAvailability(*a, serving, eligible)
				}
			default:
				result.Reason = "platform_unavailable"
			}
		}
		code := http.StatusServiceUnavailable
		if result.Available {
			code = http.StatusOK
		}
		writeAppAvailability(w, r, code, result)
	})
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
	case "stopped", "deleting", "failed", "crashed":
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
