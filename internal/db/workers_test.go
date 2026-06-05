package db_test

import (
	"testing"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

func TestWorkerRegistryCRUD(t *testing.T) {
	store := dbtest.New(t)

	w := db.Worker{
		NodeID:        "node-1",
		Name:          "burst-a",
		AdvertiseAddr: "10.0.0.5:8443",
		Tier:          "burst",
		Status:        "up",
		Fingerprint:   "ab12",
		Version:       "v0.6.0",
	}
	if err := store.UpsertWorker(w); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := store.GetWorker("node-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Tier != "burst" || got.AdvertiseAddr != "10.0.0.5:8443" || got.Fingerprint != "ab12" {
		t.Fatalf("get = %+v", got)
	}

	if err := store.TouchWorkerHeartbeat("node-1", "cd34", "up"); err != nil {
		t.Fatalf("touch: %v", err)
	}
	got, _ = store.GetWorker("node-1")
	if got.Fingerprint != "cd34" || got.LastHeartbeat == "" || got.Status != "up" {
		t.Fatalf("after touch = %+v", got)
	}

	if err := store.SetWorkerStatus("node-1", "down"); err != nil {
		t.Fatalf("set status: %v", err)
	}
	all, err := store.ListWorkers()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 1 || all[0].Status != "down" {
		t.Fatalf("list = %+v", all)
	}

	if err := store.DeleteWorker("node-1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := store.GetWorker("node-1"); err != db.ErrNotFound {
		t.Fatalf("get after delete err = %v, want ErrNotFound", err)
	}
}

// TestSupersedeTierAddrWorkers asserts the address-scoped supersede: only other
// up workers sharing the exact (tier, advertise address) are retired. A distinct-
// address worker on the same tier is real multi-worker capacity and must stay up;
// a same-address worker on a different tier is untouched.
func TestSupersedeTierAddrWorkers(t *testing.T) {
	store := dbtest.New(t)

	seed := func(id, tier, addr, status string) {
		if err := store.UpsertWorker(db.Worker{NodeID: id, Tier: tier, AdvertiseAddr: addr, Status: status}); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	seed("keep", "burst", "10.0.0.5:8443", "up")   // the surviving registrant at this endpoint
	seed("stale", "burst", "10.0.0.5:8443", "up")  // same tier+addr, must be retired
	seed("peer", "burst", "10.0.0.6:8443", "up")   // same tier, different addr: stays up
	seed("gone", "burst", "10.0.0.5:8443", "down") // already down, untouched
	seed("other", "base", "10.0.0.5:8443", "up")   // same addr, different tier: untouched

	// Zero matching rows is valid (no prior worker at this endpoint), not ErrNotFound.
	if err := store.SupersedeTierAddrWorkers("empty-tier", "10.0.0.9:1", "keep"); err != nil {
		t.Fatalf("supersede empty tier: %v", err)
	}

	if err := store.SupersedeTierAddrWorkers("burst", "10.0.0.5:8443", "keep"); err != nil {
		t.Fatalf("supersede: %v", err)
	}

	want := map[string]string{"keep": "up", "stale": "down", "peer": "up", "gone": "down", "other": "up"}
	for id, status := range want {
		got, err := store.GetWorker(id)
		if err != nil {
			t.Fatalf("get %s: %v", id, err)
		}
		if got.Status != status {
			t.Errorf("worker %s status = %q, want %q", id, got.Status, status)
		}
	}
}
