package db

import "testing"

// TestRevokeWorker verifies revocation marks a worker down, stamps revoked_at,
// surfaces through Revoked() on reads, is idempotent (preserves the first
// revoke time), and reports ErrNotFound for an unknown node.
func TestRevokeWorker(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	if err := store.UpsertWorker(Worker{
		NodeID: "node-1", AdvertiseAddr: "10.0.0.5:8443", Tier: "burst", Status: "up",
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// A freshly registered worker is not revoked.
	got, err := store.GetWorker("node-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Revoked() {
		t.Fatalf("new worker reports revoked: %+v", got)
	}

	if err := store.RevokeWorker("node-1"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	got, err = store.GetWorker("node-1")
	if err != nil {
		t.Fatalf("get after revoke: %v", err)
	}
	if !got.Revoked() {
		t.Fatalf("worker not revoked after RevokeWorker: %+v", got)
	}
	if got.Status != "down" {
		t.Errorf("revoke did not mark worker down: status=%q", got.Status)
	}
	firstRevokedAt := got.RevokedAt
	if firstRevokedAt == "" {
		t.Fatal("revoked_at not stamped")
	}

	// ListWorkers carries the revocation marker.
	all, err := store.ListWorkers()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 1 || !all[0].Revoked() {
		t.Fatalf("ListWorkers lost revocation: %+v", all)
	}

	// Re-revoking preserves the first revoke timestamp (audit stability).
	if err := store.RevokeWorker("node-1"); err != nil {
		t.Fatalf("re-revoke: %v", err)
	}
	got, _ = store.GetWorker("node-1")
	if got.RevokedAt != firstRevokedAt {
		t.Errorf("re-revoke changed revoked_at: %q -> %q", firstRevokedAt, got.RevokedAt)
	}

	if err := store.RevokeWorker("ghost"); err != ErrNotFound {
		t.Errorf("revoke unknown node err = %v, want ErrNotFound", err)
	}
}
