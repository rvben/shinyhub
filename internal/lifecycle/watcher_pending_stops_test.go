package lifecycle

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/process"
)

// TestWatcher_RetryPendingStop_StillUnconfirmedLeavesEntryQueued is the most
// common tick: the retry is attempted, but the exit still cannot be proven,
// so the entry stays queued for the next tick rather than guessing at the
// outcome or giving up.
func TestWatcher_RetryPendingStop_StillUnconfirmedLeavesEntryQueued(t *testing.T) {
	mgr := &fakeManager{
		stopIncarnationErrs: map[replicaKey][]error{
			{slug: "myapp", index: 0}: {errors.New("replica stop unconfirmed")},
		},
	}
	st := newFakeStore(map[string]*db.App{"myapp": {ID: 1, Slug: "myapp", Status: "running", Replicas: 1}}, nil)
	w := newTestWatcher(Config{}, mgr, newFakeProxy(), st, nil)

	w.QueuePendingStop(PendingStopEntry{
		Slug: "myapp", Index: 0, AppID: 1, PID: 4242, Incarnation: 7,
		Reason: "queued reason", LogRunID: "run-1",
	})

	w.processPendingStops()

	mgr.mu.Lock()
	calls := append([]stopIncarnationCall(nil), mgr.stopIncarnationCalls...)
	releases := append([]stopIncarnationCall(nil), mgr.releaseStopPendingCalls...)
	mgr.mu.Unlock()
	if len(calls) != 1 || calls[0] != (stopIncarnationCall{"myapp", 0, 7}) {
		t.Fatalf("StopReplicaIncarnation calls = %+v, want exactly one for (myapp,0,7)", calls)
	}
	if len(releases) != 0 {
		t.Fatalf("ReleaseStopPending calls = %+v, want none (the exit was never confirmed)", releases)
	}
	st.mu.Lock()
	wroteCalls := len(st.markCrashedClearingIdentityCalls)
	st.mu.Unlock()
	if wroteCalls != 0 {
		t.Fatalf("MarkReplicaCrashedClearingIdentityIfCurrent called %d times, want 0 (nothing to record yet)", wroteCalls)
	}
	if !w.isPendingStop(replicaKey{"myapp", 0}) {
		t.Fatal("entry dequeued despite an unconfirmed retry; it must stay queued for the next tick")
	}
}

