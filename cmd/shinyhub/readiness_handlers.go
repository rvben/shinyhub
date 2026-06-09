package main

import (
	"context"
	"net/http"
	"time"
)

// pinger can check database reachability. *db.Store satisfies this interface.
type pinger interface {
	PingContext(ctx context.Context) error
}

// drainChecker reports whether the proxy is currently draining. *proxy.Proxy
// satisfies this interface.
type drainChecker interface {
	Draining() bool
	SyncedOnce() bool
}

// readyzHandler builds the /readyz HTTP handler from its dependencies.
// Extracted so the handler logic can be unit-tested without running the full
// server. The returned handler is mounted directly on the mux in runServe.
//
// The gate order is intentional:
//  1. Draining  - signals a graceful shutdown; no new traffic.
//  2. Starting  - listener not yet live (readyCh not closed).
//  3. DB ping   - database is unreachable.
//  4. Synced    - first pool synchronisation not yet complete (clustered only;
//     pre-seeded true on single-node so the gate is always satisfied there).
func readyzHandler(prx drainChecker, readyCh <-chan struct{}, db pinger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if prx.Draining() {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"ready":false,"reason":"draining"}`)) //nolint:errcheck
			return
		}
		select {
		case <-readyCh:
		default:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"ready":false,"reason":"starting"}`)) //nolint:errcheck
			return
		}
		pingCtx, cancel := context.WithTimeout(r.Context(), 1*time.Second)
		defer cancel()
		if err := db.PingContext(pingCtx); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"ready":false,"reason":"db"}`)) //nolint:errcheck
			return
		}
		if !prx.SyncedOnce() {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"ready":false,"reason":"syncing"}`)) //nolint:errcheck
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ready":true}`)) //nolint:errcheck
	}
}

// activezHandler builds the /activez HTTP handler from its dependencies.
// Extracted so the handler logic can be unit-tested without running the full
// server. Returns 200 {"active":true} when ownerAndReady() is true, else 503.
func activezHandler(ownerAndReady func() bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if ownerAndReady() {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"active":true}`)) //nolint:errcheck
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"active":false}`)) //nolint:errcheck
		}
	}
}
