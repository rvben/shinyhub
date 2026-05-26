// internal/worker/registry_test.go
package worker

import (
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

func newTestStore(t *testing.T) *db.Store {
	t.Helper()
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return store
}

func TestRegistryRegisterAndLookup(t *testing.T) {
	reg, err := NewRegistry(newTestStore(t))
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	node, err := reg.Register(RegisterParams{
		Name:          "burst-a",
		AdvertiseAddr: "10.0.0.5:8443",
		Tier:          "burst",
		Version:       "v0.6.0",
		Fingerprint:   "ab12",
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if node.NodeID == "" {
		t.Fatal("empty node id allocated")
	}

	got, ok := reg.WorkerForTier("burst")
	if !ok || got.NodeID != node.NodeID {
		t.Fatalf("WorkerForTier(burst) = %+v ok=%v", got, ok)
	}
	if _, ok := reg.WorkerForTier("nonexistent"); ok {
		t.Fatal("WorkerForTier returned a worker for an empty tier")
	}

	byID, ok := reg.Worker(node.NodeID)
	if !ok || byID.AdvertiseAddr != "10.0.0.5:8443" {
		t.Fatalf("Worker(%q) = %+v ok=%v", node.NodeID, byID, ok)
	}
}

func TestRegistryReregisterSupersedesPriorWorkerOnTier(t *testing.T) {
	store := newTestStore(t)
	reg, err := NewRegistry(store)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	first, err := reg.Register(RegisterParams{AdvertiseAddr: "10.0.0.5:8443", Tier: "burst", Fingerprint: "aa"})
	if err != nil {
		t.Fatalf("register first: %v", err)
	}
	second, err := reg.Register(RegisterParams{AdvertiseAddr: "10.0.0.6:8443", Tier: "burst", Fingerprint: "bb"})
	if err != nil {
		t.Fatalf("register second: %v", err)
	}

	// Single worker per tier: the newest registrant wins the routing slot, and
	// the superseded worker must not be a routing candidate (its advertise
	// address and certificate identity are stale).
	for i := 0; i < 50; i++ {
		got, ok := reg.WorkerForTier("burst")
		if !ok || got.NodeID != second.NodeID {
			t.Fatalf("WorkerForTier(burst) = %+v ok=%v, want %s", got, ok, second.NodeID)
		}
	}

	if w, _ := store.GetWorker(first.NodeID); w == nil || w.Status != "down" {
		t.Fatalf("superseded worker %s status = %+v, want down in store", first.NodeID, w)
	}
	if got, ok := reg.Worker(first.NodeID); !ok || got.Status != "down" {
		t.Fatalf("superseded worker %s in-memory status = %+v ok=%v, want down", first.NodeID, got, ok)
	}
}

func TestRegistryMarkDownExcludesFromRouting(t *testing.T) {
	store := newTestStore(t)
	reg, err := NewRegistry(store)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	node, err := reg.Register(RegisterParams{AdvertiseAddr: "10.0.0.5:8443", Tier: "burst", Fingerprint: "aa"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := reg.MarkDown(node.NodeID); err != nil {
		t.Fatalf("mark down: %v", err)
	}
	if _, ok := reg.WorkerForTier("burst"); ok {
		t.Fatal("WorkerForTier returned a worker after it was marked down")
	}
	if w, _ := store.GetWorker(node.NodeID); w == nil || w.Status != "down" {
		t.Fatalf("worker status = %+v, want down in store", w)
	}
}

func TestRegistryRebuildsFromStore(t *testing.T) {
	store := newTestStore(t)
	if err := store.UpsertWorker(db.Worker{
		NodeID: "node-x", AdvertiseAddr: "1.2.3.4:9", Tier: "burst", Status: "up",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	reg, err := NewRegistry(store)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	if _, ok := reg.WorkerForTier("burst"); !ok {
		t.Fatal("registry did not rebuild in-memory index from store on construction")
	}
}
