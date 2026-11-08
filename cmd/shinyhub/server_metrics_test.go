package main

import (
	"testing"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
	"github.com/rvben/shinyhub/internal/metrics"
)

func TestFleetCountsReadsTheStore(t *testing.T) {
	store := dbtest.New(t)
	runningID := createTestApp(t, store, "alpha")
	for _, slug := range []string{"beta", "gamma", "delta", "epsilon"} {
		createTestApp(t, store, slug)
	}
	for slug, status := range map[string]string{"alpha": "running", "beta": "running", "gamma": "running", "delta": "crashed"} {
		if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: slug, Status: status}); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 2 {
		if err := store.UpsertReplica(db.UpsertReplicaParams{AppID: runningID, Index: i, Status: db.ReplicaStatusRunning}); err != nil {
			t.Fatal(err)
		}
	}

	got, err := fleetCounts(store)()
	if err != nil {
		t.Fatalf("fleet counts: %v", err)
	}
	if want := (metrics.FleetCounts{AppsRunning: 3, ReplicasRunning: 2, AppsCrashed: 1}); got != want {
		t.Fatalf("fleet counts = %+v, want %+v", got, want)
	}
}

// A store that cannot answer yields an error, so the gauges go absent rather
// than reading as an empty, healthy fleet.
func TestFleetCountsReportsAFailedQuery(t *testing.T) {
	store := dbtest.New(t)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := fleetCounts(store)(); err == nil {
		t.Fatalf("fleet counts on a closed store = %+v with no error, want an error", got)
	}
}