// TestWatcher_RetryPendingStop_ConfirmedExitPrefersLastExitAndReleasesFence
// asserts the confirmed-exit path: once StopReplicaIncarnation reports nil,
// the write prefers the exit monitor's own verdict (LastExit) over the
// reason/run recorded at queue time, since the monitor actually observed the
// real exit and the queued values were only a best-effort guess. The fence
// is released and the log run closed only after the store write durably
// succeeds.
func TestWatcher_RetryPendingStop_ConfirmedExitPrefersLastExitAndReleasesFence(t *testing.T) {
	verdictCode := 137
	verdictAt := time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)
	mgr := &fakeManager{
		lastExit: map[replicaKey]process.ExitVerdict{
			{slug: "myapp", index: 0}: {
				Reason: "killed: out of memory", ExitCode: &verdictCode, Signal: "SIGKILL",
				OOMKilled: true, At: verdictAt, RunID: "run-verdict",
			},
		},
	}
	st := newFakeStore(map[string]*db.App{"myapp": {ID: 1, Slug: "myapp", Status: "running", Replicas: 1}}, nil)
	pid, port, deploymentID := 4242, 39000, int64(3)
	st.replicas = map[int64][]*db.Replica{
		1: {{AppID: 1, Index: 0, PID: &pid, Port: &port, EndpointURL: "http://127.0.0.1:39000",
			WorkerID: "4242", DeploymentID: &deploymentID, Status: "running"}},
	}
	w := newTestWatcher(Config{}, mgr, newFakeProxy(), st, nil)

	w.QueuePendingStop(PendingStopEntry{
		Slug: "myapp", Index: 0, AppID: 1, PID: pid, Port: port,
		EndpointURL: "http://127.0.0.1:39000", WorkerID: "4242", DeploymentID: deploymentID,
		Incarnation: 9, Reason: "queued reason (stale)", LogRunID: "run-queued",
	})

	w.processPendingStops()

	mgr.mu.Lock()
	releases := append([]stopIncarnationCall(nil), mgr.releaseStopPendingCalls...)
	mgr.mu.Unlock()
	if len(releases) != 1 || releases[0] != (stopIncarnationCall{"myapp", 0, 9}) {
		t.Fatalf("ReleaseStopPending calls = %+v, want exactly one for (myapp,0,9)", releases)
	}

	st.mu.Lock()
	writes := append([]markCrashedClearingIdentityCall(nil), st.markCrashedClearingIdentityCalls...)
	finished := append([]string(nil), st.finishedLogRuns...)
	st.mu.Unlock()
	if len(writes) != 1 {
		t.Fatalf("MarkReplicaCrashedClearingIdentityIfCurrent called %d times, want 1", len(writes))
	}
	w1 := writes[0]
	wantIdentity := db.ReplicaRuntimeIdentity{PID: pid, Port: port, EndpointURL: "http://127.0.0.1:39000", WorkerID: "4242", DeploymentID: deploymentID}
	if w1.expect != wantIdentity {
		t.Fatalf("expect = %+v, want %+v (the queued identity)", w1.expect, wantIdentity)
	}
	if w1.params.Reason != "killed: out of memory" {
		t.Fatalf("Reason = %q, want the exit verdict's reason to override the stale queued one", w1.params.Reason)
	}
	if w1.params.ExitCode == nil || *w1.params.ExitCode != verdictCode {
		t.Fatalf("ExitCode = %v, want %d", w1.params.ExitCode, verdictCode)
	}
	if w1.params.Signal != "SIGKILL" {
		t.Fatalf("Signal = %q, want SIGKILL", w1.params.Signal)
	}
	if !w1.params.ExitOOMKilled {
		t.Fatal("ExitOOMKilled = false, want true (from the exit verdict)")
	}
	if !w1.params.ExitObservedAt.Equal(verdictAt) {
		t.Fatalf("ExitObservedAt = %v, want %v (the verdict's own timestamp)", w1.params.ExitObservedAt, verdictAt)
	}
	if w1.params.ExitRunID != "run-verdict" {
		t.Fatalf("ExitRunID = %q, want run-verdict (the verdict's own run, not the stale queued one)", w1.params.ExitRunID)
	}
	if len(finished) != 1 || finished[0] != "run-verdict" {
		t.Fatalf("FinishAppLogRunWithExit calls = %v, want exactly [run-verdict]", finished)
	}
	if w.isPendingStop(replicaKey{"myapp", 0}) {
		t.Fatal("entry still queued after a confirmed, durably recorded exit")
	}
}

// TestWatcher_RetryPendingStop_IncarnationGoneUsesQueuedReasonAndRunID pins
// the other confirmation path: ErrIncarnationGone means the slot itself no
// longer holds that incarnation, which is proof of exit even though nothing
// ever observed the process's own exit code or signal. With no exit verdict
// to prefer, the write must fall back to the reason/run recorded at queue
// time rather than fabricate exit facts it does not have.
func TestWatcher_RetryPendingStop_IncarnationGoneUsesQueuedReasonAndRunID(t *testing.T) {
	mgr := &fakeManager{
		stopIncarnationErrs: map[replicaKey][]error{
			{slug: "myapp", index: 0}: {fmt.Errorf("app myapp replica 0 incarnation 5: %w", process.ErrIncarnationGone)},
		},
	}
	st := newFakeStore(map[string]*db.App{"myapp": {ID: 1, Slug: "myapp", Status: "running", Replicas: 1}}, nil)
	pid := 4242
	st.replicas = map[int64][]*db.Replica{
		1: {{AppID: 1, Index: 0, PID: &pid, Status: "running"}},
	}
	w := newTestWatcher(Config{}, mgr, newFakeProxy(), st, nil)

	w.QueuePendingStop(PendingStopEntry{
		Slug: "myapp", Index: 0, AppID: 1, PID: pid, Incarnation: 5,
		Reason: "process did not recover ready", LogRunID: "run-queued",
	})

	w.processPendingStops()

	st.mu.Lock()
	writes := append([]markCrashedClearingIdentityCall(nil), st.markCrashedClearingIdentityCalls...)
	finished := append([]string(nil), st.finishedLogRuns...)
	st.mu.Unlock()
	if len(writes) != 1 {
		t.Fatalf("MarkReplicaCrashedClearingIdentityIfCurrent called %d times, want 1", len(writes))
	}
	w1 := writes[0]
	if w1.params.Reason != "process did not recover ready" {
		t.Fatalf("Reason = %q, want the queued reason (an incarnation-gone confirmation has no exit verdict to prefer)", w1.params.Reason)
	}
	if w1.params.ExitCode != nil {
		t.Fatalf("ExitCode = %v, want nil (an incarnation-gone confirmation never observed an exit code)", w1.params.ExitCode)
	}
	if w1.params.Signal != "" {
		t.Fatalf("Signal = %q, want empty", w1.params.Signal)
	}
	if w1.params.ExitOOMKilled {
		t.Fatal("ExitOOMKilled = true, want false (never observed)")
	}
	if len(finished) != 1 || finished[0] != "run-queued" {
		t.Fatalf("FinishAppLogRunWithExit calls = %v, want exactly [run-queued]", finished)
	}
	if w.isPendingStop(replicaKey{"myapp", 0}) {
		t.Fatal("entry still queued after ErrIncarnationGone, which is itself proof of exit")
	}
}

