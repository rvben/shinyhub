// internal/worker/registry.go
package worker

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sort"
	"sync"

	"github.com/rvben/shinyhub/internal/db"
)

// Registry is the control plane's view of joined workers: durable in the
// workers table, indexed in memory for routing. Multiple distinct-address
// workers may be up on one tier (real multi-worker capacity); the invariant is
// one up worker per (tier, advertise address), so a stale duplicate that rejoins
// at an endpoint a live worker already owns is superseded.
type Registry struct {
	store *db.Store
	// regMu serializes registrations so the multi-row store writes of one
	// Register (upsert + tier supersede) and its index update complete before
	// another begins. Without it, two concurrent registrations on the same tier
	// could each supersede the other's store row and leave both workers down in
	// the store while one is up in memory, wedging the tier after a restart.
	regMu sync.Mutex
	mu    sync.RWMutex
	byID  map[string]db.Worker
}

// RegisterParams is the data a joining worker supplies (node id is allocated by
// the registry, not chosen by the worker).
type RegisterParams struct {
	Name          string
	AdvertiseAddr string
	Tier          string
	Version       string
	Fingerprint   string
}

// NewRegistry constructs a registry and rebuilds its in-memory index from the
// workers table, so a control-plane restart re-adopts known workers.
func NewRegistry(store *db.Store) (*Registry, error) {
	r := &Registry{store: store, byID: map[string]db.Worker{}}
	if err := r.Refresh(); err != nil {
		return nil, err
	}
	return r, nil
}

// Refresh rebuilds the in-memory routing index from the workers table. An
// instance becoming the control-plane owner calls it so its routing decisions
// reflect every worker row the previous owner wrote before it died
// (registrations, heartbeats, supersedes, reaps) - not just the rows present when
// this instance booted. It is a full replace: workers added since boot appear and
// workers removed from the store disappear. Idempotent.
//
// It holds regMu (like Register/Heartbeat/Revoke), preserving the regMu-then-mu
// lock order, so the rebuild is a consistent snapshot against any in-flight
// registration on this instance.
func (r *Registry) Refresh() error {
	r.regMu.Lock()
	defer r.regMu.Unlock()
	ws, err := r.store.ListWorkers()
	if err != nil {
		return fmt.Errorf("refresh worker registry: %w", err)
	}
	next := make(map[string]db.Worker, len(ws))
	for _, w := range ws {
		next[w.NodeID] = *w
	}
	r.mu.Lock()
	r.byID = next
	r.mu.Unlock()
	return nil
}

// Register allocates a node id, persists the worker, and indexes it. Re-register
// with a known node id is handled by the caller passing that id via Reregister.
func (r *Registry) Register(p RegisterParams) (db.Worker, error) {
	nodeID, err := allocateNodeID()
	if err != nil {
		return db.Worker{}, err
	}
	w := db.Worker{
		NodeID:        nodeID,
		Name:          p.Name,
		AdvertiseAddr: p.AdvertiseAddr,
		Tier:          p.Tier,
		// "joining", not "up": a worker is not routable until its first heartbeat.
		// The agent sends that heartbeat only after its data-plane mTLS listener
		// has bound, so gating routing on the heartbeat (Heartbeat promotes
		// joining->up) guarantees an up worker is actually accepting connections.
		// Registering as up would advertise the worker for placement before its
		// listener exists, and a deploy in that window dials it -> connection
		// refused -> the replica fails to start.
		Status:      "joining",
		Fingerprint: p.Fingerprint,
		Version:     p.Version,
	}
	// Serialize the whole registration so concurrent same-tier joins cannot
	// interleave their store writes. regMu, not the routing RWMutex, guards the
	// database I/O, so routing lookups are never blocked on the database.
	r.regMu.Lock()
	defer r.regMu.Unlock()

	if err := r.store.UpsertWorker(w); err != nil {
		return db.Worker{}, err
	}
	// One up worker per (tier, advertise address): retire any other up worker at
	// this exact endpoint. A registration at an occupied endpoint means the
	// occupant is being replaced - the only way to reach this is an agent that
	// lost its persisted identity and rejoined under a fresh node id (a normal
	// restart re-adopts its old id and merely heartbeats, so it never re-registers).
	// The occupant is therefore stale: its old node id no longer matches the cert
	// the rejoined listener now presents, so routing to it would fail the mTLS
	// handshake. Retire it now and let routing fail closed (no live worker) until
	// the replacement's first heartbeat promotes it, rather than route to a stale
	// identity. The freshly registered worker stays "joining" until then.
	if err := r.store.SupersedeTierAddrWorkers(w.Tier, w.AdvertiseAddr, nodeID); err != nil {
		return db.Worker{}, err
	}

	// Take the routing lock only to mutate the in-memory index, mirroring the
	// store supersede above.
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, existing := range r.byID {
		if id == nodeID || existing.Tier != w.Tier || existing.AdvertiseAddr != w.AdvertiseAddr || existing.Status != "up" {
			continue
		}
		existing.Status = "down"
		r.byID[id] = existing
	}
	r.byID[nodeID] = w
	return w, nil
}

