package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/jobs"
	"github.com/rvben/shinyhub/internal/process"
)

// refusedDeletionFixture is an app whose deletion is refused because a
// ledgered worker cannot be confirmed stopped, with a jobs manager attached.
func refusedDeletionFixture(t *testing.T, slug string) (*ledgerStopFixture, *jobs.Manager) {
	t.Helper()
	f := newLedgerStopFixture(t, slug)
	jm, err := jobs.NewManager(process.NewManager(t.TempDir(), process.NewNativeRuntime()), nil, process.DefaultTier, f.store, nil, t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		jm.Stop(ctx)
	})
	f.srv.SetJobs(jm, nil)
	stranger := startLedgerOrphan(t, t.TempDir())
	f.record(t, stranger.pid, 0)
	return f, jm
}

func (f *ledgerStopFixture) deleteRefused(t *testing.T) {
	t.Helper()
	req := httptest.NewRequest(http.MethodDelete, "/api/apps/"+f.app.Slug, nil)
	req.Header.Set("Authorization", "Bearer "+f.token)
	rec := httptest.NewRecorder()
	f.srv.Router().ServeHTTP(rec, req)
	// The refusal is the deletion's own: a 500 for an unconfirmed stop, not an
	// auth or routing failure that never reached the job manager.
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("delete over an unidentifiable live worker: %d %s, want the deletion refused", rec.Code, rec.Body.String())
	}
	if _, err := f.store.GetAppBySlug(f.app.Slug); err != nil {
		t.Fatalf("the refused deletion removed the app: %v", err)
	}
}

// A deletion refused because a ledgered worker cannot be confirmed stopped
// leaves the app in place, so it must also leave the app able to run its
// scheduled jobs: the deletion closed job admission before it found the row.
func TestDeleteApp_RefusedDeletionReopensScheduledJobs(t *testing.T) {
	f, jm := refusedDeletionFixture(t, "ledger-delete")
	f.deleteRefused(t)

	unpause, err := jm.TryPauseAppRuns(f.app.ID)
	if err != nil {
		t.Fatalf("job admission after a refused deletion: %v, want it open again", err)
	}
	unpause()
}

// A refused deletion must keep the app's per-schedule locks. A run that holds
// one across the deletion would otherwise share its schedule with a run that
// takes a fresh lock once admission reopens, defeating the overlap policy.
func TestDeleteApp_RefusedDeletionKeepsScheduleLocks(t *testing.T) {
	f, jm := refusedDeletionFixture(t, "ledger-delete-locks")
	scheduleID, err := f.store.CreateSchedule(db.CreateScheduleParams{
		AppID: f.app.ID, Name: "refresh", CronExpr: "0 * * * *", CommandJSON: `["true"]`,
		Enabled: true, TimeoutSeconds: 60, OverlapPolicy: "skip", MissedPolicy: "skip",
	})
	if err != nil {
		t.Fatal(err)
	}
	release := jm.AcquireProducerGates([]int64{scheduleID})
	held := true
	defer func() {
		if held {
			release()
		}
	}()

	f.deleteRefused(t)

	second := make(chan func())
	go func() { second <- jm.AcquireProducerGates([]int64{scheduleID}) }()
	select {
	case r := <-second:
		r()
		t.Fatal("a second holder took the schedule's gate while the first still held it: the refused deletion forgot the lock")
	case <-time.After(200 * time.Millisecond):
	}
	release()
	held = false
	(<-second)()
}

// A deletion retried after the app was tombstoned (a first attempt failed
// during cleanup) must leave job admission closed when it fails again: the
// app is on its way out, and startup reconciliation completes the deletion.
func TestDeleteApp_FailedRetryOfATombstonedAppKeepsJobsClosed(t *testing.T) {
	f, jm := refusedDeletionFixture(t, "ledger-delete-retry")
	if err := f.store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: f.app.Slug, Status: "deleting"}); err != nil {
		t.Fatal(err)
	}
	f.deleteRefused(t)

	if unpause, err := jm.TryPauseAppRuns(f.app.ID); err == nil {
		unpause()
		t.Fatal("a failed retry reopened job admission for an app already tombstoned for deletion")
	}
}

// A deletion that waited for the deploy lock holds a snapshot of the app taken
// before an overlapping deletion tombstoned it. When it then fails, it must
// still leave job admission closed: the tombstone in the store, not the stale
// snapshot, says the app is on its way out.
func TestDeleteApp_FailedDeletionFromAStaleSnapshotKeepsJobsClosed(t *testing.T) {
	f, jm := refusedDeletionFixture(t, "ledger-delete-stale")
	stale, err := f.store.GetAppBySlug(f.app.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if stale.Status == "deleting" {
		t.Fatal("the snapshot is already tombstoned, so this test cannot show a stale one")
	}
	if err := f.store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: f.app.Slug, Status: "deleting"}); err != nil {
		t.Fatal(err)
	}

	release := f.srv.acquireDeployLock(f.app.Slug)
	_, err = f.srv.deleteAppLocked(context.Background(), stale)
	release()
	if err == nil {
		t.Fatal("deletion over an unidentifiable live worker succeeded, so this test cannot show a failure")
	}

	if unpause, err := jm.TryPauseAppRuns(f.app.ID); err == nil {
		unpause()
		t.Fatal("a failed deletion holding a stale snapshot reopened job admission for a tombstoned app")
	}
}
