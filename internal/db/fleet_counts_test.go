package db_test

import (
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

// TestCountRunningApps counts only apps in the running state, so the fleet gauge
// reflects what is actually serving (not hibernated/degraded/stopped apps).
func TestCountRunningApps(t *testing.T) {
	store := openTestStore(t)
	u := mustCreateUser(t, store, "owner", "developer")

	running := mustCreateApp(t, store, "run", u.ID)
	if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: running.Slug, Status: "running"}); err != nil {
		t.Fatal(err)
	}
	hib := mustCreateApp(t, store, "hib", u.ID)
	if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: hib.Slug, Status: "hibernated"}); err != nil {
		t.Fatal(err)
	}

	got, err := store.CountRunningApps()
	if err != nil {
		t.Fatalf("CountRunningApps: %v", err)
	}
	if got != 1 {
		t.Fatalf("CountRunningApps = %d, want 1", got)
	}
}

// TestCountCrashedApps counts only apps in the crashed state, so an operator can
// alert on "apps currently serving nothing" - the most actionable fleet signal.
func TestCountCrashedApps(t *testing.T) {
	store := openTestStore(t)
	u := mustCreateUser(t, store, "owner", "developer")

	crashed := mustCreateApp(t, store, "boom", u.ID)
	if err := store.MarkAppCrashed(crashed.Slug, "exhausted restart budget"); err != nil {
		t.Fatal(err)
	}
	running := mustCreateApp(t, store, "ok", u.ID)
	if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: running.Slug, Status: "running"}); err != nil {
		t.Fatal(err)
	}

	got, err := store.CountCrashedApps()
	if err != nil {
		t.Fatalf("CountCrashedApps: %v", err)
	}
	if got != 1 {
		t.Fatalf("CountCrashedApps = %d, want 1", got)
	}
}

// TestCountRunningReplicas counts only replica rows in the running state across
// all apps, so the fleet gauge reflects live serving capacity.
func TestCountRunningReplicas(t *testing.T) {
	store := openTestStore(t)
	u := mustCreateUser(t, store, "owner", "developer")
	app := mustCreateApp(t, store, "app", u.ID)

	pid, port := 100, 20100
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: 0, PID: &pid, Port: &port, Status: db.ReplicaStatusRunning,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: 1, Status: "crashed",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: 2, Status: db.ReplicaStatusLost,
	}); err != nil {
		t.Fatal(err)
	}

	got, err := store.CountRunningReplicas()
	if err != nil {
		t.Fatalf("CountRunningReplicas: %v", err)
	}
	if got != 1 {
		t.Fatalf("CountRunningReplicas = %d, want 1", got)
	}
}