// MarkDown transitions a worker to down in both the durable store and the
// in-memory routing index, so a worker whose heartbeat went stale is excluded
// from routing without waiting for a control-plane restart to rebuild the
// index. Unknown node ids are a no-op in memory; the store write still runs.
//
// It holds regMu so the store write and the index update are one unit against a
// concurrent Refresh: without it, a Refresh whose ListWorkers snapshot predates
// this MarkDown could overwrite the index and resurrect the worker as "up".
func (r *Registry) MarkDown(nodeID string) error {
	r.regMu.Lock()
	defer r.regMu.Unlock()
	if err := r.store.SetWorkerStatus(nodeID, "down"); err != nil {
		return err
	}
	r.mu.Lock()
	if w, ok := r.byID[nodeID]; ok {
		w.Status = "down"
		r.byID[nodeID] = w
	}
	r.mu.Unlock()
	return nil
}

// Revoke administratively revokes a worker. It persists the revocation (which
// also marks the node down) and refreshes the in-memory index from the store so
// the node is excluded from routing immediately, without waiting for a
// control-plane restart. The row is kept (not deleted) so the revocation stays
// auditable. Returns db.ErrNotFound for an unknown node.
//
// It holds regMu for the same reason Heartbeat and Register do: a heartbeat
// performs a read-decide-write of the worker's status, so revoking without that
// lock could land between a heartbeat's read and its write and let the heartbeat
// resurrect the revoked node to up. Serializing closes that window.
func (r *Registry) Revoke(nodeID string) error {
	r.regMu.Lock()
	defer r.regMu.Unlock()

	if err := r.store.RevokeWorker(nodeID); err != nil {
		return err
	}
	w, err := r.store.GetWorker(nodeID)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.byID[nodeID] = *w
	r.mu.Unlock()
	return nil
}

// Forget drops a worker from the in-memory index. The monitor calls it after the
// store reaps a long-dead worker row so the fleet snapshot does not keep listing
// a node that no longer exists. Unknown node ids are a no-op. Forget never
// touches the store; the row is already gone.
//
// It holds regMu so the delete is serialized against a concurrent Refresh, whose
// older ListWorkers snapshot could otherwise reinsert the just-reaped worker.
func (r *Registry) Forget(nodeID string) {
	r.regMu.Lock()
	defer r.regMu.Unlock()
	r.mu.Lock()
	delete(r.byID, nodeID)
	r.mu.Unlock()
}

// Worker returns the indexed worker for a node id.
func (r *Registry) Worker(nodeID string) (db.Worker, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	w, ok := r.byID[nodeID]
	return w, ok
}

// WorkersForTier returns every up worker on a tier, sorted by node id so the
// order is deterministic (map iteration order is not). The returned slice is a
// fresh copy safe for the caller to retain.
func (r *Registry) WorkersForTier(tier string) []db.Worker {
	r.mu.RLock()
	out := make([]db.Worker, 0, len(r.byID))
	for _, w := range r.byID {
		if w.Tier == tier && w.Status == "up" {
			out = append(out, w)
		}
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].NodeID < out[j].NodeID })
	return out
}

// WorkerForTier returns a single up worker routing a tier, if any: the first of
// WorkersForTier (lowest node id), so single-worker routing is deterministic.
func (r *Registry) WorkerForTier(tier string) (db.Worker, bool) {
	ws := r.WorkersForTier(tier)
	if len(ws) == 0 {
		return db.Worker{}, false
	}
	return ws[0], true
}

