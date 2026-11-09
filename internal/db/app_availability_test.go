package db_test

import (
	"context"
	"errors"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/schedulespec"
)

func TestAppAvailabilityPublishedReleaseAndQuarantine(t *testing.T) {
	store := openTestStore(t)
	u := mustCreateUser(t, store, "owner", "developer")
	app := mustCreateApp(t, store, "demo", u.ID)
	ctx := context.Background()
	if _, err := store.GetAppAvailability(ctx, "missing"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	a, err := store.GetAppAvailability(ctx, app.Slug)
	if err != nil || a.DeploymentID != 0 {
		t.Fatalf("never deployed: %+v %v", a, err)
	}
	good, err := store.CreateDeployment(db.CreateDeploymentParams{AppID: app.ID, Version: "good", BundleDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := store.BeginDeployment(app.ID, "candidate", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, failed := range []bool{false, true} {
		if failed {
			if err := store.FailDeployment(candidate.ID); err != nil {
				t.Fatal(err)
			}
		}
		a, err = store.GetAppAvailability(ctx, app.Slug)
		if err != nil || a.DeploymentID != good.ID || a.ServingBlocked || a.ActivationBlocked {
			t.Fatalf("candidate failed=%v obscured serving release: %+v %v", failed, a, err)
		}
	}
	barrier, err := store.BeginDeployment(app.ID, "writer", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkDeploymentProducerBarrierEntered(barrier.ID); err != nil {
		t.Fatal(err)
	}
	a, err = store.GetAppAvailability(ctx, app.Slug)
	if err != nil || !a.ServingBlocked || !a.ActivationBlocked {
		t.Fatalf("barrier did not fence serving: %+v %v", a, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := store.GetAppAvailability(canceled, app.Slug); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled query: %v", err)
	}
}

func TestAppAvailabilityDistinguishesActivePublisherFromFailedWrite(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "publisher")
	dep := promoteConvergenceDeployment(t, store, appID, "good", "sha256:good")
	scheduleID := createConvergenceSchedule(t, store, appID, `["producer"]`, schedulespec.DeployTriggerBundleChange)
	runID := insertProducerRun(t, store, scheduleID, dep, `["producer"]`)
	a, err := store.GetAppAvailability(context.Background(), "publisher")
	if err != nil || a.ServingBlocked || !a.ActivationBlocked {
		t.Fatalf("healthy publisher should only block activation: %+v %v", a, err)
	}
	finishProducerRun(t, store, runID, "failed")
	a, err = store.GetAppAvailability(context.Background(), "publisher")
	if err != nil || !a.ServingBlocked || !a.ActivationBlocked {
		t.Fatalf("failed write must block serving and activation: %+v %v", a, err)
	}
}

func TestAppAvailabilityLegacyReleaseFallback(t *testing.T) {
	store := openTestStore(t)
	u := mustCreateUser(t, store, "owner", "developer")
	app := mustCreateApp(t, store, "legacy", u.ID)
	good, err := store.CreateDeployment(db.CreateDeploymentParams{AppID: app.ID, Version: "good", BundleDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`UPDATE apps SET active_deployment_id = NULL WHERE id = ?`, app.ID); err != nil {
		t.Fatal(err)
	}
	a, err := store.GetAppAvailability(context.Background(), app.Slug)
	if err != nil || a.DeploymentID != good.ID {
		t.Fatalf("legacy successful release: %+v %v", a, err)
	}
	if _, err := store.BeginDeployment(app.ID, "pending", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	a, err = store.GetAppAvailability(context.Background(), app.Slug)
	if err != nil || a.DeploymentID != good.ID {
		t.Fatalf("pending candidate obscured successful legacy release: %+v %v", a, err)
	}
}
