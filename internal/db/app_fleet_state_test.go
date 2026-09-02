package db_test

import (
	"errors"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/provenance"
)

func TestAppFleetStateNoOpCheckPreservesLastApplication(t *testing.T) {
	store := mustOpenDB(t)
	owner := mustCreateUser(t, store, "checked-owner", "admin")
	app := mustCreateApp(t, store, "checked-app", owner.ID)
	firstID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	secondID := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	for _, id := range []string{firstID, secondID} {
		if _, _, err := store.CreateFleetRun(db.CreateFleetRunParams{ID: id, FleetID: "prod", Kind: "fleet_apply", UserID: &owner.ID}); err != nil {
			t.Fatal(err)
		}
	}
	declared := []db.FleetDeclaredValue{{Key: "replicas", Desired: "2"}}
	if err := store.RecordAppFleetSuccess(app.ID, firstID, "sha256:same", declared); err != nil {
		t.Fatal(err)
	}
	oldApplied := "2026-01-02 03:04:05"
	oldChecked := "2026-01-02 03:05:06"
	if _, err := store.DB().Exec(`UPDATE app_fleet_state SET applied_at = ?, convergence_updated_at = ? WHERE app_id = ?`, oldApplied, oldChecked, app.ID); err != nil {
		t.Fatal(err)
	}

	if err := store.RecordAppFleetSuccessWithChange(app.ID, secondID, "sha256:same", declared, false); err != nil {
		t.Fatal(err)
	}
	state, err := store.GetAppFleetState(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.SuccessfulRun == nil || state.SuccessfulRun.ID != firstID {
		t.Fatalf("successful application = %#v, want original run", state.SuccessfulRun)
	}
	if state.LatestRun == nil || state.LatestRun.ID != secondID {
		t.Fatalf("latest check = %#v, want second run", state.LatestRun)
	}
	wantApplied, _ := time.Parse("2006-01-02 15:04:05", oldApplied)
	if state.AppliedAt == nil || !state.AppliedAt.Equal(wantApplied) {
		t.Fatalf("applied_at = %v, want preserved %v", state.AppliedAt, wantApplied)
	}
	oldCheckedAt, _ := time.Parse("2006-01-02 15:04:05", oldChecked)
	if !state.ConvergenceUpdatedAt.After(oldCheckedAt) {
		t.Fatalf("checked_at = %v, want newer than %v", state.ConvergenceUpdatedAt, oldCheckedAt)
	}
}

func TestAppFleetStateChangedDeclarationAdvancesApplicationWithoutLiveMutation(t *testing.T) {
	store := mustOpenDB(t)
	owner := mustCreateUser(t, store, "declaration-owner", "admin")
	app := mustCreateApp(t, store, "declaration-app", owner.ID)
	firstID := "cccccccccccccccccccccccccccccccc"
	secondID := "dddddddddddddddddddddddddddddddd"
	for _, id := range []string{firstID, secondID} {
		if _, _, err := store.CreateFleetRun(db.CreateFleetRunParams{ID: id, FleetID: "prod", Kind: "fleet_apply", UserID: &owner.ID}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.RecordAppFleetSuccess(app.ID, firstID, "sha256:same", []db.FleetDeclaredValue{{Key: "replicas", Desired: "2"}}); err != nil {
		t.Fatal(err)
	}
	changedDeclaration := []db.FleetDeclaredValue{
		{Key: "replicas", Desired: "2"},
		{Key: "visibility", Desired: "private"},
	}
	if err := store.RecordAppFleetSuccessWithChange(app.ID, secondID, "sha256:same", changedDeclaration, false); err != nil {
		t.Fatal(err)
	}
	state, err := store.GetAppFleetState(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.SuccessfulRun == nil || state.SuccessfulRun.ID != secondID {
		t.Fatalf("successful application = %#v, want declaration-changing run", state.SuccessfulRun)
	}
	if len(state.Declaration) != 2 {
		t.Fatalf("declaration = %#v, want updated baseline", state.Declaration)
	}
}

func TestAppFleetStateDeclarationOrderDoesNotAdvanceApplication(t *testing.T) {
	store := mustOpenDB(t)
	owner := mustCreateUser(t, store, "order-owner", "admin")
	app := mustCreateApp(t, store, "order-app", owner.ID)
	firstID := "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	secondID := "ffffffffffffffffffffffffffffffff"
	for _, id := range []string{firstID, secondID} {
		if _, _, err := store.CreateFleetRun(db.CreateFleetRunParams{ID: id, FleetID: "prod", Kind: "fleet_apply", UserID: &owner.ID}); err != nil {
			t.Fatal(err)
		}
	}
	first := []db.FleetDeclaredValue{
		{Key: "replicas", Desired: "2"},
		{Key: "visibility", Desired: "private"},
	}
	if err := store.RecordAppFleetSuccess(app.ID, firstID, "sha256:same", first); err != nil {
		t.Fatal(err)
	}
	second := []db.FleetDeclaredValue{
		{Key: "visibility", Desired: "private"},
		{Key: "replicas", Desired: "2"},
	}
	if err := store.RecordAppFleetSuccessWithChange(app.ID, secondID, "sha256:same", second, false); err != nil {
		t.Fatal(err)
	}
	state, err := store.GetAppFleetState(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.SuccessfulRun == nil || state.SuccessfulRun.ID != firstID {
		t.Fatalf("order-only check advanced application to %#v", state.SuccessfulRun)
	}
	if state.LatestRun == nil || state.LatestRun.ID != secondID {
		t.Fatalf("latest check = %#v, want second run", state.LatestRun)
	}
}

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
	if err := store.RecordAppFleetSuccessWithChange(app.ID, oldID, "sha256:old", decl, true); !errors.Is(err, db.ErrFleetStateSuperseded) {
		t.Fatalf("older success = %v, want ErrFleetStateSuperseded", err)
	}
	state, err := store.GetAppFleetState(app.ID)
	if err != nil || state.ConvergenceStatus != "in_sync" || state.LatestRun == nil || state.LatestRun.ID != newID {
		t.Fatalf("state = %+v err=%v", state, err)
	}
}

func TestAppFleetStateAdoptedFleetNoOpRefreshesApplication(t *testing.T) {
	store := mustOpenDB(t)
	owner := mustCreateUser(t, store, "adopted-owner", "admin")
	app := mustCreateApp(t, store, "adopted-app", owner.ID)
	alphaID := "33333333333333333333333333333333"
	betaID := "44444444444444444444444444444444"
	if _, _, err := store.CreateFleetRun(db.CreateFleetRunParams{ID: alphaID, FleetID: "alpha", Kind: "fleet_apply", UserID: &owner.ID}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateFleetRun(db.CreateFleetRunParams{ID: betaID, FleetID: "beta", Kind: "fleet_apply", UserID: &owner.ID}); err != nil {
		t.Fatal(err)
	}
	declared := []db.FleetDeclaredValue{{Key: "replicas", Desired: "2"}}
	if err := store.RecordAppFleetSuccess(app.ID, alphaID, "sha256:same", declared); err != nil {
		t.Fatal(err)
	}
	oldApplied := "2026-01-02 03:04:05"
	if _, err := store.DB().Exec(`UPDATE app_fleet_state SET applied_at = ? WHERE app_id = ?`, oldApplied, app.ID); err != nil {
		t.Fatal(err)
	}

	// Fleet beta adopted the app with an identical declaration and digest, but
	// the adopt run's own fleet-state request was lost. Beta's first no-op
	// apply must still take over the successful-application provenance: the
	// retained run belongs to a fleet that no longer owns the app, and the
	// reader discards a cross-fleet application, so preserving it would leave
	// the app without a recorded application until a real declaration change.
	if err := store.RecordAppFleetSuccessWithChange(app.ID, betaID, "sha256:same", declared, false); err != nil {
		t.Fatal(err)
	}
	state, err := store.GetAppFleetState(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.SuccessfulRun == nil || state.SuccessfulRun.ID != betaID {
		t.Fatalf("successful application = %#v, want the adopting fleet's run", state.SuccessfulRun)
	}
	if state.SuccessfulRun.FleetID != "beta" {
		t.Fatalf("successful application fleet = %q, want beta", state.SuccessfulRun.FleetID)
	}
	oldAppliedAt, _ := time.Parse("2006-01-02 15:04:05", oldApplied)
	if state.AppliedAt == nil || !state.AppliedAt.After(oldAppliedAt) {
		t.Fatalf("applied_at = %v, want refreshed past %v", state.AppliedAt, oldAppliedAt)
	}
}