// PlanPlacementForTier plans where to place count new replicas of slug across a
// tier's coexisting workers, spreading load deterministically. It returns one
// worker per replica, in assignment order; the returned slice is empty when no
// worker is up on the tier and may be shorter than count only in that case.
//
// Each pick chooses the worker hosting the fewest running replicas; ties break
// toward the worker hosting fewer of slug's own replicas (anti-affinity, so an
// app's replicas spread for HA), then toward the lowest node id (deterministic).
// Picks within one call fold into a running tally before the next pick, so a
// batch deployed concurrently spreads across workers instead of stacking every
// replica on the lowest node id (which a per-replica read of the same pre-deploy
// snapshot would do). If the load query fails it plans from a zero baseline
// rather than refusing to place, so a transient store hiccup does not block
// deploys.
func (r *Registry) PlanPlacementForTier(tier, slug string, count int) []db.Worker {
	ws := r.WorkersForTier(tier)
	if len(ws) == 0 || count <= 0 {
		return nil
	}
	loads, err := r.store.RunningReplicaLoadByWorker(slug)
	if err != nil {
		slog.Error("placement: load worker replica counts; planning from zero baseline", "tier", tier, "err", err)
		loads = nil
	}
	// tally is a mutable working copy of each candidate's load that each pick
	// increments, so successive picks in this batch see the replicas already
	// assigned. ws is sorted by node id, so iterating it keeps node id as the
	// final deterministic tiebreak.
	tally := make([]db.WorkerReplicaLoad, len(ws))
	for i, w := range ws {
		tally[i] = loads[w.NodeID]
	}
	out := make([]db.Worker, 0, count)
	for range count {
		best := 0
		for i := 1; i < len(ws); i++ {
			if tally[i].Total < tally[best].Total ||
				(tally[i].Total == tally[best].Total && tally[i].SameApp < tally[best].SameApp) {
				best = i
			}
		}
		out = append(out, ws[best])
		tally[best].Total++
		tally[best].SameApp++
	}
	return out
}

// Heartbeat records a worker's liveness and refreshes its trusted cert
// fingerprint (cert renewal) in both the store and the index. It keeps an up
// worker up, and promotes a not-up worker - a joining worker on its first
// heartbeat, or a down worker reaped for missed heartbeats or superseded while
// offline and now restarted under its old identity - to up only when no other up
// worker owns its (tier, advertise address). Gating the promotion on endpoint
// ownership keeps the one-up-worker-per-(tier,address) invariant: a worker the
// endpoint's live owner holds cannot resurrect itself alongside that owner. A
// joining worker becomes routable here, and the agent sends this first heartbeat
// only after its listener binds, so an up worker is always one that is listening.
func (r *Registry) Heartbeat(nodeID, fingerprint string) error {
	// Serialize against Register so the tier-ownership decision is made against a
	// stable set of up workers, mirroring how Register guards its supersede.
	r.regMu.Lock()
	defer r.regMu.Unlock()

	r.mu.RLock()
	cur, known := r.byID[nodeID]
	r.mu.RUnlock()
	if !known {
		return db.ErrNotFound
	}
	// A revoked worker can never be promoted back to up: reject the heartbeat so
	// a resurrected agent presenting a still-valid cert cannot rejoin within its
	// TTL. The worker API rejects revoked certs up front; this is defense in
	// depth against any path that reaches Heartbeat directly.
	if cur.Revoked() {
		return db.ErrNotFound
	}

	status := "up"
	if cur.Status != "up" {
		// Promote a joining worker (first heartbeat after registration) or a down
		// worker (reaped for missed heartbeats, or superseded while offline and now
		// restarted under its old identity) to up, unless another up worker already
		// owns its (tier, advertise address): a worker an endpoint's live owner
		// holds must stay out of routing. Distinct-address workers on the tier are
		// independent capacity and do not block the promotion. A joining worker
		// becoming up here is what makes it routable, and the agent sends this first
		// heartbeat only after its listener has bound, so an up worker is always one
		// that is actually accepting connections.
		for _, owner := range r.WorkersForTier(cur.Tier) {
			if owner.NodeID != nodeID && owner.AdvertiseAddr == cur.AdvertiseAddr {
				status = "down"
				break
			}
		}
	}

	if err := r.store.TouchWorkerHeartbeat(nodeID, fingerprint, status); err != nil {
		return err
	}
	r.mu.Lock()
	if w, ok := r.byID[nodeID]; ok {
		w.Fingerprint = fingerprint
		w.Status = status
		r.byID[nodeID] = w
	}
	r.mu.Unlock()
	return nil
}

func allocateNodeID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("allocate node id: %w", err)
	}
	return "node-" + hex.EncodeToString(b[:]), nil
}
