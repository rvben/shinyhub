package proxy

import "github.com/rvben/shinyhub/internal/config"

// poolIsElastic reports whether pool is in demand-driven (elastic) mode.
// A pool created by an existing SetPoolSize/SetPoolCap caller has a zero-value
// mode (""), which is intentionally treated as multiplex so the existing
// single-slot behaviour is byte-for-byte unchanged.
func poolIsElastic(pool *backendPool) bool {
	return pool.mode == config.IsolationGrouped || pool.mode == config.IsolationPerSession
}

// workerStates returns a snapshot of every worker in the elastic pool as a
// []workerState that the pure decide() function consumes. Callers must hold
// the pool lock (p.mu) for the duration of the call.
func (pool *backendPool) workerStates() []workerState {
	if len(pool.workers) == 0 {
		return nil
	}
	out := make([]workerState, 0, len(pool.workers))
	for _, w := range pool.workers {
		out = append(out, workerState{
			slotID:          w.slotID,
			assignedClients: w.assignedClients,
			status:          w.status,
		})
	}
	return out
}

// allocateSlotID returns the next monotonically increasing slot ID for this
// pool. IDs are never reused within a pool's lifetime, so a routing pin
// referencing a removed slot is always stale. Callers must hold the pool lock.
func (pool *backendPool) allocateSlotID() int {
	id := pool.nextSlotID
	pool.nextSlotID++
	return id
}

// addElasticWorker inserts a replicaBackend into the elastic workers map.
// r.slotID must already be set (via allocateSlotID). Callers must hold the
// pool lock.
func addElasticWorker(pool *backendPool, r *replicaBackend) {
	if pool.workers == nil {
		pool.workers = make(map[int]*replicaBackend)
	}
	pool.workers[r.slotID] = r
}

// removeElasticWorker removes the worker identified by slotID from the elastic
// workers map. It is a no-op for unknown slot IDs. Callers must hold the pool
// lock.
func removeElasticWorker(pool *backendPool, slotID int) {
	delete(pool.workers, slotID)
}
