package main

import (
	"context"
	"log/slog"
	"time"
)

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
