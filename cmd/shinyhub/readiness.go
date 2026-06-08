package main

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"
)

// ownerAndReadyPredicate returns the gate the mutation endpoints use: true only
// when this instance is the elected owner AND its worker routing index has been
// refreshed for the current ownership span. Both are required - the elector
// reports ownership before ownerWork refreshes the index, so gating on ownership
// alone would admit owner-only mutations (deploy/placement, worker register/
// heartbeat) against a stale index. Kept as a single constructor so the wiring
// cannot silently drop the readiness half.
func ownerAndReadyPredicate(isOwner func() bool, ready *atomic.Bool) func() bool {
	return func() bool { return isOwner() && ready.Load() }
}

// registryRefreshBackoff is how long ownerWork waits between failed registry
// refreshes before retrying, while the readiness gate stays closed.
const registryRefreshBackoff = 2 * time.Second

// registryRefresher rebuilds an in-memory routing index from the authoritative
// store. *worker.Registry satisfies it; the interface keeps refreshUntilReady
// unit-testable.
type registryRefresher interface {
	Refresh() error
}

// refreshUntilReady retries reg.Refresh() with the given backoff until it
// succeeds (returns true) or octx is cancelled (returns false, i.e. ownership was
// lost before a fresh index). A failing refresh keeps the readiness gate closed,
// so owner work never runs on a stale routing index. reg must be non-nil; the
// caller skips the call entirely when worker hosting is disabled.
func refreshUntilReady(octx context.Context, reg registryRefresher, backoff time.Duration, log *slog.Logger) bool {
	for {
		if err := reg.Refresh(); err == nil {
			return true
		} else {
			log.Error("worker registry refresh on acquire; not ready until it succeeds", "err", err)
		}
		select {
		case <-octx.Done():
			return false
		case <-time.After(backoff):
		}
	}
}
