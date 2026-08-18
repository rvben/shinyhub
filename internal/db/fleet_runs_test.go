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

	first, err := store.CreateDeployment(db.CreateDeploymentParams{AppID: app.ID, Version: "100", BundleDir: "/b/100", RunID: run.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateDeployment(db.CreateDeploymentParams{AppID: app.ID, Version: "100", BundleDir: "/b/100", RunID: run.ID, RestoredFromID: &first.ID}); err != nil {
		t.Fatal(err)
	}
	rows, err := store.ListDeploymentsBySlug("app")
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].Provenance == nil || rows[0].Provenance.Metadata.Source.Label != "Pipeline #42" {
		t.Fatalf("missing provenance: %#v", rows[0])
	}
	if rows[0].RestoredFromReleaseNumber == nil || *rows[0].RestoredFromReleaseNumber != 1 {
		t.Fatalf("rollback source: %#v", rows[0].RestoredFromReleaseNumber)
	}
}
