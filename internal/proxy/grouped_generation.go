package proxy

import (
	"fmt"

	"github.com/rvben/shinyhub/internal/config"
)

// GroupedGenerationReady verifies routing for a specific serving worker.
func (p *Proxy) GroupedGenerationReady(slug string, deploymentID int64, slot int, endpoint string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	pool := p.pools[slug]
	if pool == nil || pool.mode != config.IsolationGrouped || pool.activeDeploymentID != deploymentID {
		return false
	}
	worker := pool.workers[slot]
	return worker != nil && worker.deploymentID == deploymentID && worker.targetURL == endpoint && !worker.draining.Load()
}

// StageGroupedGeneration reserves a unique slot for a readiness-tested worker.
// The candidate is absent from workers and cannot receive client assignments.
// Slot IDs are never reused, including after abort, so old disconnect timers
// and queued spawn callbacks cannot address a replacement worker.
func (p *Proxy) StageGroupedGeneration(slug string, deploymentID int64) (int, error) {
	p.mu.RLock()
	pool := p.pools[slug]
	policy := GenerationPolicy{Mode: config.IsolationGrouped}
	if pool != nil {
		policy = poolPolicy(pool)
	}
	p.mu.RUnlock()
	if policy.Mode != config.IsolationGrouped {
		return 0, fmt.Errorf("stage %s: grouped pool required", slug)
	}
	return p.StageGenerationWithPolicy(slug, deploymentID, 1, policy, 0)
}

// ElasticSlotCanStart fences callbacks queued before a grouped cutover.
func (p *Proxy) ElasticSlotCanStart(slug string, slotID int) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	pool := p.pools[slug]
	if pool == nil {
		return false
	}
	if !poolIsElastic(pool) {
		return false
	}
	worker := pool.workers[slotID]
	return worker != nil && !worker.draining.Load() && worker.status != workerDraining && (worker.deploymentID == 0 || worker.deploymentID == pool.activeDeploymentID)
}

// removeGroupedGenerationLocked removes bindings as well as routes. Open
// requests retain their backend pointer until they close; late callbacks are
// fenced by the unique slot ID and clientSlot identity.
func (p *Proxy) removeGroupedGenerationLocked(slug string, pool *backendPool, deploymentID int64) {
	if pool.generationPolicies[deploymentID].Mode != config.IsolationGrouped {
		return
	}
	for _, worker := range pool.drainingGenerations[deploymentID] {
		if worker == nil || pool.workers[worker.slotID] != worker {
			continue
		}
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
