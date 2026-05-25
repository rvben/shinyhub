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
	if err := r.store.UpsertWorker(w); err != nil {
		return db.Worker{}, err
	}
	r.mu.Lock()
	r.byID[nodeID] = w
	r.mu.Unlock()
	return w, nil
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

// UpdateFingerprint refreshes the trusted cert fingerprint (cert renewal) in
// both the store and the index.
func (r *Registry) UpdateFingerprint(nodeID, fingerprint string) error {
	if err := r.store.TouchWorkerHeartbeat(nodeID, fingerprint); err != nil {
		return err
	}
	r.mu.Lock()
	if w, ok := r.byID[nodeID]; ok {
		w.Fingerprint = fingerprint
		w.Status = "up"
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
