package proxy

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/rvben/shinyhub/internal/db"
)

// PoolSyncInterval is the default tick rate at which the pool syncer
// reconciles the proxy's backend pools against the DB replica table.
const PoolSyncInterval = 1500 * time.Millisecond

// RoutableSource is the minimal DB interface the pool syncer requires to list
// all routable replicas across all apps. *db.Store satisfies it.
type RoutableSource interface {
	ListRoutableReplicas() ([]db.RoutableReplica, error)
}

// TransportBuilder derives the per-replica HTTP transport from a DB row.
// *worker.ReplicaTransportBuilder satisfies it.
type TransportBuilder interface {
	TransportForReplica(r *db.Replica) (http.RoundTripper, error)
}

// PoolSyncer reconciles the proxy's backend pools against the authoritative
// DB replica table. It runs on every control-plane instance in a clustered
// deployment so standbys can serve off-host apps without relying on a local
// placement registry.
//
// Sync is diff-based: a slot whose endpoint_url and deployment_id have not
// changed since the last sync is left completely untouched, which preserves
// the wsReady cache and avoids flapping readiness on every tick.
type PoolSyncer struct {
	prx       *Proxy
	store     RoutableSource
	transport TransportBuilder
	interval  time.Duration
	log       *slog.Logger
}

// NewPoolSyncer constructs a syncer. interval is the reconcile tick period;
// pass 0 to use PoolSyncInterval.
func NewPoolSyncer(prx *Proxy, store RoutableSource, transport TransportBuilder, log *slog.Logger) *PoolSyncer {
	return &PoolSyncer{
		prx:       prx,
		store:     store,
		transport: transport,
		interval:  PoolSyncInterval,
		log:       log,
	}
}

// Run starts the reconcile loop and blocks until ctx is cancelled. Intended
// to be called in a dedicated goroutine.
func (s *PoolSyncer) Run(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.sync(ctx); err != nil {
				s.log.Warn("pool_sync_error", "err", err)
			}
		}
	}
}

// RunOnce performs exactly one sync pass. Useful in tests and the initial
// sync at startup.
func (s *PoolSyncer) RunOnce(ctx context.Context) {
	if err := s.sync(ctx); err != nil {
		s.log.Warn("pool_sync_error", "err", err)
	}
}

// SyncSlug performs a targeted, synchronous sync for a single slug. It calls
// ListRoutableReplicas (all replicas) and reconciles only the pool for the
// given slug. Used by the on-miss path so a freshly-started app is served
// before the next background tick.
//
// This issues a full-table query and filters client-side, which avoids adding
// a second query variant. The miss path is low-frequency (first request after
// a cold-start or scale-up), so the extra scan cost is negligible.
// A per-slug query is a future optimisation at large fleet scale.
func (s *PoolSyncer) SyncSlug(_ context.Context, slug string) {
	rows, err := s.store.ListRoutableReplicas()
	if err != nil {
		s.log.Warn("pool_sync_slug_error", "slug", slug, "err", err)
		return
	}
	// Filter to just the target slug and reconcile.
	var filtered []db.RoutableReplica
	for _, rr := range rows {
		if rr.Slug == slug {
			filtered = append(filtered, rr)
		}
	}
	s.reconcileSlugs(map[string][]db.RoutableReplica{slug: filtered})
}

// sync fetches all routable replicas and reconciles every slug.
func (s *PoolSyncer) sync(ctx context.Context) error {
	rows, err := s.store.ListRoutableReplicas()
	if err != nil {
		return err
	}
	// Group by slug.
	bySlug := make(map[string][]db.RoutableReplica, len(rows))
	for _, rr := range rows {
		bySlug[rr.Slug] = append(bySlug[rr.Slug], rr)
	}
	s.reconcileSlugs(bySlug)

	// Deregister any slug currently in the pool that has no routable replicas
	// in the DB. This handles replicas that went lost/stopped since the last tick.
	for slug := range s.prx.RegisteredSlugs() {
		if _, ok := bySlug[slug]; !ok {
			s.prx.Deregister(slug)
		}
	}

	s.prx.MarkSynced()
	return nil
}

// reconcileSlugs applies the desired state described by bySlug to the proxy
// pool table. Slugs absent from bySlug are not touched here (the caller
// controls scope; a full sync passes all slugs, a slug-scoped sync passes
// only one).
func (s *PoolSyncer) reconcileSlugs(bySlug map[string][]db.RoutableReplica) {
	for slug, rows := range bySlug {
		s.reconcileSlug(slug, rows)
	}
}

