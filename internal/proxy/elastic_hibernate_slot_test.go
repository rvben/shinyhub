package proxy

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
)

// TestProxy_BeginHibernate_ElasticSlotNotReused proves that hibernating an
// elastic pool and then waking it never reissues a slot ID a pre-hibernate
// generation used. Before the fix, BeginHibernate only removed p.pools[slug]:
// it left p.clients[slug] bound to the old slot, never bumped p.poolEpoch,
// and the next SetPoolMode built a fresh pool whose nextSlotID restarted at
// 0. That let a still-armed lifetime timer from the old generation land on
// the new slot-0 worker (elastic.go armLifetime/expireLifetime), and left a
// stale client binding pinned to a slot a new worker now occupies.
func TestProxy_BeginHibernate_ElasticSlotNotReused(t *testing.T) {
	p := New()
	p.SetPoolMode("app", config.IsolationGrouped, 3, 10)
	pool := p.pools["app"]
	slot0 := pool.allocateSlotID()
	wkr := &replicaBackend{slotID: slot0}
	addElasticWorker(pool, wkr)
	p.bindClient("app", "client1", slot0)

	epochBefore := p.PoolEpoch("app")

	if !p.BeginHibernate("app", time.Now()) {
		t.Fatal("BeginHibernate refused to hibernate a genuinely idle elastic pool")
	}

	if got := p.PoolEpoch("app"); got <= epochBefore {
		t.Fatalf("PoolEpoch not advanced by BeginHibernate: before=%d after=%d", epochBefore, got)
	}
	if n := len(p.clients["app"]); n != 0 {
		t.Fatalf("p.clients[%q] still holds %d entries after BeginHibernate, want 0", "app", n)
	}

	// Wake: rebuild the pool the way the watcher does on a wake request.
	p.SetPoolMode("app", config.IsolationGrouped, 3, 10)
	wokenPool := p.pools["app"]
	newSlot := wokenPool.allocateSlotID()

	if newSlot <= slot0 {
		t.Fatalf("woken pool reused slot ID %d (previously held by slot %d)", newSlot, slot0)
	}
}

// TestNoStrayBackendPoolLiterals guards newBackendPoolLocked's role as the
// single place a *backendPool is constructed. A pool built by any other
// literal bypasses the slotSeq seeding that keeps elastic slot IDs from
// being reused after a pool is torn down and recreated for the same slug, so
// a second construction site reintroduces the bug this package's other
// elastic-hibernate tests guard against. The one literal inside
// newBackendPoolLocked itself is expected and excluded.
func TestNoStrayBackendPoolLiterals(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	needle := []byte("&backendPool{")
	var stray []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", name, err)
		}
		count := bytes.Count(data, needle)
		if name == "proxy.go" {
			if count != 1 {
				t.Errorf("proxy.go: want exactly 1 occurrence of %q (inside newBackendPoolLocked), got %d", needle, count)
			}
			continue
		}
		if count > 0 {
			stray = append(stray, name)
		}
	}
	if len(stray) > 0 {
		t.Errorf("found %q outside newBackendPoolLocked in: %v", needle, stray)
	}
}