// TestWatcher_RetryPendingStop_PersistFailureLeavesEntryQueuedAndFenced
// asserts that a confirmed exit whose durable write fails changes nothing
// observable: the manager fence stays held and the entry stays queued, so
// the outcome is retried rather than silently lost.
func TestWatcher_RetryPendingStop_PersistFailureLeavesEntryQueuedAndFenced(t *testing.T) {
	mgr := &fakeManager{}
	st := newFakeStore(map[string]*db.App{"myapp": {ID: 1, Slug: "myapp", Status: "running", Replicas: 1}}, nil)
	st.markCrashedClearingIdentityErr = errors.New("database unavailable")
	w := newTestWatcher(Config{}, mgr, newFakeProxy(), st, nil)

	w.QueuePendingStop(PendingStopEntry{Slug: "myapp", Index: 0, AppID: 1, PID: 4242, Incarnation: 3, Reason: "r", LogRunID: "run-1"})

	w.processPendingStops()

	mgr.mu.Lock()
	releases := len(mgr.releaseStopPendingCalls)
	mgr.mu.Unlock()
	if releases != 0 {
		t.Fatalf("ReleaseStopPending called %d times, want 0 (the fence must survive a failed persist, or the process could be mistaken for reaped)", releases)
	}
	if !w.isPendingStop(replicaKey{"myapp", 0}) {
		t.Fatal("entry dequeued despite a failed persist; it must be retried next tick")
	}
}

// TestWatcher_RetryPendingStop_ObsoleteEntryReleasesFenceWithoutClosingLogRun
// covers the other way MarkReplicaCrashedClearingIdentityIfCurrent can return
// wrote=false with a nil error: a fast restart already reused the row with a
// different PID by the time the retry landed. That is just as much proof
// there is nothing left to retry as a successful write, so the fence is
// still released and the entry dequeued - but since nothing was actually
// written, no log run is closed.
func TestWatcher_RetryPendingStop_ObsoleteEntryReleasesFenceWithoutClosingLogRun(t *testing.T) {
	mgr := &fakeManager{}
	st := newFakeStore(map[string]*db.App{"myapp": {ID: 1, Slug: "myapp", Status: "running", Replicas: 1}}, nil)
	newerPID := 9999
	st.replicas = map[int64][]*db.Replica{
		1: {{AppID: 1, Index: 0, PID: &newerPID, Status: "running"}},
	}
	w := newTestWatcher(Config{}, mgr, newFakeProxy(), st, nil)

	w.QueuePendingStop(PendingStopEntry{Slug: "myapp", Index: 0, AppID: 1, PID: 4242, Incarnation: 3, Reason: "r", LogRunID: "run-1"})

	w.processPendingStops()

	mgr.mu.Lock()
	releases := len(mgr.releaseStopPendingCalls)
	mgr.mu.Unlock()
	if releases != 1 {
		t.Fatalf("ReleaseStopPending called %d times, want 1 (a mismatched PID means the row already moved on)", releases)
	}
	st.mu.Lock()
	finished := len(st.finishedLogRuns)
	st.mu.Unlock()
	if finished != 0 {
		t.Fatalf("FinishAppLogRunWithExit called %d times, want 0 (nothing was actually written)", finished)
	}
	if w.isPendingStop(replicaKey{"myapp", 0}) {
		t.Fatal("entry still queued despite the row having already moved on")
	}
}

