package proxy

import (
	"fmt"

	"github.com/rvben/shinyhub/internal/config"
)

// StageGroupedGeneration reserves a unique slot for a readiness-tested worker.
// The candidate is absent from workers and cannot receive client assignments.
// Slot IDs are never reused, including after abort, so old disconnect timers
// and queued spawn callbacks cannot address a replacement worker.
func (p *Proxy) StageGroupedGeneration(slug string, deploymentID int64) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	pool := p.pools[slug]
	if pool == nil || pool.mode != config.IsolationGrouped || deploymentID <= 0 {
		return 0, fmt.Errorf("stage %s: a grouped pool and positive deployment ID are required", slug)
	}
	if deploymentID == pool.activeDeploymentID || len(pool.candidates) != 0 || len(pool.drainingGenerations) != 0 {
		return 0, fmt.Errorf("stage %s: another generation is active, staged, or draining", slug)
	}
	if pool.candidates == nil {
		pool.candidates = make(map[int64][]*replicaBackend)
	}
	slot := pool.allocateSlotID()
	pool.candidates[deploymentID] = make([]*replicaBackend, slot+1)
	return slot, nil
}

// activateGroupedGenerationLocked keeps old client bindings addressable while
// excluding their workers from placement for new clients. p.mu must be held.
func (p *Proxy) activateGroupedGenerationLocked(pool *backendPool, deploymentID int64, slots []*replicaBackend) (int64, error) {
	// A grouped candidate has exactly one worker, at the last reserved index.
	if len(slots) == 0 || slots[len(slots)-1] == nil {
		return 0, fmt.Errorf("activate grouped deployment %d: candidate is not ready", deploymentID)
	}
	previous := pool.activeDeploymentID
	old := make([]*replicaBackend, 0, len(pool.workers))
	for _, worker := range pool.workers {
		worker.draining.Store(true)
		old = append(old, worker)
		if previous == 0 && worker.deploymentID != 0 {
			previous = worker.deploymentID
		}
	}
	if previous != 0 {
		if pool.drainingGenerations == nil {
			pool.drainingGenerations = make(map[int64][]*replicaBackend)
		}
		pool.drainingGenerations[previous] = old
	}
	worker := slots[len(slots)-1]
	worker.slotID = worker.index
	worker.status = workerRunning
	worker.handoffReady = true
	pool.workers[worker.slotID] = worker
	pool.activeDeploymentID = deploymentID
	delete(pool.candidates, deploymentID)
	return previous, nil
}

// ElasticSlotCanStart fences callbacks queued before a grouped cutover.
func (p *Proxy) ElasticSlotCanStart(slug string, slotID int) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	pool := p.pools[slug]
	if pool == nil {
		return false
	}
	if pool.mode != config.IsolationGrouped {
		return true
	}
	worker := pool.workers[slotID]
	return worker != nil && !worker.draining.Load()
}

// removeGroupedGenerationLocked removes bindings as well as routes. Open
// requests retain their backend pointer until they close; late callbacks are
// fenced by the unique slot ID and clientSlot identity.
func (p *Proxy) removeGroupedGenerationLocked(slug string, pool *backendPool, deploymentID int64) {
	if pool.mode != config.IsolationGrouped {
		return
	}
	for _, worker := range pool.drainingGenerations[deploymentID] {
		if p.cancelElasticLifetime != nil {
			p.cancelElasticLifetime(slug, worker.slotID)
		}
		delete(pool.workers, worker.slotID)
		for cid, cs := range p.clients[slug] {
			if cs.slotID == worker.slotID {
				if cs.releaseTimer != nil {
					cs.releaseTimer.Stop()
				}
				delete(p.clients[slug], cid)
			}
		}
	}
}
