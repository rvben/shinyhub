package lifecycle

import (
	"errors"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/process"
)

// newTestWatcherWithManager mirrors newTestWatcher but accepts the manager
// interface directly, so a test can wire a real *process.Manager instead of
// fakeManager when it needs genuine incarnation/stop semantics.
func newTestWatcherWithManager(cfg Config, mgr manager, prx *fakeProxy, st *fakeStore,
	deployFn func(slug, bundleDir string, index int) (*deploy.Result, error)) *Watcher {
	return &Watcher{
		cfg:           cfg,
		mgr:           mgr,
		prx:           prx,
		store:         st,
		deploy:        deployFn,
		attempts:      make(map[replicaKey]int),
		nextRetry:     make(map[replicaKey]time.Time),
		crashCount:    make(map[replicaKey]int),
		lastCrash:     make(map[replicaKey]time.Time),
		lostForgiven:  make(map[replicaKey]bool),
		driving:       make(map[string]bool),
		expandingWarm: make(map[string]bool),
	}
}

// TestWake_PanicAfterStartConfirmedStopLeavesNoManagerEntry proves the
// per-replica wake goroutine's recover (watcher.go, inside driveWakingApp's
// boot loop) does two things when a panic lands after wakeReplica started a
// process but before persistence: it must not crash the process (the panic
// happens in a child goroutine, so a recover in the caller cannot catch it),
// and it must stop the process it just started with confirmed semantics
// rather than leaving a live orphan the app's "hibernated" status no longer
// points to. A real *process.Manager is used (not fakeManager) so the
// manager-entry assertion below reflects genuine Stop/incarnation bookkeeping
// rather than a fake that never removes entries.
//
// Failing-first: with the per-replica recover removed (watcher.go, the
// `defer func() { if r := recover(); r != nil { ... } }()` immediately after
// wakeReplica returns), this panic is never caught in the goroutine that
// raised it, and the whole test binary crashes. That was verified by
// temporarily deleting the recover block and running this test alone via
// `GOWORK=off go test ./internal/lifecycle/ -run
// TestWake_PanicAfterStartConfirmedStopLeavesNoManagerEntry`: the process
// exited non-zero with "panic: boom-after-wake-start" in stderr and no test
// output at all (the binary died before reporting PASS/FAIL). The recover
// was restored immediately after capturing that output.
func TestWake_PanicAfterStartConfirmedStopLeavesNoManagerEntry(t *testing.T) {
	mgr := process.NewManager(t.TempDir(), &resumeConfirmingRuntime{})
	prx := newFakeProxy()
	st := newFakeStore(
		map[string]*db.App{"wakeapp": {ID: 1, Slug: "wakeapp", Status: "hibernated", Replicas: 1}},
		[]*db.Deployment{{ID: 5, Version: "v1", BundleDir: "/bundles/v1"}},
	)
	w := newTestWatcherWithManager(Config{}, mgr, prx, st,
		func(slug, dir string, idx int) (*deploy.Result, error) {
			// Stands in for the real deploy path's mgr.Start: registers a live
			// manager entry before the wake's persistence step runs, exactly
			// like the process the panic below must not leave untracked.
			mgr.Adopt(slug, process.ProcessInfo{
				Slug: slug, Index: idx, Status: process.StatusRunning,
				AppID: 1, DeploymentID: 5, PID: 6060 + idx,
			}, process.RunHandle{PID: 6060 + idx})
			return &deploy.Result{Index: idx, PID: 6060 + idx, Port: 21000 + idx, EndpointURL: "http://replica-0", WorkerID: "worker-0"}, nil
		})
	w.testPanicAfterWakeStart = func(int) { panic("boom-after-wake-start") }

	w.WakeTrigger("wakeapp") // must not crash the test binary
	w.wakeWG.Wait()

	// The confirmed stop's crash-mark is conditioned on the row identity the
	// wake persists for this start, so it cannot land on a replacement.
	st.mu.Lock()
	marks := append([]markCrashedClearingIdentityCall(nil), st.markCrashedClearingIdentityCalls...)
	st.mu.Unlock()
	wantRow := db.ReplicaRuntimeIdentity{PID: 6060, Port: 21000, EndpointURL: "http://replica-0", WorkerID: "worker-0", DeploymentID: 5}
	if len(marks) != 1 || marks[0].expect != wantRow {
		t.Errorf("crash-mark calls = %+v, want one conditioned on %+v", marks, wantRow)
	}

	if _, ok := mgr.GetReplica("wakeapp", 0); ok {
		t.Error("manager still holds a live entry for the replica the panic started; want it stopped and removed")
	}
	st.mu.Lock()
	status := st.apps["wakeapp"].Status
	st.mu.Unlock()
	if status != "hibernated" {
		t.Errorf("app status = %q, want fail-closed hibernated (not left running over a stopped process)", status)
	}
}

