package process_test

import (
	"errors"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/process"
)

// waitForReplicaStopPending polls until the manager's exit-monitor goroutine
// has processed the replica's exit (Status no longer reports it Running), so
// a test can deterministically observe the stopPending-fence window instead
// of racing the monitor goroutine's scheduling.
func waitForReplicaStopPending(t *testing.T, m *process.Manager, params process.StartParams) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status, err := m.Status(params.Slug)
		if err != nil {
			t.Fatalf("Status while waiting for the exit monitor: %v", err)
		}
		if status.Status != process.StatusRunning {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("exit monitor never flipped the replica's status away from Running")
}

// TestManager_StopReplicaConfirmed_UnconfirmedStopFencesStartUntilReleased
// drives the real Manager (not a fake) through the full incarnation-fencing
// lifecycle that recovery and the watcher's pending-stop retry loop rely on:
// a stop whose signal is rejected leaves the slot stopPending rather than
// vacated, Start refuses to replace it while pending, a retry targeted at
// the captured incarnation can still confirm the exit once it is observed,
// and only ReleaseStopPending actually frees the slot for reuse. This models
// a CLAIMED fence: the test claims it (ClaimStopPending) right after the
// failed stop, exactly as recovery.go and the watcher's
// stopStartedWakeReplicas do before queueing a retry, so the exit monitor
// leaves the fence for ReleaseStopPending instead of clearing it itself. An
// UNCLAIMED fence (no retry queue involved) behaves differently - see
// TestManager_UnclaimedStopPending_* in manager_stoppending_fence_test.go.
func TestManager_StopReplicaConfirmed_UnconfirmedStopFencesStartUntilReleased(t *testing.T) {
	rt := newSignalFailRuntime()
	m := process.NewManager(t.TempDir(), rt)

	params := process.StartParams{Slug: "demo", Index: 0, Command: []string{"x"}, Port: 1}
	info, err := m.Start(params)
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	genBeforeStop, pidBeforeStop, ok := m.ReplicaIncarnation("demo", 0)
	if !ok || pidBeforeStop != info.PID {
		t.Fatalf("ReplicaIncarnation before stop = gen=%d pid=%d ok=%v, want pid=%d ok=true", genBeforeStop, pidBeforeStop, ok, info.PID)
	}

	// The runtime rejects the signal, so the stop cannot be confirmed. A
	// requireConfirmed caller must leave the slot fenced rather than either
	// vacating it (which would let a replacement Start collide with the still
	// -live process) or clearing the intentional-stop mark blindly.
	if err := m.StopReplicaConfirmed("demo", 0); err == nil {
		t.Fatal("StopReplicaConfirmed returned nil despite the signal being rejected")
	} else if errors.Is(err, process.ErrReplicaNotFound) || errors.Is(err, process.ErrIncarnationGone) {
		t.Fatalf("StopReplicaConfirmed error = %v, want the raw signal failure, not a not-found/gone sentinel", err)
	}

	// The entry must still be tracked under its original incarnation: nothing
	// observed this process exit yet.
	genPending, pidPending, ok := m.ReplicaIncarnation("demo", 0)
	if !ok || genPending != genBeforeStop || pidPending != info.PID {
		t.Fatalf("ReplicaIncarnation after unconfirmed stop = gen=%d pid=%d ok=%v, want gen=%d pid=%d ok=true", genPending, pidPending, ok, genBeforeStop, info.PID)
	}

	// Claim the fence, exactly as a retry queue does before it enqueues an
	// entry (recovery.go's recovery-unready site, watcher.go's
	// stopStartedWakeReplicas). Without this claim the fence is unclaimed and
	// the exit monitor below would clear stopPending itself once it observes
	// the exit, which is a different, deliberately unfenced lifecycle covered
	// by manager_stoppending_fence_test.go.
	if !m.ClaimStopPending("demo", 0, genBeforeStop) {
		t.Fatal("ClaimStopPending returned false for the incarnation just captured")
	}

	// The manager still considers the process genuinely running (nothing has
	// observed an exit yet), so Start refuses it as already running - the
	// ordinary ErrReplicaAlreadyRunning reason, distinct from the stopPending
	// fence checked below.
	if _, err := m.Start(params); !errors.Is(err, process.ErrReplicaAlreadyRunning) {
		t.Fatalf("Start immediately after an unconfirmed stop error = %v, want errors.Is(..., ErrReplicaAlreadyRunning)", err)
	}

	// The process now actually exits on its own (simulating the retry's
	// signal eventually landing, or the process dying for an unrelated
	// reason). The background exit-monitor goroutine observes this
	// independently of any retry and flips the entry's status away from
	// Running, so Start's refusal reason changes to the stopPending fence -
	// exactly the window ReleaseStopPending exists to close.
	rt.triggerExit(info.PID)
	waitForReplicaStopPending(t, m, params)

	// The fence is claimed, so the exit monitor must leave stopPending set
	// even though it just proved the process exited - only ReleaseStopPending
	// may clear a claimed fence. Start's refusal reason must therefore still
	// be the stopPending fence, not have silently reverted to unfenced.
	if _, err := m.Start(params); !errors.Is(err, process.ErrReplicaStopPending) {
		t.Fatalf("Start right after the monitor observed the exit = %v, want errors.Is(..., ErrReplicaStopPending): a claimed fence must not be auto-cleared", err)
	}

	// A retry scoped to the captured incarnation must still be able to
	// confirm the exit via the non-blocking pre-check, since the monitor
	// already observed it.
	if err := m.StopReplicaIncarnation("demo", 0, genBeforeStop); err != nil {
		t.Fatalf("StopReplicaIncarnation after the process exited: %v", err)
	}

	// Confirming the exit does not itself free the slot: only the pending
	// -stop owner's ReleaseStopPending may do that, so nothing can reuse the
	// slot before the retry queue has durably recorded the outcome.
	genAfterConfirm, pidAfterConfirm, ok := m.ReplicaIncarnation("demo", 0)
	if !ok || genAfterConfirm != genBeforeStop || pidAfterConfirm != info.PID {
		t.Fatalf("ReplicaIncarnation after confirmed-but-unreleased stop = gen=%d pid=%d ok=%v, want gen=%d pid=%d ok=true", genAfterConfirm, pidAfterConfirm, ok, genBeforeStop, info.PID)
	}
	if _, err := m.Start(params); !errors.Is(err, process.ErrReplicaStopPending) {
		t.Fatalf("Start before ReleaseStopPending error = %v, want errors.Is(..., ErrReplicaStopPending)", err)
	}

	m.ReleaseStopPending("demo", 0, genBeforeStop)

	if _, _, ok := m.ReplicaIncarnation("demo", 0); ok {
		t.Fatal("ReplicaIncarnation still reports an occupant after ReleaseStopPending")
	}
	if _, err := m.Start(params); err != nil {
		t.Fatalf("Start after ReleaseStopPending: %v", err)
	}
}

