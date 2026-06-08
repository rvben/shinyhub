package db_test

import (
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

// TestAppHasRunningReplica verifies the DB-backed readiness query used by the
// clustered app-readiness probe. The probe must answer from replica status so
// all instances report consistently, regardless of which one observed the WS
// handshake locally.
func TestAppHasRunningReplica(t *testing.T) {
	store := openTestStore(t)
	u := mustCreateUser(t, store, "owner", "developer")
	app := mustCreateApp(t, store, "demo", u.ID)

	// No replicas yet: must return false.
	got, err := store.AppHasRunningReplica("demo")
	if err != nil {
		t.Fatalf("AppHasRunningReplica (no replicas): %v", err)
	}
	if got {
		t.Fatal("expected false when no replicas exist")
	}

	// Add a non-running replica: still false.
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID:  app.ID,
		Index:  0,
		Status: db.ReplicaStatusLost,
	}); err != nil {
		t.Fatalf("UpsertReplica (lost): %v", err)
	}
	got, err = store.AppHasRunningReplica("demo")
	if err != nil {
		t.Fatalf("AppHasRunningReplica (lost replica): %v", err)
	}
	if got {
		t.Fatal("expected false when only a lost replica exists")
	}

	// Upsert a running replica: must return true.
	pid, port := 1234, 20001
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID:  app.ID,
		Index:  1,
		PID:    &pid,
		Port:   &port,
		Status: db.ReplicaStatusRunning,
	}); err != nil {
		t.Fatalf("UpsertReplica (running): %v", err)
	}
	got, err = store.AppHasRunningReplica("demo")
	if err != nil {
		t.Fatalf("AppHasRunningReplica (running replica): %v", err)
	}
	if !got {
		t.Fatal("expected true when at least one running replica exists")
	}

	// A degraded app with a running replica still returns true: the probe
	// answers from replica status, not app status.
	if err := store.UpdateAppStatus(db.UpdateAppStatusParams{
		Slug:   "demo",
		Status: "degraded",
	}); err != nil {
		t.Fatalf("UpdateAppStatus degraded: %v", err)
	}
	got, err = store.AppHasRunningReplica("demo")
	if err != nil {
		t.Fatalf("AppHasRunningReplica (degraded app, running replica): %v", err)
	}
	if !got {
		t.Fatal("expected true: running replica exists even though app is degraded")
	}

	// Unknown slug: must return false (no matching row), no error.
	got, err = store.AppHasRunningReplica("no-such-app")
	if err != nil {
		t.Fatalf("AppHasRunningReplica (unknown slug): %v", err)
	}
	if got {
		t.Fatal("expected false for an unknown slug")
	}
}
