// internal/worker/registry_revoke_test.go
package worker

import (
	"errors"
	"sync"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

// TestRegistryRevokeExcludesFromRoutingAndPersists asserts that revoking a
// worker removes it from routing immediately, marks it revoked in both the
// in-memory index and the store, and that the revocation survives a
// control-plane restart (NewRegistry rebuild).
func TestRegistryRevokeExcludesFromRoutingAndPersists(t *testing.T) {
	store := newTestStore(t)
	reg, err := NewRegistry(store)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	node, err := reg.Register(RegisterParams{AdvertiseAddr: "10.0.0.5:8443", Tier: "burst", Fingerprint: "aa"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	if err := reg.Revoke(node.NodeID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, ok := reg.WorkerForTier("burst"); ok {
		t.Fatal("revoked worker still routable for its tier")
	}
	w, ok := reg.Worker(node.NodeID)
	if !ok {
		t.Fatal("revoked worker dropped from in-memory index (revocation must remain auditable)")
	}
	if !w.Revoked() || w.Status != "down" {
		t.Fatalf("in-memory worker not revoked/down: %+v", w)
	}
	if sw, _ := store.GetWorker(node.NodeID); sw == nil || !sw.Revoked() {
		t.Fatalf("revocation not persisted to store: %+v", sw)
	}

	// A fresh registry rebuilt from the store keeps the revocation.
	reg2, err := NewRegistry(store)
	if err != nil {
		t.Fatalf("rebuild registry: %v", err)
	}
	if _, ok := reg2.WorkerForTier("burst"); ok {
		t.Fatal("revoked worker routable after rebuild")
	}
	if w, ok := reg2.Worker(node.NodeID); !ok || !w.Revoked() {
		t.Fatalf("rebuild lost revocation: %+v ok=%v", w, ok)
	}

	if err := reg.Revoke("ghost"); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("revoke unknown node err = %v, want ErrNotFound", err)
	}
}

// TestRegistryHeartbeatRefusesRevokedWorker asserts a revoked worker can never
// be promoted back to up by a heartbeat: the heartbeat is rejected, so a
// resurrected agent presenting a still-valid (un-expired) cert cannot rejoin
// routing within its TTL.
func TestRegistryHeartbeatRefusesRevokedWorker(t *testing.T) {
	store := newTestStore(t)
	reg, err := NewRegistry(store)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	node, err := reg.Register(RegisterParams{AdvertiseAddr: "10.0.0.5:8443", Tier: "burst", Fingerprint: "aa"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := reg.Revoke(node.NodeID); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	if err := reg.Heartbeat(node.NodeID, "bb"); err == nil {
		t.Fatal("heartbeat from a revoked worker was accepted")
	}
	if _, ok := reg.WorkerForTier("burst"); ok {
		t.Fatal("revoked worker re-upped via heartbeat")
	}
	if w, _ := reg.Worker(node.NodeID); w.Status == "up" {
		t.Fatalf("revoked worker promoted to up: %+v", w)
	}
}

// TestRegistryRevokeRaceWithHeartbeat asserts that a heartbeat racing a revoke
// can never resurrect the worker to up/routable. Heartbeat reads the worker,
// decides status, then writes it back; if revoke is not serialized against that
// read-modify-write, a heartbeat that read the pre-revoke row writes status=up
// after the revoke lands, and because that node stays revoked, no later
// heartbeat ever corrects it. Once all goroutines settle, the worker must be
// revoked, down, and unroutable on every iteration.
func TestRegistryRevokeRaceWithHeartbeat(t *testing.T) {
	for iter := 0; iter < 200; iter++ {
		store := newTestStore(t)
		reg, err := NewRegistry(store)
		if err != nil {
			t.Fatalf("new registry: %v", err)
		}
		node, err := reg.Register(RegisterParams{AdvertiseAddr: "10.0.0.5:8443", Tier: "burst", Fingerprint: "aa"})
		if err != nil {
			t.Fatalf("register: %v", err)
		}

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				_ = reg.Heartbeat(node.NodeID, "bb")
			}
		}()
		go func() {
			defer wg.Done()
			_ = reg.Revoke(node.NodeID)
		}()
		wg.Wait()

		// Drain any heartbeat that may still be promoting; the invariant must hold
		// once revoke has returned, regardless of ordering.
		w, ok := reg.Worker(node.NodeID)
		if !ok {
			t.Fatalf("iter %d: worker dropped from index", iter)
		}
		if !w.Revoked() {
			t.Fatalf("iter %d: worker not revoked after concurrent revoke: %+v", iter, w)
		}
		if w.Status == "up" {
			t.Fatalf("iter %d: revoked worker resurrected to up: %+v", iter, w)
		}
		if _, routable := reg.WorkerForTier("burst"); routable {
			t.Fatalf("iter %d: revoked worker is routable", iter)
		}
		store.Close()
	}
}

// TestRegistryForgetDropsFromIndex asserts Forget removes a reaped worker from
// the in-memory index so it no longer appears in the fleet snapshot, while being
// a no-op for unknown node ids.
func TestRegistryForgetDropsFromIndex(t *testing.T) {
	store := newTestStore(t)
	reg, err := NewRegistry(store)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	node, err := reg.Register(RegisterParams{AdvertiseAddr: "10.0.0.5:8443", Tier: "burst", Fingerprint: "aa"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	reg.Forget(node.NodeID)
	if _, ok := reg.Worker(node.NodeID); ok {
		t.Fatal("forgotten worker still present in the index")
	}

	// Unknown node: no panic, no effect.
	reg.Forget("ghost")
}