// TestManager_StopReplicaIncarnation_StaleGenerationLeavesReplacementUntouched
// proves that a retry racing a slot that has since been cleanly stopped and
// replaced reports ErrIncarnationGone for the stale incarnation it targeted,
// without touching the new occupant in any way: the mismatch check on entry
// is the only thing that runs, never the stop signal/wait machinery.
func TestManager_StopReplicaIncarnation_StaleGenerationLeavesReplacementUntouched(t *testing.T) {
	rt := newFakeRuntime()
	m := process.NewManager(t.TempDir(), rt)

	params := process.StartParams{
		Slug: "demo", Index: 0, Dir: t.TempDir(), Command: []string{"app"}, Port: 19000,
	}
	first, err := m.Start(params)
	if err != nil {
		t.Fatalf("start first: %v", err)
	}
	staleGen, _, ok := m.ReplicaIncarnation("demo", 0)
	if !ok {
		t.Fatal("ReplicaIncarnation missing for the just-started replica")
	}

	// fakeRuntime confirms the exit as soon as Signal delivers it, so this is
	// a clean, fully confirmed stop: finalizeConfirmedStop vacates the slot
	// outright (it never sets stopPending).
	if err := m.StopReplicaConfirmed("demo", 0); err != nil {
		t.Fatalf("stop first: %v", err)
	}

	second, err := m.Start(params)
	if err != nil {
		t.Fatalf("start replacement: %v", err)
	}
	if second.PID == first.PID {
		t.Fatalf("replacement PID=%d equals original PID; test needs distinct incarnations", second.PID)
	}
	freshGen, freshPID, ok := m.ReplicaIncarnation("demo", 0)
	if !ok || freshPID != second.PID || freshGen == staleGen {
		t.Fatalf("ReplicaIncarnation after replacement = gen=%d pid=%d ok=%v, want a fresh gen (!= %d) and pid=%d", freshGen, freshPID, ok, staleGen, second.PID)
	}

	// A retry still holding the ORIGINAL incarnation must not touch the
	// replacement: it targets a process that, from the retry's point of view,
	// is simply gone.
	err = m.StopReplicaIncarnation("demo", 0, staleGen)
	if !errors.Is(err, process.ErrIncarnationGone) {
		t.Fatalf("StopReplicaIncarnation(stale) error = %v, want errors.Is(..., ErrIncarnationGone)", err)
	}

	genAfter, pidAfter, ok := m.ReplicaIncarnation("demo", 0)
	if !ok || genAfter != freshGen || pidAfter != second.PID {
		t.Fatalf("replacement disturbed by a stale-incarnation stop: gen=%d pid=%d ok=%v, want gen=%d pid=%d ok=true", genAfter, pidAfter, ok, freshGen, second.PID)
	}
	status, err := m.Status("demo")
	if err != nil || status.Status != process.StatusRunning || status.PID != second.PID {
		t.Fatalf("replacement status after stale-incarnation stop = %+v err=%v, want Running pid=%d", status, err, second.PID)
	}

	if err := m.StopReplicaConfirmed("demo", 0); err != nil {
		t.Fatalf("cleanup replacement: %v", err)
	}
}
