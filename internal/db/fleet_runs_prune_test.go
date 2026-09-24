package db_test

import (
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

// TestPruneFleetRuns_KeepsNewestNPerFleetAndProtectedPointers keeps the
// newest N terminal runs per fleet_id and deletes older ones, except a run
// still named by app_fleet_state as the successful or latest run for some
// app: fleet status reads those pointers directly, so deleting the row would
// silently blank out "who/when last applied" for an app that has not been
// re-applied recently.
func TestPruneFleetRuns_KeepsNewestNPerFleetAndProtectedPointers(t *testing.T) {
	store := openTestStore(t)
	owner := mustCreateUser(t, store, "fleet-prune-owner", "developer")
	app := mustCreateApp(t, store, "fleet-prune-app", owner.ID)

	runIDs := make([]string, 5)
	for i := 0; i < 5; i++ {
		id := "run-" + string(rune('0'+i))
		runIDs[i] = id
		if _, _, err := store.CreateFleetRun(db.CreateFleetRunParams{
			ID: id, FleetID: "acme", Kind: "fleet_apply",
		}); err != nil {
			t.Fatalf("create fleet run %d: %v", i, err)
		}
		if err := store.FinishFleetRun(id, "succeeded", 0, ""); err != nil {
			t.Fatalf("finish fleet run %d: %v", i, err)
		}
	}
	// runIDs[0] is the oldest (lowest run_sequence) and the one about to be
	// pruned by rank; pin it as the app's recorded successful/latest run so
	// the prune must skip it despite its rank.
	if err := store.RecordAppFleetSuccessWithChange(app.ID, runIDs[0], "digest-1", nil, true); err != nil {
		t.Fatalf("record app fleet success: %v", err)
	}

	// A zero retention is a no-op: nothing is deleted.
	if n, err := store.PruneFleetRuns(0); err != nil || n != 0 {
		t.Fatalf("PruneFleetRuns(0) = (%d, %v), want (0, nil)", n, err)
	}

	deleted, err := store.PruneFleetRuns(2)
	if err != nil {
		t.Fatalf("PruneFleetRuns: %v", err)
	}
	// Keep newest 2 (runIDs[4], runIDs[3]) by rank, plus runIDs[0] protected
	// by the app_fleet_state pointer. runIDs[1] and runIDs[2] are pruned.
	if deleted != 2 {
		t.Fatalf("PruneFleetRuns(2) deleted %d, want 2", deleted)
	}
	for _, id := range []string{runIDs[4], runIDs[3], runIDs[0]} {
		if _, err := store.GetFleetRun(id); err != nil {
			t.Errorf("expected %s to survive prune, got %v", id, err)
		}
	}
	for _, id := range []string{runIDs[1], runIDs[2]} {
		if _, err := store.GetFleetRun(id); err != db.ErrNotFound {
			t.Errorf("expected %s to be pruned, got %v", id, err)
		}
	}
}

// TestPruneFleetRuns_NeverRemovesRunningRun mirrors PruneAppLogRuns: a run
// still in progress has no terminal rank and must never be pruned, even when
// older runs for the same fleet have already been finished and counted.
func TestPruneFleetRuns_NeverRemovesRunningRun(t *testing.T) {
	store := openTestStore(t)

	if _, _, err := store.CreateFleetRun(db.CreateFleetRunParams{
		ID: "run-running", FleetID: "acme2", Kind: "fleet_apply",
	}); err != nil {
		t.Fatalf("create running fleet run: %v", err)
	}
	for i, id := range []string{"run-a", "run-b"} {
		if _, _, err := store.CreateFleetRun(db.CreateFleetRunParams{
			ID: id, FleetID: "acme2", Kind: "fleet_apply",
		}); err != nil {
			t.Fatalf("create fleet run %d: %v", i, err)
		}
		if err := store.FinishFleetRun(id, "succeeded", 0, ""); err != nil {
			t.Fatalf("finish fleet run %d: %v", i, err)
		}
	}

	deleted, err := store.PruneFleetRuns(1)
	if err != nil {
		t.Fatalf("PruneFleetRuns: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("PruneFleetRuns(1) deleted %d, want 1 (only run-a)", deleted)
	}
	if _, err := store.GetFleetRun("run-running"); err != nil {
		t.Errorf("running run must survive prune, got %v", err)
	}
	if _, err := store.GetFleetRun("run-b"); err != nil {
		t.Errorf("newest finished run must survive prune, got %v", err)
	}
	if _, err := store.GetFleetRun("run-a"); err != db.ErrNotFound {
		t.Errorf("expected run-a to be pruned, got %v", err)
	}
}