// reconcileSlug reconciles the pool for a single slug against the given rows.
// When rows is empty, the pool is fully deregistered (replica has no routable
// replicas).
func (s *PoolSyncer) reconcileSlug(slug string, rows []db.RoutableReplica) {
	if len(rows) == 0 {
		s.prx.Deregister(slug)
		return
	}

	// Determine pool size: maxIdx+1 across all routable rows.
	// All rows for a slug share the same appID and max_sessions_per_replica
	// (they come from the same parent app row in the JOIN), so taking both
	// values from the first row encountered is correct.
	var maxIdx int
	var appID int64
	var maxSess int
	for _, rr := range rows {
		r := rr.Replica
		if r.Index > maxIdx {
			maxIdx = r.Index
		}
		if appID == 0 {
			appID = r.AppID
			maxSess = rr.AppMaxSessionsPerRepl
		}
	}
	poolSize := maxIdx + 1

	// Configure pool metadata. SetPoolSize creates the pool if absent; it
	// is safe to call when the pool already exists (idempotent grow).
	s.prx.SetPoolSize(slug, poolSize)
	s.prx.SetPoolAppID(slug, appID)
	s.prx.SetPoolCap(slug, maxSess)

	// Build a quick index of the desired state for O(1) lookup.
	type desired struct {
		endpointURL  string
		deploymentID int64
		desiredState string
		rr           db.RoutableReplica
	}
	want := make(map[int]desired, len(rows))
	for _, rr := range rows {
		r := rr.Replica
		var depID int64
		if r.DeploymentID != nil {
			depID = *r.DeploymentID
		}
		want[r.Index] = desired{
			endpointURL:  r.EndpointURL,
			deploymentID: depID,
			desiredState: r.DesiredState,
			rr:           rr,
		}
	}

	// Reconcile each slot up to poolSize.
	for idx := 0; idx < poolSize; idx++ {
		d, ok := want[idx]
		if !ok {
			// This slot is not in the routable set. If a backend is registered,
			// deregister it (the replica went lost/stopped). We do not know its
			// current target URL here, so read it under the lock via
			// ReplicaTargetURL and then call DeregisterReplicaIfTarget.
			if cur := s.prx.ReplicaTargetURL(slug, idx); cur != "" {
				s.prx.DeregisterReplicaIfTarget(slug, idx, cur)
			}
			continue
		}

		// Diff: only re-register when endpoint or deployment changed. The two
		// reads (ReplicaTargetURL + ReplicaDeploymentID) and the subsequent
		// RegisterReplica are not atomic across lock acquisitions. A concurrent
		// deploy could update the slot between the read and the register. In
		// that case the syncer's stale read causes at worst a harmless extra
		// re-register (which clears wsReady), never incorrect routing: the
		// deploy path always registers the new URL before persisting the row,
		// so the syncer's next tick will see the deployed state and stabilise.
		curURL := s.prx.ReplicaTargetURL(slug, idx)
		curDepID := s.prx.ReplicaDeploymentID(slug, idx)
		if curURL == d.endpointURL && curDepID == d.deploymentID {
			// Slot is unchanged - leave wsReady intact, just update drain.
			s.syncDrainState(slug, idx, d.desiredState)
			continue
		}

		// Build the per-replica transport from the DB row.
		tr, err := s.transport.TransportForReplica(d.rr.Replica)
		if err != nil {
			s.log.Warn("pool_sync_transport_error",
				"slug", slug, "index", idx, "err", err)
			continue
		}

		// RegisterReplica clears wsReady for this slug; the app must re-prove
		// readiness. This is intentional: a new endpoint is a new connection.
		if err := s.prx.RegisterReplica(slug, idx, d.endpointURL, tr, d.deploymentID); err != nil {
			s.log.Warn("pool_sync_register_error",
				"slug", slug, "index", idx, "err", err)
			continue
		}
		s.syncDrainState(slug, idx, d.desiredState)
	}
}

// syncDrainState applies the desired_state to a slot's drain flag.
func (s *PoolSyncer) syncDrainState(slug string, idx int, desiredState string) {
	if desiredState == "draining" {
		s.prx.DrainReplica(slug, idx)
	} else {
		// Only undrain if currently draining, to avoid unnecessary lock traffic.
		if s.prx.IsDraining(slug, idx) {
			s.prx.UndrainReplica(slug, idx)
		}
	}
}
