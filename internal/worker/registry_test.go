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