// TestWake_PanicAfterStartUnconfirmedStopQueuesAndFencesIndex covers the
// other half of the same panic site: when the confirmed stop cannot be
// proven, the started replica's identity must be queued into pendingStops
// (round-10 of the plan) rather than dropped, and that queued index must be
// fenced away from a subsequent wake's boot loop until a later retry proves
// the exit. fakeManager's scripted StopReplicaIncarnation lets this be driven
// deterministically: unconfirmed on the first call (the one the panic
// triggers), confirmed on the retry.
//
// Failing-first: before the round-10 fix, stopStartedWakeReplicas (or its
// predecessor, a blind mgr.Stop(slug) call) never queued anything on an
// unconfirmed stop. Reverting stopStartedWakeReplicas's unconfirmed branch to
// skip QueuePendingStop (just `continue`) and re-running this test in
// isolation reproduces that: isPendingStop is false right after the panic, so
// nothing fences the second WakeTrigger, and it starts a second process on
// the same index while the first may still be alive - asserted below as
// deployCalls containing index 0 twice with the fix reverted. This does not
// crash the process (no panic in this path), so it runs directly rather than
// through a re-exec'd subprocess.
func TestWake_PanicAfterStartUnconfirmedStopQueuesAndFencesIndex(t *testing.T) {
	const slug = "wakeapp2"
	mgr := &fakeManager{
		stopIncarnationErrs: map[replicaKey][]error{
			{slug, 0}: {errors.New("still running: sigterm not yet confirmed")},
		},
	}
	prx := newFakeProxy()
	st := newFakeStore(
		map[string]*db.App{slug: {ID: 9, Slug: slug, Status: "hibernated", Replicas: 1}},
		[]*db.Deployment{{ID: 4, Version: "v1", BundleDir: "/bundles/v1"}},
	)
	var deployCalls []int
	panicNext := true
	w := newTestWatcher(Config{}, mgr, prx, st,
		func(s, dir string, idx int) (*deploy.Result, error) {
			deployCalls = append(deployCalls, idx)
			mgr.mu.Lock()
			mgr.entries = append(mgr.entries, &process.ProcessInfo{Slug: s, Index: idx, PID: 7070 + idx, Status: process.StatusRunning})
			mgr.mu.Unlock()
			return &deploy.Result{Index: idx, PID: 7070 + idx, Port: 22000 + idx, EndpointURL: "http://replica-0", WorkerID: "worker-0"}, nil
		})
	w.testPanicAfterWakeStart = func(int) {
		if panicNext {
			panicNext = false
			panic("boom-after-wake-start")
		}
	}

	w.WakeTrigger(slug)
	w.wakeWG.Wait()

	key := replicaKey{slug, 0}
	if !w.isPendingStop(key) {
		t.Fatal("expected the unconfirmed stop to queue a pendingStops entry for index 0")
	}
	st.mu.Lock()
	status := st.apps[slug].Status
	st.mu.Unlock()
	if status != "hibernated" {
		t.Fatalf("app status = %q, want fail-closed hibernated", status)
	}

	// A second wake attempt must skip the fenced index rather than starting a
	// competing process on the same slot. With every slot fenced nothing boots,
	// but nothing failed either: the wake must stay retryable (reverted to
	// hibernated) rather than be recorded as a crash, since the queued stop
	// may confirm on the next tick.
	resetToHibernated := func() {
		st.mu.Lock()
		st.apps[slug].Status = "hibernated"
		st.appStatus[slug] = "hibernated"
		st.mu.Unlock()
	}
	resetToHibernated()
	w.WakeTrigger(slug)
	w.wakeWG.Wait()
	if len(deployCalls) != 1 {
		t.Fatalf("deploy calls = %v, want exactly one (index 0 fenced by the pending stop)", deployCalls)
	}
	st.mu.Lock()
	status = st.apps[slug].Status
	st.mu.Unlock()
	if status != "hibernated" {
		t.Fatalf("app status after a wake with every slot fenced = %q, want hibernated (retryable, not crashed)", status)
	}

	// The retry now confirms the stop (scripted errs exhausted -> nil).
	w.processPendingStops()
	if w.isPendingStop(key) {
		t.Fatal("expected the entry to be dequeued once the retry confirmed the stop")
	}

	// A third wake attempt may now start index 0 again.
	resetToHibernated()
	w.WakeTrigger(slug)
	w.wakeWG.Wait()
	if len(deployCalls) != 2 {
		t.Fatalf("deploy calls = %v, want a second start for index 0 once the fence cleared", deployCalls)
	}
}

