package lifecycle

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/process"
)

// newElasticHibernateRetryFixture builds a watcher whose single elastic slot
// (app/0, pid 111) is due for hibernation, with term scripting the inline
// TerminateConfirmed result.
func newElasticHibernateRetryFixture(term *fakeElasticTerminator) (*Watcher, *fakeManager, *fakeStore) {
	mgr := &fakeManager{entries: []*process.ProcessInfo{
		{Slug: "app", Index: 0, Status: process.StatusRunning, AppID: 1, DeploymentID: 5, PID: 111, Tier: "python"},
	}}
	prx := newFakeProxy()
	prx.seen["app"] = time.Now().Add(-2 * time.Hour)
	st := newFakeStore(
		map[string]*db.App{"app": {
			ID:              1,
			Slug:            "app",
			Status:          "running",
			WorkerIsolation: string(config.IsolationGrouped),
			UpdatedAt:       time.Now().Add(-3 * time.Hour),
		}},
		nil,
	)
	w := newTestWatcher(Config{HibernateTimeout: 30 * time.Minute, RestartMaxAttempts: 5},
		mgr, prx, st, func(_ context.Context, slug, dir string, idx int) (*deploy.Result, error) {
			return &deploy.Result{}, nil
		})
	w.SetElasticTerminator(term)
	return w, mgr, st
}

// TestRetryElasticHibernateStop_IdentityOnlyRetryNeverSignalsReplacement
// covers a hibernate teardown whose stop WAS confirmed inline but whose
// identity delete failed. The slot is then free, so a later occupant (for
// example a fixed replica after a switch to multiplex and a redeploy) can
// take the same index before the identity retry succeeds. The retry has only
// the identity row left to clear and must never signal that slot again.
func TestRetryElasticHibernateStop_IdentityOnlyRetryNeverSignalsReplacement(t *testing.T) {
	term := &fakeElasticTerminator{results: map[replicaKey][]ElasticTerminateResult{
		{"app", 0}: {{
			Slug: "app", SlotID: 0, AppID: 1, DeploymentID: 5, PID: 111, Incarnation: 7, Native: true,
			Stopped: true, IdentityCleared: false,
		}},
	}}
	w, mgr, st := newElasticHibernateRetryFixture(term)
	st.mu.Lock()
	st.deleteDeploymentReplicaIdentityErr = errors.New("database is locked")
	st.mu.Unlock()

	w.runOnce()

	key := pendingStopKey{slug: "app", index: 0}
	if _, ok := w.pendingStops[key]; !ok {
		t.Fatal("expected the failed identity delete to be queued for retry")
	}

	// The slot is reused by an unrelated replacement process.
	mgr.mu.Lock()
	mgr.entries = []*process.ProcessInfo{
		{Slug: "app", Index: 0, Status: process.StatusRunning, AppID: 1, DeploymentID: 6, PID: 222, Tier: "python"},
	}
	mgr.mu.Unlock()

	w.processPendingStops() // identity delete still fails
	st.mu.Lock()
	st.deleteDeploymentReplicaIdentityErr = nil
	st.mu.Unlock()
	w.processPendingStops() // identity delete succeeds

	mgr.mu.Lock()
	confirmed := append([]replicaKey(nil), mgr.stopConfirmedCalls...)
	byGen := append([]stopIncarnationCall(nil), mgr.stopIncarnationCalls...)
	mgr.mu.Unlock()
	if len(confirmed) != 0 || len(byGen) != 0 {
		t.Fatalf("identity-only retry signalled the slot: StopReplicaConfirmed=%v StopReplicaIncarnation=%v", confirmed, byGen)
	}
	if _, ok := w.pendingStops[key]; ok {
		t.Error("expected the entry to be dequeued once the identity delete succeeded")
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.deletedDeploymentReplicaIdentities) != 1 || st.deletedDeploymentReplicaIdentities[0].pid != 111 {
		t.Errorf("expected exactly one identity delete for pid 111, got %+v", st.deletedDeploymentReplicaIdentities)
	}
}

