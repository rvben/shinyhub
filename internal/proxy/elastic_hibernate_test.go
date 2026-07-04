package proxy

import (
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
)

// TestProxy_BeginHibernate_ElasticWorkerWithActiveConns proves the hibernation
// activity gate accounts for elastic (grouped/per_session) pools, whose live
// backends live in pool.workers, not pool.replicas. A long-lived Shiny
// WebSocket keeps its worker's activeConns > 0 while lastSeen goes stale past
// the idle timeout; hibernating then would tear down the live session mid-use
// (ARCH-1). BeginHibernate must refuse while any worker has active connections
// and only hibernate once the pool is genuinely idle.
func TestProxy_BeginHibernate_ElasticWorkerWithActiveConns(t *testing.T) {
	p := New()
	p.SetPoolMode("app", config.IsolationGrouped, 3, 10)
	pool := p.pools["app"]
	wkr := &replicaBackend{slotID: pool.allocateSlotID()}
	wkr.activeConns.Store(1)
	addElasticWorker(pool, wkr)

	// lastSeen is unset, so only the activeConns scan can block hibernation.
	if p.BeginHibernate("app", time.Now()) {
		t.Fatal("BeginHibernate hibernated an elastic pool with a live worker connection (ARCH-1)")
	}

	// Once the worker has no active connections the pool is genuinely idle and
	// may hibernate.
	wkr.activeConns.Store(0)
	if !p.BeginHibernate("app", time.Now()) {
		t.Fatal("BeginHibernate refused to hibernate a genuinely idle elastic pool")
	}
}
