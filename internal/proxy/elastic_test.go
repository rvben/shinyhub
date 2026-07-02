package proxy

import (
	"testing"

	"github.com/rvben/shinyhub/internal/config"
)

// TestElasticSetPoolMode_InitializesWorkersMap verifies that SetPoolMode with
// an elastic mode (grouped or per_session) initialises pool.workers.
func TestElasticSetPoolMode_InitializesWorkersMap(t *testing.T) {
	for _, mode := range []config.WorkerIsolationMode{config.IsolationGrouped, config.IsolationPerSession} {
		t.Run(string(mode), func(t *testing.T) {
			p := New()
			p.SetPoolMode("app", mode, 5, 10)

			p.mu.RLock()
			pool := p.pools["app"]
			p.mu.RUnlock()

			if pool == nil {
				t.Fatal("pool not created")
			}
			if pool.mode != mode {
				t.Errorf("pool.mode = %q, want %q", pool.mode, mode)
			}
			if pool.workers == nil {
				t.Error("pool.workers is nil; expected an initialized map for elastic mode")
			}
			if pool.groupedSize != 5 {
				t.Errorf("pool.groupedSize = %d, want 5", pool.groupedSize)
			}
			if pool.maxWorkers != 10 {
				t.Errorf("pool.maxWorkers = %d, want 10", pool.maxWorkers)
			}
		})
	}
}

// TestElasticSetPoolMode_SwitchToMultiplexClearsWorkers verifies that switching
// a pool back to multiplex sets pool.workers = nil.
func TestElasticSetPoolMode_SwitchToMultiplexClearsWorkers(t *testing.T) {
	p := New()
	// First put pool into elastic mode so workers map is allocated.
	p.SetPoolMode("app", config.IsolationGrouped, 3, 4)

	p.mu.RLock()
	pool := p.pools["app"]
	p.mu.RUnlock()
	if pool.workers == nil {
		t.Fatal("precondition: workers should be non-nil in elastic mode")
	}

	// Switch to multiplex.
	p.SetPoolMode("app", config.IsolationMultiplex, 0, 0)

	p.mu.RLock()
	pool = p.pools["app"]
	p.mu.RUnlock()
	if pool.workers != nil {
		t.Error("pool.workers should be nil after switching to multiplex")
	}
	if pool.mode != config.IsolationMultiplex {
		t.Errorf("pool.mode = %q, want %q", pool.mode, config.IsolationMultiplex)
	}
}

// TestElasticSetPoolMode_EmptyModeIsMultiplex verifies that a pool with
// zero-value mode="" behaves as multiplex (workers must stay nil).
func TestElasticSetPoolMode_EmptyModeIsMultiplex(t *testing.T) {
	p := New()
	// Create pool via SetPoolSize so mode stays zero value.
	p.SetPoolSize("app", 1)

	p.mu.RLock()
	pool := p.pools["app"]
	p.mu.RUnlock()

	if pool.workers != nil {
		t.Error("pool.workers should be nil for zero-value mode (= multiplex)")
	}
	if poolIsElastic(pool) {
		t.Error("poolIsElastic should be false for zero-value mode")
	}
}

// TestElasticAllocateSlotID_Monotonic verifies that allocateSlotID always
// returns a strictly increasing value and never repeats across add/remove cycles.
func TestElasticAllocateSlotID_Monotonic(t *testing.T) {
	p := New()
	p.SetPoolMode("app", config.IsolationPerSession, 1, 20)

	p.mu.Lock()
	pool := p.pools["app"]
	p.mu.Unlock()

	seen := make(map[int]bool)
	prev := -1
	for i := 0; i < 20; i++ {
		id := pool.allocateSlotID()
		if id <= prev {
			t.Errorf("slot ID %d is not strictly greater than previous %d (monotonicity broken)", id, prev)
		}
		if seen[id] {
			t.Errorf("slot ID %d allocated twice (not unique)", id)
		}
		seen[id] = true
		prev = id

		// Simulate add then remove to ensure IDs do not reset.
		rb := &replicaBackend{slotID: id}
		addElasticWorker(pool, rb)
		removeElasticWorker(pool, id)
	}
}

// TestElasticPoolHasAny_TrueWhenElasticWorkerExists verifies that poolHasAny
// returns true when there is at least one worker in the elastic map.
func TestElasticPoolHasAny_TrueWhenElasticWorkerExists(t *testing.T) {
	p := New()
	p.SetPoolMode("app", config.IsolationGrouped, 3, 10)

	p.mu.Lock()
	pool := p.pools["app"]
	// Add a worker directly to the elastic map.
	addElasticWorker(pool, &replicaBackend{slotID: pool.allocateSlotID()})
	p.mu.Unlock()

	if !p.poolRoutable("app") {
		t.Error("poolRoutable should be true when elastic pool has at least one worker")
	}
}

// TestElasticPoolHasAny_FalseForMultiplexWithNoReplicas verifies that a
// multiplex pool (mode=="") with no registered replicas is not routable,
// even when workers map is accidentally non-nil.
func TestElasticPoolHasAny_FalseForMultiplexWithNoReplicas(t *testing.T) {
	p := New()
	p.SetPoolSize("app", 2) // mode stays ""

	p.mu.Lock()
	pool := p.pools["app"]
	// Artificially set workers to non-nil to confirm it does NOT make the pool routable.
	pool.workers = map[int]*replicaBackend{0: {slotID: 0}}
	p.mu.Unlock()

	if p.poolRoutable("app") {
		t.Error("poolRoutable should be false for multiplex pool with no replicas, even if workers map is non-nil")
	}
}

// TestElasticMultiplexUnaffected verifies that a multiplex pool created via
// SetPoolSize retains correct routing behaviour after SetPoolMode(multiplex).
func TestElasticMultiplexUnaffected(t *testing.T) {
	p := New()
	p.SetPoolSize("app", 1)
	// Calling SetPoolMode with multiplex on an already-multiplex pool is a no-op.
	p.SetPoolMode("app", config.IsolationMultiplex, 0, 0)

	p.mu.RLock()
	pool := p.pools["app"]
	p.mu.RUnlock()

	if pool.workers != nil {
		t.Error("workers map should remain nil after SetPoolMode(multiplex) on a multiplex pool")
	}
	if poolIsElastic(pool) {
		t.Error("poolIsElastic should be false for a multiplex pool")
	}

	// Register a replica and confirm routing still works via the normal path.
	if err := p.RegisterReplica("app", 0, "http://127.0.0.1:9999", nil, 1); err != nil {
		t.Fatalf("RegisterReplica: %v", err)
	}
	if !p.poolRoutable("app") {
		t.Error("multiplex pool with a registered replica should be routable")
	}
}