// TestWake_PanicAfterStartQueuedStopCarriesLogRunID pins that a wake-abort
// stop that could not be confirmed queues the started replica's log run ID,
// so the retry that later confirms the exit can close that run instead of
// leaving it open forever.
func TestWake_PanicAfterStartQueuedStopCarriesLogRunID(t *testing.T) {
	const slug = "wakeapp3"
	mgr := &fakeManager{
		stopIncarnationErrs: map[replicaKey][]error{
			{slug, 0}: {errors.New("still running: sigterm not yet confirmed")},
		},
	}
	st := newFakeStore(
		map[string]*db.App{slug: {ID: 9, Slug: slug, Status: "hibernated", Replicas: 1}},
		[]*db.Deployment{{ID: 4, Version: "v1", BundleDir: "/bundles/v1"}},
	)
	w := newTestWatcher(Config{}, mgr, newFakeProxy(), st,
		func(s, dir string, idx int) (*deploy.Result, error) {
			mgr.mu.Lock()
			mgr.entries = append(mgr.entries, &process.ProcessInfo{Slug: s, Index: idx, PID: 7070 + idx, Status: process.StatusRunning, LogRunID: "run-wake"})
			mgr.mu.Unlock()
			return &deploy.Result{Index: idx, PID: 7070 + idx, Port: 22000 + idx, EndpointURL: "http://replica-0", WorkerID: "worker-0"}, nil
		})
	w.testPanicAfterWakeStart = func(int) { panic("boom-after-wake-start") }

	w.WakeTrigger(slug)
	w.wakeWG.Wait()

	w.mu.Lock()
	var got []PendingStopEntry
	for _, e := range w.pendingStops {
		got = append(got, e)
	}
	w.mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("pending stops = %v, want exactly one for the unconfirmed wake-abort stop", got)
	}
	if got[0].LogRunID != "run-wake" {
		t.Fatalf("queued LogRunID = %q, want %q (the started replica's run)", got[0].LogRunID, "run-wake")
	}
	// The retry may only crash-mark the row the wake persisted for this
	// start, so it must carry that whole row identity, not just the PID.
	want := db.ReplicaRuntimeIdentity{PID: 7070, Port: 22000, EndpointURL: "http://replica-0", WorkerID: "worker-0", DeploymentID: 4}
	if id := got[0].replicaIdentity(); id != want {
		t.Fatalf("queued identity = %+v, want %+v", id, want)
	}
}
