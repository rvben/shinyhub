package db_test

import (
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

// TestQuarantineRecordsReasonAndLastErrorSeparately pins the split between the
// two columns a compatibility quarantine writes: deployments.failure_reason
// keeps the classified reason the deploy history and HTTP response show, while
// apps.last_error carries the caller's diagnostic (the boot error plus the
// app's log tail) that the app's Overview reads.
func TestQuarantineRecordsReasonAndLastErrorSeparately(t *testing.T) {
	for _, status := range []string{"failed", "stopped"} {
		t.Run(status, func(t *testing.T) {
			store := mustOpenDB(t)
			owner := mustCreateUser(t, store, "owner", "developer")
			app := mustCreateApp(t, store, "app", owner.ID)
			pending, err := store.BeginDeployment(app.ID, "v1", t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err := store.MarkDeploymentProducerBarrierEntered(pending.ID); err != nil {
				t.Fatal(err)
			}
			const reason = "deploy failed: the app exited during startup."
			const diagnostic = "exit status 1\nTraceback (most recent call last):\nRuntimeError: boom"
			if err := store.QuarantineAndFailDeploymentWithAppStatus(pending.ID, reason, diagnostic, status); err != nil {
				t.Fatal(err)
			}

			got, err := store.GetAppBySlug("app")
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != status || got.LastError != diagnostic {
				t.Fatalf("app = (status %q, last_error %q), want (%q, %q)", got.Status, got.LastError, status, diagnostic)
			}
			summaries, err := store.ListDeploymentsBySlug("app")
			if err != nil {
				t.Fatal(err)
			}
			if len(summaries) != 1 || summaries[0].Status != db.DeploymentFailed || summaries[0].FailureReason != reason {
				t.Fatalf("deployments = %+v, want one failed row with failure_reason %q", summaries, reason)
			}
		})
	}
}

// TestQuarantineWithoutDiagnosticRecordsReasonOnApp covers the callers with no
// boot error to report (startup recovery, provenance persistence failures):
// the reason is then the only explanation, so it lands on the app too.
func TestQuarantineWithoutDiagnosticRecordsReasonOnApp(t *testing.T) {
	store := mustOpenDB(t)
	owner := mustCreateUser(t, store, "owner", "developer")
	app := mustCreateApp(t, store, "app", owner.ID)
	pending, err := store.BeginDeployment(app.ID, "v1", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkDeploymentProducerBarrierEntered(pending.ID); err != nil {
		t.Fatal(err)
	}
	const reason = "server interrupted after candidate compatibility barrier"
	if err := store.QuarantineAndFailDeployment(pending.ID, reason); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetAppBySlug("app")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "failed" || got.LastError != reason {
		t.Fatalf("app = (status %q, last_error %q), want (failed, %q)", got.Status, got.LastError, reason)
	}
}
