package db_test

import (
	"errors"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/provenance"
)

func TestFleetRunDeploymentAttributionAndRollbackSource(t *testing.T) {
	store := mustOpenDB(t)
	owner := mustCreateUser(t, store, "owner", "developer")
	app := mustCreateApp(t, store, "app", owner.ID)
	p := db.CreateFleetRunParams{ID: "0123456789abcdef0123456789abcdef", FleetID: "prod-eu", Kind: "fleet_apply", UserID: &owner.ID,
		Provenance: provenance.Metadata{Provider: "gitlab", Source: &provenance.Link{Label: "Pipeline #42", URL: "https://gitlab.example/pipelines/42"}}}
	run, created, err := store.CreateFleetRun(p)
	if err != nil || !created {
		t.Fatalf("create run: created=%v err=%v", created, err)
	}
	if _, created, err := store.CreateFleetRun(p); err != nil || created {
		t.Fatalf("idempotent create: created=%v err=%v", created, err)
	}
	conflict := p
	conflict.FleetID = "other"
	if _, _, err := store.CreateFleetRun(conflict); !errors.Is(err, db.ErrFleetRunConflict) {
		t.Fatalf("conflict err=%v", err)
	}

	first, err := store.CreateDeployment(db.CreateDeploymentParams{
		AppID: app.ID, Version: "100", BundleDir: "/b/100", RunID: run.ID,
		Origin: db.DeploymentOrigin{UserID: &owner.ID, Actor: owner.Username},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateDeployment(db.CreateDeploymentParams{
		AppID: app.ID, Version: "100", BundleDir: "/b/100", RunID: run.ID, RestoredFromID: &first.ID,
		Origin: db.DeploymentOrigin{UserID: &owner.ID, Actor: owner.Username},
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := store.ListDeploymentsBySlug("app")
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].Provenance == nil || rows[0].Provenance.Metadata.Source.Label != "Pipeline #42" {
		t.Fatalf("missing provenance: %#v", rows[0])
	}
	if rows[0].Provenance.Origin.Kind != db.DeploymentOriginFleet || rows[0].Provenance.Origin.Actor != "owner" {
		t.Fatalf("fleet origin: %#v", rows[0].Provenance.Origin)
	}
	if rows[0].RestoredFromReleaseNumber == nil || *rows[0].RestoredFromReleaseNumber != 1 {
		t.Fatalf("rollback source: %#v", rows[0].RestoredFromReleaseNumber)
	}
}

func TestFleetRunLifecycleIsSequencedAndImmutable(t *testing.T) {
	store := mustOpenDB(t)
	owner := mustCreateUser(t, store, "runner", "developer")
	makeRun := func(id string) *db.FleetRun {
		run, created, err := store.CreateFleetRun(db.CreateFleetRunParams{
			ID: id, FleetID: "prod", Kind: "fleet_apply", UserID: &owner.ID,
		})
		if err != nil || !created {
			t.Fatalf("CreateFleetRun(%s): created=%v err=%v", id, created, err)
		}
		return run
	}
	first := makeRun("11111111111111111111111111111111")
	second := makeRun("22222222222222222222222222222222")
	if first.Sequence <= 0 || second.Sequence != first.Sequence+1 {
		t.Fatalf("sequences = %d, %d; want consecutive server order", first.Sequence, second.Sequence)
	}
	if first.Status != "running" || first.ExitCode != nil || first.FinishedAt != nil {
		t.Fatalf("new run = %+v", first)
	}
	if err := store.TouchFleetRun(first.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishFleetRun(first.ID, "succeeded", 0, "OK - all converged"); err != nil {
		t.Fatal(err)
	}
	// Lost-response retries are harmless, but a different terminal truth is not.
	if err := store.FinishFleetRun(first.ID, "succeeded", 0, "OK - all converged"); err != nil {
		t.Fatalf("idempotent finish: %v", err)
	}
	if err := store.FinishFleetRun(first.ID, "partial", 4, "different"); !errors.Is(err, db.ErrFleetRunFinished) {
		t.Fatalf("different finish = %v, want ErrFleetRunFinished", err)
	}
	if err := store.TouchFleetRun(first.ID); !errors.Is(err, db.ErrFleetRunFinished) {
		t.Fatalf("heartbeat after finish = %v, want ErrFleetRunFinished", err)
	}
	finished, err := store.GetFleetRun(first.ID)
	if err != nil || finished.FinishedAt == nil || finished.ExitCode == nil || *finished.ExitCode != 0 {
		t.Fatalf("finished run = %+v err=%v", finished, err)
	}
}

func TestDeploymentOriginsDistinguishDirectRollbackAndLegacy(t *testing.T) {
	store := mustOpenDB(t)
	owner := mustCreateUser(t, store, "operator", "developer")
	app := mustCreateApp(t, store, "origins", owner.ID)

	direct, err := store.CreateDeployment(db.CreateDeploymentParams{
		AppID: app.ID, Version: "100", BundleDir: "/b/direct",
		Origin: db.DeploymentOrigin{Kind: db.DeploymentOriginDirect, Channel: db.DeploymentChannelDashboard, UserID: &owner.ID, Actor: owner.Username},
	})
	if err != nil {
		t.Fatal(err)
	}
	current, err := store.CurrentDeploymentProvenance(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current == nil || current.Origin.Kind != db.DeploymentOriginDirect || current.Origin.Actor != "operator" {
		t.Fatalf("direct current provenance = %#v", current)
	}
	if _, err := store.CreateDeployment(db.CreateDeploymentParams{
		AppID: app.ID, Version: "100", BundleDir: "/b/rollback", RestoredFromID: &direct.ID,
		Origin: db.DeploymentOrigin{Kind: db.DeploymentOriginRollback, Channel: db.DeploymentChannelCLI, UserID: &owner.ID, Actor: owner.Username},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateDeployment(db.CreateDeploymentParams{AppID: app.ID, Version: "50", BundleDir: "/b/legacy"}); err != nil {
		t.Fatal(err)
	}

	rows, err := store.ListDeploymentsBySlug("origins")
	if err != nil {
		t.Fatal(err)
	}
	if got := rows[0].Provenance.Origin; got.Kind != db.DeploymentOriginLegacy || got.Actor != "" {
		t.Fatalf("legacy origin = %#v", got)
	}
	if got := rows[1].Provenance.Origin; got.Kind != db.DeploymentOriginRollback || got.Channel != db.DeploymentChannelCLI || got.Actor != "operator" {
		t.Fatalf("rollback origin = %#v", got)
	}
	if got := rows[2].Provenance.Origin; got.Kind != db.DeploymentOriginDirect || got.Channel != db.DeploymentChannelDashboard || got.Actor != "operator" {
		t.Fatalf("direct origin = %#v", got)
	}

	current, err = store.CurrentDeploymentProvenance(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current != nil {
		t.Fatalf("legacy current provenance should stay out of the header: %#v", current)
	}
}
