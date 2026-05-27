// internal/worker/registry.go
package worker

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"

	"github.com/rvben/shinyhub/internal/db"
)

// Registry is the control plane's view of joined workers: durable in the
// workers table, indexed in memory for routing. Single-worker-per-tier in this
// build (the last registrant for a tier wins the routing slot); the schema and
// index do not preclude multi-worker selection later.
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
	ws, err := store.ListWorkers()
	if err != nil {
		return nil, fmt.Errorf("load workers: %w", err)
	}
	for _, w := range ws {
		r.byID[w.NodeID] = *w
	}
	return r, nil
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
		Status:        "up",
		Fingerprint:   p.Fingerprint,
		Version:       p.Version,
	}
	// Serialize the whole registration so concurrent same-tier joins cannot
	// interleave their store writes. regMu, not the routing RWMutex, guards the
	// database I/O, so routing lookups are never blocked on the database.
	r.regMu.Lock()
	defer r.regMu.Unlock()

	if err := r.store.UpsertWorker(w); err != nil {
		return db.Worker{}, err
	}
	// Single worker per tier: the newest registrant wins the routing slot. Retire
	// any other up worker on this tier so routing never resolves a superseded
	// node, e.g. a worker that restarted under a fresh node id (its advertise
	// address and certificate identity are stale and a dial would fail).
	if err := r.store.SupersedeTierWorkers(w.Tier, nodeID); err != nil {
		return db.Worker{}, err
	}

	// Take the routing lock only to mutate the in-memory index, mirroring the
	// store supersede above.
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, existing := range r.byID {
		if id == nodeID || existing.Tier != w.Tier || existing.Status != "up" {
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
func (r *Registry) MarkDown(nodeID string) error {
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
func (r *Registry) Revoke(nodeID string) error {
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

// Workers returns a snapshot of every known worker (including down and revoked
// nodes) for the admin fleet view. The slice is a copy; mutating it does not
// affect the registry's index.
func (r *Registry) Workers() []db.Worker {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]db.Worker, 0, len(r.byID))
	for _, w := range r.byID {
		out = append(out, w)
	}
	return out
}

// Worker returns the indexed worker for a node id.
func (r *Registry) Worker(nodeID string) (db.Worker, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	w, ok := r.byID[nodeID]
	return w, ok
}

// WorkerForTier returns the (single) up worker routing a tier, if any.
func (r *Registry) WorkerForTier(tier string) (db.Worker, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, w := range r.byID {
		if w.Tier == tier && w.Status == "up" {
			return w, true
		}
	}
	return db.Worker{}, false
}

// Heartbeat records a worker's liveness and refreshes its trusted cert
// fingerprint (cert renewal) in both the store and the index. It keeps an up
// worker up, and promotes a down worker (one reaped for missed heartbeats, or
// superseded while offline and now restarted under its old identity) back to up
// only when its tier slot is free. Gating the promotion on tier ownership keeps
// the single-up-worker-per-tier invariant: a replaced worker cannot resurrect
// itself alongside the newer owner and make routing depend on map iteration
// order.
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
		if owner, ok := r.WorkerForTier(cur.Tier); ok && owner.NodeID != nodeID {
			status = "down" // a newer worker owns the tier; stay superseded
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
