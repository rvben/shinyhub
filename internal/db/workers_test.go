package db

import "testing"

func TestWorkerRegistryCRUD(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	w := Worker{
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

	if err := store.TouchWorkerHeartbeat("node-1", "cd34"); err != nil {
		t.Fatalf("touch: %v", err)
	}
	got, _ = store.GetWorker("node-1")
	if got.Fingerprint != "cd34" || got.LastHeartbeat == "" {
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
	if _, err := store.GetWorker("node-1"); err != ErrNotFound {
		t.Fatalf("get after delete err = %v, want ErrNotFound", err)
	}
}

func TestSupersedeTierWorkers(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	seed := func(id, tier, status string) {
		if err := store.UpsertWorker(Worker{NodeID: id, Tier: tier, Status: status}); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	seed("keep", "burst", "up")    // the surviving registrant on the tier
	seed("old", "burst", "up")     // up on the same tier, must be retired
	seed("gone", "burst", "down")  // already down, untouched
	seed("other", "base", "up")    // up on a different tier, untouched

	// Zero matching rows is valid (no prior worker), not ErrNotFound.
	if err := store.SupersedeTierWorkers("empty-tier", "keep"); err != nil {
		t.Fatalf("supersede empty tier: %v", err)
	}

	if err := store.SupersedeTierWorkers("burst", "keep"); err != nil {
		t.Fatalf("supersede: %v", err)
	}

	want := map[string]string{"keep": "up", "old": "down", "gone": "down", "other": "up"}
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
