package proxy

import (
	"log/slog"
	"time"
)

// fleetStore is the subset of *db.Store that FleetSignal needs.
// A narrow interface keeps FleetSignal unit-testable without a real database.
type fleetStore interface {
	AppFleetLoad(appID int64, staleWindowSec int64, excludeInstanceID string) (active []int64, idleSinceSec int64, err error)
}

// FleetSignal is an autoscale.Signal adapter that overrides ReplicaSessionCounts
// to return the fleet-wide sum of active sessions from the replica_sessions
// table. All other Signal methods are delegated to the underlying *Proxy
// unchanged.
//
// FleetSignal is only wired in clustered deployments (Postgres DSN). Single-node
// deployments pass the *Proxy directly to the autoscaler so the local in-memory
// count is used exactly as before.
type FleetSignal struct {
	prx   *Proxy
	store fleetStore
	log   *slog.Logger
}

// NewFleetSignal constructs a FleetSignal backed by prx for slug->appID
// resolution and store for fleet-load aggregation. log may be nil, in which
// case the default logger is used.
func NewFleetSignal(prx *Proxy, store fleetStore, log *slog.Logger) *FleetSignal {
	if log == nil {
		log = slog.Default()
	}
	return &FleetSignal{prx: prx, store: store, log: log}
}

// fleetReplicaSessionCounts returns the per-index fleet-wide sum of active
// sessions for slug from the replica_sessions table.
//
// It resolves slug's numeric app ID from the proxy pool. If the pool is not
// registered or has no app ID set, or if no non-stale rows exist, it returns
// an empty/nil slice. The autoscaler's existing len(counts)==0 early-return
// then fires, so the controller takes no action rather than over-scaling.
func (f *FleetSignal) fleetReplicaSessionCounts(slug string) []int64 {
	appID, ok := f.prx.appIDForSlug(slug)
	if !ok {
		return nil
	}
	staleWindowSec := int64(ReplicaSessionStaleCutoff.Seconds())
	active, _, err := f.store.AppFleetLoad(appID, staleWindowSec, "")
	if err != nil {
		// DB errors are transient; treat as no signal (no action) rather than
		// crashing or mis-scaling. Log at warn so a persistent failure is
		// visible to operators without flooding normal operation logs.
		f.log.Warn("fleet session signal: AppFleetLoad failed", "slug", slug, "err", err)
		return nil
	}
	return active
}

// ReplicaSessionCounts satisfies the autoscale.Signal interface by returning
// the fleet-wide count. The autoscaler always calls this method; the name
// matches the interface so FleetSignal can be used wherever *Proxy is used
// today as a Signal.
//
// Callers that need the exact local in-memory count (UI/app-detail API,
// admission, scale-drain) use *Proxy.ReplicaSessionCounts directly; the
// FleetSignal adapter is only passed to the autoscaler.
func (f *FleetSignal) ReplicaSessionCounts(slug string) []int64 {
	return f.fleetReplicaSessionCounts(slug)
}

// RejectsByReason delegates to the underlying proxy unchanged. The rejection
// rollup is per-instance and does not need fleet aggregation for autoscale
// decisions: a single instance seeing pool-saturated rejects is sufficient
// evidence to scale up.
func (f *FleetSignal) RejectsByReason(slug string, d time.Duration) map[RejectReason]uint64 {
	return f.prx.RejectsByReason(slug, d)
}
