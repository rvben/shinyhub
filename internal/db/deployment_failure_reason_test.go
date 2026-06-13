package db_test

import (
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

// TestFailDeploymentWithReasonSurfacesInSummary verifies a failed deploy
// records WHY it failed, and that the reason is readable back via the
// slug-based summary the API and CLI use.
func TestFailDeploymentWithReasonSurfacesInSummary(t *testing.T) {
	store := mustOpenDB(t)
	owner := mustCreateUser(t, store, "owner", "developer")
	app := mustCreateApp(t, store, "app", owner.ID)

	pending, err := store.BeginDeployment(app.ID, "v1", "/b/v1")
	if err != nil {
		t.Fatalf("BeginDeployment: %v", err)
	}
	const reason = "deploy failed: R runtime not found on the server (Rscript is not in PATH)."
	if err := store.FailDeploymentWithReason(pending.ID, reason); err != nil {
		t.Fatalf("FailDeploymentWithReason: %v", err)
	}

	summaries, err := store.ListDeploymentsBySlug("app")
	if err != nil {
		t.Fatalf("ListDeploymentsBySlug: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("got %d summaries, want 1", len(summaries))
	}
	if summaries[0].Status != db.DeploymentFailed {
		t.Errorf("status = %q, want failed", summaries[0].Status)
	}
	if summaries[0].FailureReason != reason {
		t.Errorf("failure_reason = %q, want %q", summaries[0].FailureReason, reason)
	}
}

// TestFailDeploymentNoReasonStaysEmpty keeps the plain FailDeployment helper
// back-compatible: it fails the row with no recorded reason.
func TestFailDeploymentNoReasonStaysEmpty(t *testing.T) {
	store := mustOpenDB(t)
	owner := mustCreateUser(t, store, "owner", "developer")
	app := mustCreateApp(t, store, "app", owner.ID)

	pending, err := store.BeginDeployment(app.ID, "v1", "/b/v1")
	if err != nil {
		t.Fatalf("BeginDeployment: %v", err)
	}
	if err := store.FailDeployment(pending.ID); err != nil {
		t.Fatalf("FailDeployment: %v", err)
	}
	summaries, err := store.ListDeploymentsBySlug("app")
	if err != nil {
		t.Fatalf("ListDeploymentsBySlug: %v", err)
	}
	if len(summaries) != 1 || summaries[0].FailureReason != "" {
		t.Fatalf("want one summary with empty failure_reason, got %+v", summaries)
	}
}