// TestRetryElasticHibernateStop_UnconfirmedRetryTargetsCapturedIncarnation
// covers the other half: a stop that was NOT confirmed inline is retried
// against exactly the incarnation the inline stop targeted, never by
// slug/index alone, so a replacement in the same slot cannot be signalled.
func TestRetryElasticHibernateStop_UnconfirmedRetryTargetsCapturedIncarnation(t *testing.T) {
	term := &fakeElasticTerminator{results: map[replicaKey][]ElasticTerminateResult{
		{"app", 0}: {{
			Slug: "app", SlotID: 0, AppID: 1, DeploymentID: 5, PID: 111, Incarnation: 7, Native: true,
			Stopped: false, IdentityCleared: false,
		}},
	}}
	w, mgr, st := newElasticHibernateRetryFixture(term)

	w.runOnce()
	key := pendingStopKey{slug: "app", index: 0}
	if e, ok := w.pendingStops[key]; !ok || e.Incarnation != 7 || e.Stopped {
		t.Fatalf("expected an unconfirmed entry carrying incarnation 7, got %+v (queued=%v)", e, ok)
	}

	w.processPendingStops()

	mgr.mu.Lock()
	confirmed := append([]replicaKey(nil), mgr.stopConfirmedCalls...)
	byGen := append([]stopIncarnationCall(nil), mgr.stopIncarnationCalls...)
	mgr.mu.Unlock()
	if len(confirmed) != 0 {
		t.Errorf("retry stopped by slug/index alone: %v", confirmed)
	}
	if len(byGen) != 1 || byGen[0] != (stopIncarnationCall{"app", 0, 7}) {
		t.Errorf("expected one StopReplicaIncarnation(app, 0, 7), got %v", byGen)
	}
	if _, ok := w.pendingStops[key]; ok {
		t.Error("expected the entry to be dequeued once the retry confirmed the stop")
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.deletedDeploymentReplicaIdentities) != 1 || st.deletedDeploymentReplicaIdentities[0].pid != 111 {
		t.Errorf("expected exactly one identity delete for pid 111, got %+v", st.deletedDeploymentReplicaIdentities)
	}
}

// TestRetryElasticHibernateStop_GoneIncarnationCountsAsStopped pins that a
// retry whose incarnation has already left the slot treats that as proof of
// exit and finishes the identity half, rather than retrying forever.
func TestRetryElasticHibernateStop_GoneIncarnationCountsAsStopped(t *testing.T) {
	w, mgr, st := newElasticHibernateRetryFixture(&fakeElasticTerminator{})
	mgr.mu.Lock()
	mgr.stopIncarnationErrs = map[replicaKey][]error{{"app", 0}: {process.ErrIncarnationGone}}
	mgr.mu.Unlock()
	w.QueuePendingStop(PendingStopEntry{
		Kind: pendingStopElasticHibernate, Slug: "app", Index: 0,
		AppID: 1, DeploymentID: 5, PID: 111, Incarnation: 7, Native: true,
	})

	w.processPendingStops()

	if _, ok := w.pendingStops[pendingStopKey{slug: "app", index: 0}]; ok {
		t.Error("expected a gone incarnation to count as stopped and dequeue the entry")
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.deletedDeploymentReplicaIdentities) != 1 {
		t.Errorf("expected the identity delete to run, got %+v", st.deletedDeploymentReplicaIdentities)
	}
}

// TestRetryElasticHibernateStop_ConfirmedRetryIsRemembered pins that a retry
// which confirms the stop but then fails the identity delete records the
// confirmation, so the next retry goes straight to the identity half.
func TestRetryElasticHibernateStop_ConfirmedRetryIsRemembered(t *testing.T) {
	w, mgr, st := newElasticHibernateRetryFixture(&fakeElasticTerminator{})
	st.mu.Lock()
	st.deleteDeploymentReplicaIdentityErr = errors.New("database is locked")
	st.mu.Unlock()
	w.QueuePendingStop(PendingStopEntry{
		Kind: pendingStopElasticHibernate, Slug: "app", Index: 0,
		AppID: 1, DeploymentID: 5, PID: 111, Incarnation: 7, Native: true,
	})

	w.processPendingStops() // stop confirmed, identity delete fails
	st.mu.Lock()
	st.deleteDeploymentReplicaIdentityErr = nil
	st.mu.Unlock()
	w.processPendingStops() // identity delete succeeds

	mgr.mu.Lock()
	byGen := append([]stopIncarnationCall(nil), mgr.stopIncarnationCalls...)
	mgr.mu.Unlock()
	if len(byGen) != 1 {
		t.Errorf("expected the stop to be attempted once and then remembered, got %v", byGen)
	}
	if _, ok := w.pendingStops[pendingStopKey{slug: "app", index: 0}]; ok {
		t.Error("expected the entry to be dequeued once the identity delete succeeded")
	}
}
