package proxy

import (
	"context"
	"log/slog"
	"time"

	"github.com/rvben/shinyhub/internal/db"
)

// ReporterInterval is how often the SessionReporter pushes replica session
// counts and last-activity to the replica_sessions table. Every instance
// refreshes its rows on each tick so their updated_at stays above the stale
// cutoff as long as the instance is alive.
//
// Three ticks without a refresh = stale (ReplicaSessionStaleCutoff below).
// Keeping the interval short (a few seconds) means the fleet view is
// near-real-time while the stale window remains a comfortable multiple of the
// interval so a transient DB hiccup does not immediately evict a live instance.
const ReporterInterval = 5 * time.Second

// ReplicaSessionStaleCutoff is the age at which a row in replica_sessions is
// considered stale (from a crashed or permanently-disconnected instance).
// Callers pass `now - ReplicaSessionStaleCutoff` as the cutoffEpoch to
// AppFleetLoad and ReapStaleReplicaSessions.
//
// The window is intentionally conservative: a stale row can only delay a
// scale-down or hibernation decision (false "still active"), never wrongly
// trigger one. Three ReporterIntervals ensures a live instance that misses a
// single tick (slow DB write, GC pause) is not evicted.
const ReplicaSessionStaleCutoff = 3 * ReporterInterval

// sessionStore is the subset of *db.Store that the SessionReporter needs.
// A narrow interface keeps the reporter unit-testable without a real DB.
type sessionStore interface {
	UpsertReplicaSessions(instanceID string, rows []db.ReplicaSessionRow) error
}

// SessionReporter periodically snapshots the local proxy's per-(slug, replica)
// active connection counts and last-activity timestamps and upserts them into
// the replica_sessions table so every other instance can include this
// instance's load in fleet-wide aggregation queries.
//
// In addition to the periodic tick it listens on the proxy's immediateFlush
// channel: when any app's local active count rises from 0 to >0 (a session
// just admitted on a previously-idle app), the reporter flushes that app's row
// immediately so the fleet view is updated within milliseconds, not up to one
// full reporterInterval later.
//
// SessionReporter must only be started in clustered deployments (Postgres DSN).
// Single-node deployments must never start it; they write no replica_sessions
// rows at all.
type SessionReporter struct {
	prx        *Proxy
	store      sessionStore
	instanceID string
	// flushCh is the same channel passed to prx.EnableImmediateFlush.
	flushCh <-chan string
}

// NewSessionReporter creates a SessionReporter. flushCh is the channel that
// the proxy signals on 0->active transitions; it must be the same channel
// passed to prx.EnableImmediateFlush. The channel must be buffered.
func NewSessionReporter(prx *Proxy, store sessionStore, instanceID string, flushCh chan string) *SessionReporter {
	return &SessionReporter{
		prx:        prx,
		store:      store,
		instanceID: instanceID,
		flushCh:    flushCh,
	}
}

// Run starts the reporter loop. It blocks until ctx is cancelled and returns
// when cleanup is done. Callers should run it in a goroutine tracked by a
// WaitGroup so shutdown can wait for the final flush.
func (r *SessionReporter) Run(ctx context.Context) {
	ticker := time.NewTicker(ReporterInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			// Final flush on shutdown so rows reflect the last known state.
			r.flush(nil)
			return
		case <-ticker.C:
			r.flush(nil)
		case slug := <-r.flushCh:
			// A slug just went 0->active; flush only that slug immediately,
			// then drain any additional slugs queued behind it (coalescing).
			r.flushSlug(slug)
		drainLoop:
			for {
				select {
				case s := <-r.flushCh:
					r.flushSlug(s)
				default:
					break drainLoop
				}
			}
		}
	}
}

// flush upserts every pool's session rows. When slugFilter is non-nil only
// the listed slugs are flushed; nil means all pools.
func (r *SessionReporter) flush(slugFilter map[string]struct{}) {
	rows := r.prx.snapshotSessions(slugFilter)
	if len(rows) == 0 {
		return
	}
	if err := r.store.UpsertReplicaSessions(r.instanceID, rows); err != nil {
		slog.Warn("session reporter: upsert failed", "err", err)
	}
}

// flushSlug flushes a single slug's rows immediately. Used for 0->active
// immediate flushes so the cost is one DB round-trip per transitioning app,
// not a full snapshot every time.
func (r *SessionReporter) flushSlug(slug string) {
	filter := map[string]struct{}{slug: {}}
	r.flush(filter)
}

// snapshotSessions builds the []db.ReplicaSessionRow batch for the current
// proxy state. When slugFilter is non-nil only pools whose slug appears in
// the map are included; nil includes all pools.
//
// The snapshot holds p.mu (read) only to copy pool metadata and replica
// pointers. activeConns reads happen after the lock is released; they are
// atomic and therefore safe. The resulting counts are best-effort (a request
// may land between snapshot and upsert), which is acceptable: the reporter is
// a near-real-time signal, not a synchronised ledger.
//
// Pools without an appID (zero) are skipped; they have not been wired for
// clustering and must not write junk rows into replica_sessions.
func (p *Proxy) snapshotSessions(slugFilter map[string]struct{}) []db.ReplicaSessionRow {
	type poolSnap struct {
		slug     string
		appID    int64
		replicas []*replicaBackend // safe to read activeConns after lock release
	}

	// Hold the read lock only to copy pool metadata and backend pointers.
	// *replicaBackend pointers remain valid after lock release because
	// RegisterReplica always installs a fresh pointer; the old one is never
	// freed while a reference exists.
	p.mu.RLock()
	snaps := make([]poolSnap, 0, len(p.pools))
	for slug, pool := range p.pools {
		if pool.appID == 0 {
			continue // not wired for clustering; skip
		}
		if slugFilter != nil {
			if _, ok := slugFilter[slug]; !ok {
				continue
			}
		}
		ps := poolSnap{slug: slug, appID: pool.appID}
		for _, rep := range pool.replicas {
			if rep != nil {
				ps.replicas = append(ps.replicas, rep)
			}
		}
		snaps = append(snaps, ps)
	}
	p.mu.RUnlock()

	if len(snaps) == 0 {
		return nil
	}

	// Read per-replica state without the lock: activeConns is atomic, and
	// lastSeen is per-slug under seenMu which LastSeen acquires briefly.
	//
	// LastActivityAgeSec is the seconds since this replica last saw activity,
	// computed as a skew-independent duration on local time. The DB upsert
	// subtracts this age from the DB-clock now so last_activity ends up on the
	// shared database clock, making it safely comparable across instances.
	now := time.Now()
	rows := make([]db.ReplicaSessionRow, 0, len(snaps)*2)
	for _, ps := range snaps {
		lastSeen := p.LastSeen(ps.slug)
		var ageSec int64
		if !lastSeen.IsZero() {
			if d := int64(now.Sub(lastSeen).Seconds()); d > 0 {
				ageSec = d
			}
		}
		for _, rep := range ps.replicas {
			rows = append(rows, db.ReplicaSessionRow{
				AppID:              ps.appID,
				Idx:                rep.index,
				Active:             rep.activeConns.Load(),
				LastActivityAgeSec: ageSec,
			})
		}
	}
	return rows
}
