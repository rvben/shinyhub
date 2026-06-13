package db_test

import (
	"testing"
)

// TestAppSummary_LastDeploymentStatus verifies the app summary distinguishes a
// failed-only deploy from a never-deployed app, so the dashboard can render
// "Failed" instead of the benign "Awaiting deploy".
func TestAppSummary_LastDeploymentStatus(t *testing.T) {
	store := mustOpenDB(t)
	owner := mustCreateUser(t, store, "owner", "developer")
	app := mustCreateApp(t, store, "app", owner.ID)

	got, err := store.GetApp("app")
	if err != nil {
		t.Fatal(err)
	}
	if got.LastDeploymentStatus != "" {
		t.Errorf("never-deployed LastDeploymentStatus = %q, want empty", got.LastDeploymentStatus)
	}

	pending, err := store.BeginDeployment(app.ID, "v1", "/b/v1")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FailDeployment(pending.ID); err != nil {
		t.Fatal(err)
	}
	got, err = store.GetApp("app")
	if err != nil {
		t.Fatal(err)
	}
	if got.LastDeploymentStatus != "failed" {
		t.Errorf("after failed deploy, LastDeploymentStatus = %q, want \"failed\"", got.LastDeploymentStatus)
	}
}
