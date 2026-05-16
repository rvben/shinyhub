package db_test

import (
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

// TestDeploymentLifecycle exercises the pending->succeeded/failed state
// machine and the invariant that an in-flight deployment never becomes the
// authoritative live-bundle pointer until it is promoted.
func TestDeploymentLifecycle(t *testing.T) {
	store := mustOpenDB(t)
	owner := mustCreateUser(t, store, "owner", "developer")
	app := mustCreateApp(t, store, "app", owner.ID)

	// A first succeeded deployment is the live bundle.
	if _, err := store.CreateDeployment(db.CreateDeploymentParams{
		AppID: app.ID, Version: "v1", BundleDir: "/b/v1",
	}); err != nil {
		t.Fatalf("seed v1: %v", err)
	}

	// Begin a new deployment: the pending row must NOT shift the live pointer.
	pending, err := store.BeginDeployment(app.ID, "v2", "/b/v2")
	if err != nil {
		t.Fatalf("BeginDeployment: %v", err)
	}
	if pending.Status != db.DeploymentPending {
		t.Errorf("BeginDeployment status = %q, want pending", pending.Status)
	}
	live, err := store.ListDeployments(app.ID)
	if err != nil {
		t.Fatalf("ListDeployments: %v", err)
	}
	if len(live) != 1 || live[0].Version != "v1" {
		t.Fatalf("during in-flight deploy, live pointer = %+v, want only v1", live)
	}
	inflight, err := store.ListInflightDeployments()
	if err != nil {
		t.Fatalf("ListInflightDeployments: %v", err)
	}
	if len(inflight) != 1 || inflight[0].ID != pending.ID {
		t.Fatalf("inflight = %+v, want the v2 pending row", inflight)
	}

	// Promote: v2 becomes the live bundle, v1 the rollback target.
	if err := store.PromoteDeployment(pending.ID); err != nil {
		t.Fatalf("PromoteDeployment: %v", err)
	}
	live, _ = store.ListDeployments(app.ID)
	if len(live) != 2 || live[0].Version != "v2" || live[1].Version != "v1" {
		t.Fatalf("after promote, live = %+v, want [v2, v1]", live)
	}
	if in, _ := store.ListInflightDeployments(); len(in) != 0 {
		t.Fatalf("after promote, inflight = %+v, want none", in)
	}

	// Promote is single-shot: a second call must not resurrect the row.
	if err := store.PromoteDeployment(pending.ID); err == nil {
		t.Error("second PromoteDeployment succeeded, want error (no longer pending)")
	}
}

// TestFailDeploymentKeepsPreviousLive verifies an aborted deploy never
// displaces the previous live deployment and is excluded from history.
func TestFailDeploymentKeepsPreviousLive(t *testing.T) {
	store := mustOpenDB(t)
	owner := mustCreateUser(t, store, "owner", "developer")
	app := mustCreateApp(t, store, "app", owner.ID)

	if _, err := store.CreateDeployment(db.CreateDeploymentParams{
		AppID: app.ID, Version: "v1", BundleDir: "/b/v1",
	}); err != nil {
		t.Fatalf("seed v1: %v", err)
	}
	pending, err := store.BeginDeployment(app.ID, "v2", "/b/v2")
	if err != nil {
		t.Fatalf("BeginDeployment: %v", err)
	}
	if err := store.FailDeployment(pending.ID); err != nil {
		t.Fatalf("FailDeployment: %v", err)
	}

	live, _ := store.ListDeployments(app.ID)
	if len(live) != 1 || live[0].Version != "v1" {
		t.Fatalf("after failed deploy, live = %+v, want only v1", live)
	}
	if in, _ := store.ListInflightDeployments(); len(in) != 0 {
		t.Fatalf("after fail, inflight = %+v, want none", in)
	}
	// A failed deployment can no longer be promoted.
	if err := store.PromoteDeployment(pending.ID); err == nil {
		t.Error("PromoteDeployment on a failed row succeeded, want error")
	}
}