// TestWatcher_RunOnce_PendingStopFencesManagerCrashHandling proves the first
// of the two fencing sites: a manager entry reported StatusCrashed for a slot
// under an unresolved pending stop must not be driven through the ordinary
// restart path, which writes an unconditional RecordReplicaCrash that would
// race the pending-stop retry's own atomic, PID-conditional write.
func TestWatcher_RunOnce_PendingStopFencesManagerCrashHandling(t *testing.T) {
	mgr := &fakeManager{
		entries: []*process.ProcessInfo{{Slug: "myapp", Index: 0, Status: process.StatusCrashed}},
		stopIncarnationErrs: map[replicaKey][]error{
			{slug: "myapp", index: 0}: {errors.New("replica stop unconfirmed")},
		},
	}
	st := newFakeStore(
		map[string]*db.App{"myapp": {ID: 1, Slug: "myapp", Status: "running", Replicas: 1}},
		[]*db.Deployment{{BundleDir: "/bundles/v1"}},
	)
	var deployed []string
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, mgr, newFakeProxy(), st,
		func(slug, bundleDir string, idx int) (*deploy.Result, error) {
			deployed = append(deployed, slug)
			return &deploy.Result{Index: idx, PID: 11, Port: 20011}, nil
		})
	w.QueuePendingStop(PendingStopEntry{Slug: "myapp", Index: 0, AppID: 1, PID: 4242, Incarnation: 1, Reason: "r"})

	w.runOnce()

	if len(deployed) != 0 {
		t.Fatalf("deployFn called %v, want none: a crashed manager entry under a pending stop must not be driven through restart", deployed)
	}
	mgr.mu.Lock()
	calls := len(mgr.stopIncarnationCalls)
	mgr.mu.Unlock()
	if calls != 1 {
		t.Fatalf("StopReplicaIncarnation called %d times, want 1 (runOnce must still retry the pending stop this tick)", calls)
	}
	if !w.isPendingStop(replicaKey{"myapp", 0}) {
		t.Fatal("entry dequeued despite an unconfirmed retry")
	}
}

// TestWatcher_RunOnce_PendingStopFencesReconcileReplicas proves the second
// fencing site: a "crashed" DB replica row under an unresolved pending stop
// must not be picked up by reconcileReplicas and restarted from underneath
// the retry, which is still trying to confirm whether the old process is
// really gone.
func TestWatcher_RunOnce_PendingStopFencesReconcileReplicas(t *testing.T) {
	mgr := &fakeManager{
		entries: []*process.ProcessInfo{{Slug: "myapp", Index: 0, Status: process.StatusRunning}},
		stopIncarnationErrs: map[replicaKey][]error{
			{slug: "myapp", index: 1}: {errors.New("replica stop unconfirmed")},
		},
	}
	st := newFakeStore(
		map[string]*db.App{"myapp": {ID: 1, Slug: "myapp", Status: "running", Replicas: 2}},
		[]*db.Deployment{{BundleDir: "/bundles/v1"}},
	)
	pid0, port0 := 10, 20010
	st.replicas = map[int64][]*db.Replica{
		1: {
			{AppID: 1, Index: 0, PID: &pid0, Port: &port0, Status: "running"},
			{AppID: 1, Index: 1, Status: "crashed"},
		},
	}
	var deployedIdx []int
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, mgr, newFakeProxy(), st,
		func(slug, bundleDir string, idx int) (*deploy.Result, error) {
			deployedIdx = append(deployedIdx, idx)
			return &deploy.Result{Index: idx, PID: 11, Port: 20011}, nil
		})
	w.QueuePendingStop(PendingStopEntry{Slug: "myapp", Index: 1, AppID: 1, PID: 99999, Incarnation: 2, Reason: "r"})

	w.runOnce()

	if len(deployedIdx) != 0 {
		t.Fatalf("deployFn called for index(es) %v, want none: a crashed DB row under a pending stop must not be restarted from underneath the retry", deployedIdx)
	}
	mgr.mu.Lock()
	calls := len(mgr.stopIncarnationCalls)
	mgr.mu.Unlock()
	if calls != 1 {
		t.Fatalf("StopReplicaIncarnation called %d times, want 1 (runOnce must still retry the pending stop this tick)", calls)
	}
}
