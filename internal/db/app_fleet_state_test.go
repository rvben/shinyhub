package db_test

import (
	"errors"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/provenance"
)

func TestAppFleetStatePreservesSuccessfulBaselineAcrossIncompleteRun(t *testing.T) {
	store := mustOpenDB(t)
	owner := mustCreateUser(t, store, "fleet-owner", "admin")
	app := mustCreateApp(t, store, "fleet-app", owner.ID)
	firstID := "0123456789abcdef0123456789abcdef"
	secondID := "fedcba9876543210fedcba9876543210"
	for _, p := range []db.CreateFleetRunParams{
		{ID: firstID, FleetID: "prod", Kind: "fleet_apply", UserID: &owner.ID,
			Provenance: provenance.Metadata{Provider: "github"}},
		{ID: secondID, FleetID: "prod", Kind: "fleet_apply", UserID: &owner.ID,
			Provenance: provenance.Metadata{Provider: "gitlab"}},
	} {
		if _, _, err := store.CreateFleetRun(p); err != nil {
			t.Fatalf("create run: %v", err)
		}
	}
	declared := []db.FleetDeclaredValue{{Key: "visibility", Desired: "private"}, {Key: "replicas", Desired: "2"}}
	if err := store.RecordAppFleetSuccess(app.ID, firstID, "sha256:first", declared); err != nil {
		t.Fatalf("record success: %v", err)
	}
	if err := store.RecordAppFleetIncomplete(app.ID, secondID, "replica update failed"); err != nil {
		t.Fatalf("record incomplete: %v", err)
	}
	state, err := store.GetAppFleetState(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.ConvergenceStatus != "incomplete" || state.ConvergenceError != "replica update failed" {
		t.Fatalf("incomplete state = %#v", state)
	}
	if state.SuccessfulRun == nil || state.SuccessfulRun.ID != firstID || state.SuccessfulRun.Actor != owner.Username {
		t.Fatalf("successful run = %#v", state.SuccessfulRun)
	}
	if state.LatestRun == nil || state.LatestRun.ID != secondID || state.LatestRun.Provenance.Provider != "gitlab" {
		t.Fatalf("latest run = %#v", state.LatestRun)
	}
	if state.DesiredContentDigest != "sha256:first" || len(state.Declaration) != 2 || state.AppliedAt == nil {
		t.Fatalf("successful baseline was not preserved: %#v", state)
	}
}

func TestAppFleetStateRejectsLateOlderRun(t *testing.T) {
	store := mustOpenDB(t)
	owner := mustCreateUser(t, store, "ordered-owner", "admin")
	app := mustCreateApp(t, store, "ordered-app", owner.ID)
	oldID := "11111111111111111111111111111111"
	newID := "22222222222222222222222222222222"
	for _, id := range []string{oldID, newID} {
		if _, _, err := store.CreateFleetRun(db.CreateFleetRunParams{ID: id, FleetID: "prod", Kind: "fleet_apply", UserID: &owner.ID}); err != nil {
			t.Fatal(err)
		}
	}
	decl := []db.FleetDeclaredValue{{Key: "replicas", Desired: "2"}}
	if err := store.RecordAppFleetSuccess(app.ID, newID, "sha256:new", decl); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordAppFleetIncomplete(app.ID, oldID, "older apply finished late"); !errors.Is(err, db.ErrFleetStateSuperseded) {
		t.Fatalf("older write = %v, want ErrFleetStateSuperseded", err)
	}
	state, err := store.GetAppFleetState(app.ID)
	if err != nil || state.ConvergenceStatus != "in_sync" || state.LatestRun == nil || state.LatestRun.ID != newID {
		t.Fatalf("state = %+v err=%v", state, err)
	}
}
