package proxy

import (
	"fmt"
	"net/http"

	"github.com/rvben/shinyhub/internal/config"
)

// GenerationPolicy describes the routing and worker limits owned by a generation.
// Staging a policy never changes the policy of the serving generation.
type GenerationPolicy struct {
	Mode                                config.WorkerIsolationMode
	GroupedSize, MaxWorkers, WarmSpares int
}

func poolPolicy(pool *backendPool) GenerationPolicy {
	mode := pool.mode
	if mode == "" {
		mode = config.IsolationMultiplex
	}
	return GenerationPolicy{Mode: mode, GroupedSize: pool.groupedSize, MaxWorkers: pool.maxWorkers, WarmSpares: pool.warmSpareTarget}
}

func publishGenerationPolicy(pool *backendPool, policy GenerationPolicy) {
	pool.mode = policy.Mode
	pool.groupedSize = policy.GroupedSize
	pool.maxWorkers = policy.MaxWorkers
	pool.warmSpareTarget = policy.WarmSpares
}

func activeGenerationBackends(pool *backendPool) []*replicaBackend {
	if !poolIsElastic(pool) {
		return pool.replicas
	}
	out := make([]*replicaBackend, 0, len(pool.workers))
	for _, worker := range pool.workers {
		if !worker.draining.Load() && (pool.activeDeploymentID == 0 || worker.deploymentID == 0 || worker.deploymentID == pool.activeDeploymentID) {
			out = append(out, worker)
		}
	}
	return out
}

// StageGenerationWithPolicy reserves unpublished routes and, for grouped,
// a globally monotonic worker slot above the caller's manager/ledger floor.
func (p *Proxy) StageGenerationWithPolicy(slug string, deploymentID int64, size int, policy GenerationPolicy, minSlot int) (int, error) {
	if deploymentID <= 0 || size < 1 || minSlot < 0 {
		return -1, fmt.Errorf("stage %s: invalid generation reservation", slug)
	}
	if policy.Mode == "" {
		policy.Mode = config.IsolationMultiplex
	}
	if policy.Mode != config.IsolationGrouped && policy.Mode != config.IsolationMultiplex {
		return -1, fmt.Errorf("stage %s: unsupported isolation %s", slug, policy.Mode)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	pool := p.pools[slug]
	if pool == nil {
		return -1, fmt.Errorf("stage %s: pool not found", slug)
	}
	if pool.mode == config.IsolationPerSession || deploymentID == pool.activeDeploymentID || len(pool.candidates) > 0 || len(pool.drainingGenerations) > 0 {
		return -1, fmt.Errorf("stage %s: unsupported pool or generation already active, staged or draining", slug)
	}
	if pool.candidates == nil {
		pool.candidates = make(map[int64][]*replicaBackend)
	}
	if pool.generationPolicies == nil {
		pool.generationPolicies = make(map[int64]GenerationPolicy)
	}
	slot := -1
	if policy.Mode == config.IsolationGrouped {
		if pool.nextSlotID < minSlot {
			pool.nextSlotID = minSlot
		}
		if pool.nextSlotID < len(pool.replicas) {
			pool.nextSlotID = len(pool.replicas)
		}
		slot = pool.allocateSlotID()
		size = slot + 1
		if p.slotSeq == nil {
			p.slotSeq = make(map[string]int)
		}
		if p.slotSeq[slug] < pool.nextSlotID {
			p.slotSeq[slug] = pool.nextSlotID
		}
	}
	pool.candidates[deploymentID] = make([]*replicaBackend, size)
	pool.generationPolicies[deploymentID] = policy
	return slot, nil
}

// ServingMode reports the policy selected for fresh requests.
func (p *Proxy) ServingMode(slug string) (config.WorkerIsolationMode, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	pool := p.pools[slug]
	if pool == nil {
		return "", false
	}
	return poolPolicy(pool).Mode, true
}

// ElasticWorkerGeneration resolves retained workers even when multiplex is active.
func (p *Proxy) ElasticWorkerGeneration(slug string, slot int) (int64, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	pool := p.pools[slug]
	if pool == nil {
		return 0, false
	}
	w := pool.workers[slot]
	if w == nil {
		return 0, false
	}
	return w.deploymentID, true
}

// SetTerminateGenerationFunc installs exact-generation teardown. It supersedes
// the legacy callback, which remains available for compatibility.
func (p *Proxy) SetTerminateGenerationFunc(fn func(string, int64, int)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.terminateGeneration = fn
}

// terminateWorkerLocked captures ownership before route removal; callbacks may
// run after the pool or worker has already disappeared.
func (p *Proxy) terminateWorkerLocked(slug string, w *replicaBackend) func() {
	if w == nil {
		return nil
	}
	slot, dep := w.slotID, w.deploymentID
	if term := p.terminateGeneration; term != nil {
		return func() { term(slug, dep, slot) }
	}
	if term := p.terminate; term != nil {
		return func() { term(slug, slot) }
	}
	return nil
}

func (p *Proxy) hasRetainedClientLocked(slug string, pool *backendPool, r *http.Request) bool {
	if _, err := r.Cookie(clientCookiePrefix + slug); err != nil {
		return false
	}
	cid, isNew := p.clientID(r, slug)
	if isNew {
		return false
	}
	cs := p.clients[slug][cid]
	if cs == nil {
		return false
	}
	w := pool.workers[cs.slotID]
	return w != nil && w.draining.Load() && w.status == workerRunning
}

// HasCandidateGeneration reports unpublished reservations for cold-start fencing.
func (p *Proxy) HasCandidateGeneration(slug string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	pool := p.pools[slug]
	return pool != nil && len(pool.candidates) > 0
}

// HasServingBackends reports concrete routes, rather than demand-driven capacity.
func (p *Proxy) HasServingBackends(slug string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	pool := p.pools[slug]
	if pool == nil {
		return false
	}
	if len(pool.workers) > 0 {
		return true
	}
	for _, rep := range pool.replicas {
		if rep != nil {
			return true
		}
	}
	for _, generation := range pool.drainingGenerations {
		for _, rep := range generation {
			if rep != nil {
				return true
			}
		}
	}
	return false
}

// EnsureServingGeneration attaches verified durable authority to an idle pool.
// It never overrides a different published generation or runtime mode.
func (p *Proxy) EnsureServingGeneration(slug string, deploymentID int64, mode config.WorkerIsolationMode) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	pool := p.pools[slug]
	if pool == nil || deploymentID <= 0 {
		return fmt.Errorf("serving generation %s: pool and positive deployment ID required", slug)
	}
	if mode == "" {
		mode = config.IsolationMultiplex
	}
	if poolPolicy(pool).Mode != mode || (pool.activeDeploymentID != 0 && pool.activeDeploymentID != deploymentID) {
		return fmt.Errorf("serving generation %s: runtime authority or isolation differs", slug)
	}
	for _, rep := range activeGenerationBackends(pool) {
		if rep != nil && rep.deploymentID != 0 && rep.deploymentID != deploymentID {
			return fmt.Errorf("serving generation %s: backend belongs to another generation", slug)
		}
	}
	pool.activeDeploymentID = deploymentID
	return nil
}
