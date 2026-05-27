// internal/worker/registry_test.go
package worker

import (
	"sync"
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

// TestRegistryConcurrentSameTierRegistrationsConverge asserts that many
// concurrent registrations on one tier converge to a single up worker that the
// store and the in-memory index agree on. If the supersede store write is not
// serialized with the registration, two registrations can each mark the other
// down in the store, leaving zero up workers persisted while one is up in
// memory -- the tier then has no routing candidate after a control-plane
// restart rebuilds the index from the store.
func TestRegistryConcurrentSameTierRegistrationsConverge(t *testing.T) {
	store := newTestStore(t)
	reg, err := NewRegistry(store)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}

	const n = 16
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			if _, err := reg.Register(RegisterParams{
				AdvertiseAddr: "10.0.0.5:8443", Tier: "burst", Fingerprint: "aa",
			}); err != nil {
				t.Errorf("register: %v", err)
			}
		}()
	}
	wg.Wait()

	// Exactly one worker is up in the store, matching the in-memory routing slot.
	all, err := store.ListWorkers()
	if err != nil {
		t.Fatalf("list workers: %v", err)
	}
	var upInStore []string
	for _, w := range all {
		if w.Status == "up" {
			upInStore = append(upInStore, w.NodeID)
		}
	}
	if len(upInStore) != 1 {
		t.Fatalf("up workers in store = %v, want exactly 1", upInStore)
	}
	routed, ok := reg.WorkerForTier("burst")
	if !ok {
		t.Fatal("WorkerForTier(burst) found no worker after concurrent registrations")
	}
	if routed.NodeID != upInStore[0] {
		t.Fatalf("routing slot %s disagrees with the up worker in store %s", routed.NodeID, upInStore[0])
	}
}

// TestRegistryHeartbeatDoesNotResurrectSupersededWorker asserts that a heartbeat
// from a worker that was superseded while offline (e.g. it restarted and
// re-adopted its old identity) does not flip it back to up alongside the newer
// tier owner. The newest registrant keeps the single routing slot; the heartbeat
// still refreshes the superseded worker's fingerprint and liveness.
func TestRegistryHeartbeatDoesNotResurrectSupersededWorker(t *testing.T) {
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

	// The superseded worker restarts and heartbeats under its old node id.
	if err := reg.Heartbeat(first.NodeID, "aa2"); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}

	got, ok := reg.WorkerForTier("burst")
	if !ok || got.NodeID != second.NodeID {
		t.Fatalf("WorkerForTier(burst) = %+v ok=%v, want %s", got, ok, second.NodeID)
	}
	w, ok := reg.Worker(first.NodeID)
	if !ok || w.Status != "down" {
		t.Errorf("superseded worker re-upped via heartbeat: %+v ok=%v", w, ok)
	}
	if w.Fingerprint != "aa2" {
		t.Errorf("heartbeat did not refresh superseded worker fingerprint: %+v", w)
	}
	if sw, _ := store.GetWorker(first.NodeID); sw == nil || sw.Status != "down" {
		t.Errorf("superseded worker re-upped in store: %+v", sw)
	}
}

// TestRegistryHeartbeatReupsWhenTierSlotFree asserts that a worker reaped for
// missed heartbeats (marked down with no successor) recovers its routing slot on
// its next heartbeat, since the tier is otherwise unowned.
func TestRegistryHeartbeatReupsWhenTierSlotFree(t *testing.T) {
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
		t.Fatal("tier slot should be free after the only worker was marked down")
	}

	if err := reg.Heartbeat(node.NodeID, "bb"); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	got, ok := reg.WorkerForTier("burst")
	if !ok || got.NodeID != node.NodeID {
		t.Fatalf("recovered worker not re-upped: %+v ok=%v", got, ok)
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
