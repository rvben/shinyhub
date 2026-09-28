package process_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/process"
)

// failOnceThenConfirmRuntime is a Runtime whose very first Signal call fails
// (as if the initial SIGTERM delivery hit a transient signal(2) error, e.g.
// EPERM) and every later Signal call succeeds; the single background Wait
// spawned for the entry returns nil (a confirmed exit) as soon as any Signal
// has succeeded. It models a caller that retries StopReplicaConfirmed by
// slug/index alone with no incarnation targeting and no ClaimStopPending -
// exactly the pattern internal/lifecycle/watcher.go's
// retryElasticHibernateStop and retryElasticRecoveryStop use.
type failOnceThenConfirmRuntime struct {
	mu      sync.Mutex
	calls   int
	nextPID int
	exited  chan struct{}
	once    sync.Once
}

func newFailOnceThenConfirmRuntime() *failOnceThenConfirmRuntime {
	return &failOnceThenConfirmRuntime{nextPID: 80000, exited: make(chan struct{})}
}

func (r *failOnceThenConfirmRuntime) Start(_ context.Context, p process.StartParams, _ io.Writer) (process.ReplicaEndpoint, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextPID++
	pid := r.nextPID
	return process.ReplicaEndpoint{
		URL:      fmt.Sprintf("http://127.0.0.1:%d", p.Port),
		Provider: "native",
		WorkerID: strconv.Itoa(pid),
		Handle:   process.RunHandle{PID: pid},
	}, nil
}

func (r *failOnceThenConfirmRuntime) Signal(process.RunHandle, syscall.Signal) error {
	r.mu.Lock()
	r.calls++
	n := r.calls
	r.mu.Unlock()
	if n == 1 {
		return errors.New("operation not permitted")
	}
	// A later Signal call delivered the SIGTERM the first call could not:
	// unblock the paired Wait so it reports a confirmed exit.
	r.once.Do(func() { close(r.exited) })
	return nil
}

func (r *failOnceThenConfirmRuntime) Wait(_ context.Context, _ process.RunHandle) error {
	<-r.exited
	return nil
}

func (r *failOnceThenConfirmRuntime) Stats(context.Context, process.RunHandle) (*float64, uint64, error) {
	return nil, 0, nil
}
func (r *failOnceThenConfirmRuntime) RunOnce(context.Context, process.StartParams, io.Writer) (process.ExitInfo, error) {
	return process.ExitInfo{}, nil
}
func (r *failOnceThenConfirmRuntime) HostPreparesDeps() bool    { return false }
func (r *failOnceThenConfirmRuntime) AppBindHost() string       { return "127.0.0.1" }
func (r *failOnceThenConfirmRuntime) HostProvidesAppData() bool { return false }

// TestManager_UnclaimedStopPending_RetrySucceedsAndFreesSlot proves the core
// fix: a stopPending fence that no retry queue ever claimed must not persist
// forever. A confirmed stop that first fails to signal, then succeeds on a
// plain retry (the exact calling convention of
// internal/lifecycle/watcher.go's retryElasticHibernateStop and
// retryElasticRecoveryStop, and of internal/lifecycle/elastic.go's hibernate
// retry - none of which ever call ClaimStopPending), must remove the manager
// entry so the slot is immediately reusable.
//
// Failing-first: before the fix, finalizeConfirmedStop removed an entry only
// when !e.stopPending. The first failed stop sets stopPending=true and
// nothing ever cleared it (only ReleaseStopPending did, and no caller here
// ever calls it), so the second, successful StopReplicaConfirmed left the
// entry in place, and the following Start returned ErrReplicaStopPending
// forever - the exact defect this test asserts against.
func TestManager_UnclaimedStopPending_RetrySucceedsAndFreesSlot(t *testing.T) {
	rt := newFailOnceThenConfirmRuntime()
	m := process.NewManager(t.TempDir(), rt)
	m.SetStopGrace(20 * time.Millisecond)

	params := process.StartParams{Slug: "elastic-demo", Index: 0, Command: []string{"x"}, Port: 1}
	if _, err := m.Start(params); err != nil {
		t.Fatalf("start: %v", err)
	}

	// First attempt: the signal is rejected, so the stop cannot be confirmed.
	// No ClaimStopPending call follows - this fence is unclaimed, matching an
	// elastic-hibernate or elastic-recovery retry queue entry.
	if err := m.StopReplicaConfirmed("elastic-demo", 0); err == nil {
		t.Fatal("StopReplicaConfirmed returned nil despite the signal being rejected")
	}

	// Second attempt (the retry): the signal now succeeds and the process
	// confirms its exit. finalizeConfirmedStop must remove the entry outright
	// since nothing ever claimed the fence.
	if err := m.StopReplicaConfirmed("elastic-demo", 0); err != nil {
		t.Fatalf("StopReplicaConfirmed retry: %v", err)
	}

	if _, _, ok := m.ReplicaIncarnation("elastic-demo", 0); ok {
		t.Fatal("manager still holds an entry after the unclaimed fence's retry confirmed the stop")
	}
	if _, err := m.Start(params); err != nil {
		t.Fatalf("Start after the unclaimed fence's retry confirmed the stop: %v", err)
	}
}

// TestManager_UnclaimedStopPending_IndependentExitFreesSlotForStart covers
// the second way an unclaimed stop can resolve: rather than a caller
// retrying StopReplicaConfirmed, the process exits on its own and the
// background exit-monitor goroutine is the only thing that ever observes it.
// Since no retry queue claimed the fence, the monitor must clear stopPending
// itself - otherwise nothing ever would, and Start would refuse the slot
// forever even though the process is provably gone.
//
// Failing-first: before the fix, the exit monitor never touched stopPending
// at all. The independent exit left stopPending=true in place, so Start kept
// returning ErrReplicaStopPending even though the monitor had already proven
// the process exited - the exact defect this test asserts against.
func TestManager_UnclaimedStopPending_IndependentExitFreesSlotForStart(t *testing.T) {
	rt := newSignalFailRuntime()
	m := process.NewManager(t.TempDir(), rt)

	params := process.StartParams{Slug: "elastic-demo2", Index: 0, Command: []string{"x"}, Port: 1}
	info, err := m.Start(params)
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	if err := m.StopReplicaConfirmed("elastic-demo2", 0); err == nil {
		t.Fatal("StopReplicaConfirmed returned nil despite the signal being rejected")
	}
	// No ClaimStopPending call: this fence is unclaimed.

	// The process exits on its own; nothing ever retries the stop.
	rt.triggerExit(info.PID)
	waitForReplicaStopPending(t, m, params)

	if _, err := m.Start(params); err != nil {
		t.Fatalf("Start after an unclaimed fence's independent exit was observed: %v", err)
	}
}

// TestManager_ClaimedStopPending_SurvivesStopReplicaConfirmedUntilReleased
// proves the other half of the claimed-fence contract test (d) requires:
// once a retry queue has claimed the fence, neither the exit monitor nor a
// second, successful StopReplicaConfirmed call by that same plain
// slug/index calling convention (as opposed to the incarnation-targeted
// StopReplicaIncarnation covered by
// TestManager_StopReplicaConfirmed_UnconfirmedStopFencesStartUntilReleased)
// may free the slot - only ReleaseStopPending may.
//
// Failing-first: before the fix, finalizeConfirmedStop's removal condition
// ignored stopPendingClaimed entirely (mutating it to `!e.stopPending` alone
// reproduces the pre-fix code and is exercised directly by the mutation
// check in the fixup commit's test run, not by reverting this test).
func TestManager_ClaimedStopPending_SurvivesStopReplicaConfirmedUntilReleased(t *testing.T) {
	rt := newFailOnceThenConfirmRuntime()
	m := process.NewManager(t.TempDir(), rt)
	m.SetStopGrace(20 * time.Millisecond)

	params := process.StartParams{Slug: "elastic-demo3", Index: 0, Command: []string{"x"}, Port: 1}
	if _, err := m.Start(params); err != nil {
		t.Fatalf("start: %v", err)
	}
	gen, _, ok := m.ReplicaIncarnation("elastic-demo3", 0)
	if !ok {
		t.Fatal("ReplicaIncarnation missing for the just-started replica")
	}

	if err := m.StopReplicaConfirmed("elastic-demo3", 0); err == nil {
		t.Fatal("StopReplicaConfirmed returned nil despite the signal being rejected")
	}
	if !m.ClaimStopPending("elastic-demo3", 0, gen) {
		t.Fatal("ClaimStopPending returned false for the incarnation just captured")
	}

	// The retry now succeeds and confirms the exit, but the entry must
	// survive because the fence is claimed: only ReleaseStopPending may
	// remove it.
	if err := m.StopReplicaConfirmed("elastic-demo3", 0); err != nil {
		t.Fatalf("StopReplicaConfirmed retry: %v", err)
	}
	if _, _, ok := m.ReplicaIncarnation("elastic-demo3", 0); !ok {
		t.Fatal("claimed entry removed by a confirmed StopReplicaConfirmed retry; want it left for ReleaseStopPending")
	}
	if _, err := m.Start(params); !errors.Is(err, process.ErrReplicaStopPending) {
		t.Fatalf("Start before ReleaseStopPending error = %v, want errors.Is(..., ErrReplicaStopPending)", err)
	}

	m.ReleaseStopPending("elastic-demo3", 0, gen)

	if _, _, ok := m.ReplicaIncarnation("elastic-demo3", 0); ok {
		t.Fatal("ReplicaIncarnation still reports an occupant after ReleaseStopPending")
	}
	if _, err := m.Start(params); err != nil {
		t.Fatalf("Start after ReleaseStopPending: %v", err)
	}
}

// raceExitBeforeSignalRuntime reproduces a specific interleaving that the
// other unclaimed-fence tests in this file do not reach: the process exits
// independently WHILE a StopReplicaConfirmed call is already past its
// non-blocking pre-check (which found the entry not yet exited) but has not
// yet delivered its SIGTERM. The one-and-only exit monitor spawned by Start
// observes and fully processes that independent exit - including its own
// conditional stopPending auto-clear (manager.go's exit-monitor block) -
// while stopPending is STILL false, so the auto-clear has nothing to do.
// Only afterward does the in-flight SIGTERM actually land, on what is by
// then a dead handle, and fail. No monitor will ever revisit this entry, so
// a stopPending fence set here would never be cleared, and a caller that
// does not retry (scale-down, a schedule roll) would leave the slot
// refusing every later Start.
//
// Signal (called once, synchronously, by the first StopReplicaConfirmed) is
// what drives this: it triggers the independent exit and then polls Status
// until it has left StatusRunning, proving under the manager's own lock that
// the monitor's full bookkeeping (not just Wait returning) has completed,
// before reporting the SIGTERM as failed. mgr/slug are wired in after
// construction since the manager does not exist yet when the runtime does.
type raceExitBeforeSignalRuntime struct {
	t    *testing.T
	mgr  *process.Manager
	slug string

	mu         sync.Mutex
	calls      int
	nextPID    int
	exited     chan struct{}
	exitedOnce sync.Once
}

func newRaceExitBeforeSignalRuntime(t *testing.T) *raceExitBeforeSignalRuntime {
	return &raceExitBeforeSignalRuntime{t: t, nextPID: 60000, exited: make(chan struct{})}
}

func (r *raceExitBeforeSignalRuntime) Start(_ context.Context, p process.StartParams, _ io.Writer) (process.ReplicaEndpoint, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextPID++
	pid := r.nextPID
	return process.ReplicaEndpoint{
		URL:      fmt.Sprintf("http://127.0.0.1:%d", p.Port),
		Provider: "native",
		WorkerID: strconv.Itoa(pid),
		Handle:   process.RunHandle{PID: pid},
	}, nil
}

func (r *raceExitBeforeSignalRuntime) Signal(process.RunHandle, syscall.Signal) error {
	r.mu.Lock()
	r.calls++
	n := r.calls
	r.mu.Unlock()
	if n != 1 {
		// Not exercised by the test below, but harmless if it ever were.
		return nil
	}
	r.exitedOnce.Do(func() { close(r.exited) })
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status, err := r.mgr.Status(r.slug)
		if err == nil && status.Status != process.StatusRunning {
			return errors.New("no such process")
		}
		time.Sleep(time.Millisecond)
	}
	r.t.Fatal("exit monitor never processed the independent exit before the deadline")
	return errors.New("no such process")
}

func (r *raceExitBeforeSignalRuntime) Wait(_ context.Context, _ process.RunHandle) error {
	<-r.exited
	return &process.ProcessExitError{Code: 1}
}

func (r *raceExitBeforeSignalRuntime) Stats(context.Context, process.RunHandle) (*float64, uint64, error) {
	return nil, 0, nil
}
func (r *raceExitBeforeSignalRuntime) RunOnce(context.Context, process.StartParams, io.Writer) (process.ExitInfo, error) {
	return process.ExitInfo{}, nil
}
func (r *raceExitBeforeSignalRuntime) HostPreparesDeps() bool    { return false }
func (r *raceExitBeforeSignalRuntime) AppBindHost() string       { return "127.0.0.1" }
func (r *raceExitBeforeSignalRuntime) HostProvidesAppData() bool { return false }

// TestManager_StopConfirmed_ExitObservedBeforeFailedSignalConfirmsStop
// proves that a confirmed stop whose SIGTERM fails only because the process
// already exited, and whose exit the monitor already observed, is treated as
// confirmed on that same call: it returns nil, removes the entry, and leaves
// the slot free for Start, with no retry required.
func TestManager_StopConfirmed_ExitObservedBeforeFailedSignalConfirmsStop(t *testing.T) {
	rt := newRaceExitBeforeSignalRuntime(t)
	m := process.NewManager(t.TempDir(), rt)
	rt.mgr = m
	rt.slug = "race-exit-demo"

	params := process.StartParams{Slug: rt.slug, Index: 0, Command: []string{"x"}, Port: 1}
	if _, err := m.Start(params); err != nil {
		t.Fatalf("start: %v", err)
	}

	// The first call's own Signal triggers the independent exit mid-call (see
	// raceExitBeforeSignalRuntime.Signal) and then reports the SIGTERM as
	// failing against the now-dead handle. The exit was already observed, so
	// the stop is confirmed despite the failed signal.
	if err := m.StopReplicaConfirmed(rt.slug, 0); err != nil {
		t.Fatalf("StopReplicaConfirmed after an observed exit: %v", err)
	}
	if _, _, ok := m.ReplicaIncarnation(rt.slug, 0); ok {
		t.Fatal("manager still holds the entry after a confirmed stop")
	}
	if _, err := m.Start(params); err != nil {
		t.Fatalf("Start after a confirmed stop: %v", err)
	}
}

// TestManager_ReleaseStopPending_RetriesFailedLogRunFinish asserts that when
// the exit monitor's persist of a claimed replica's terminal log run fails,
// ReleaseStopPending retries it before dropping the entry. The entry is the
// only copy of that verdict, so dropping it without a retry would leave the
// run recorded as still open forever.
func TestManager_ReleaseStopPending_RetriesFailedLogRunFinish(t *testing.T) {
	rt := newSignalFailRuntime()
	m := process.NewManager(t.TempDir(), rt)
	m.SetStopGrace(20 * time.Millisecond)

	var mu sync.Mutex
	var finishes []process.LogRun
	m.SetLogRunRecorder(process.LogRunRecorder{
		Finish: func(run process.LogRun) error {
			mu.Lock()
			defer mu.Unlock()
			finishes = append(finishes, run)
			if len(finishes) == 1 {
				return errors.New("inject: database unavailable")
			}
			return nil
		},
	})
	finishCount := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(finishes)
	}

	const slug = "release-finish"
	info, err := m.Start(process.StartParams{Slug: slug, Index: 0, Command: []string{"x"}, Port: 1})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	gen, _, ok := m.ReplicaIncarnation(slug, 0)
	if !ok {
		t.Fatal("ReplicaIncarnation missing for the just-started replica")
	}
	if err := m.StopReplicaConfirmed(slug, 0); err == nil {
		t.Fatal("StopReplicaConfirmed returned nil despite the signal being rejected")
	}
	if !m.ClaimStopPending(slug, 0, gen) {
		t.Fatal("ClaimStopPending returned false for the incarnation just captured")
	}

	rt.triggerExit(info.PID)
	deadline := time.Now().Add(5 * time.Second)
	for finishCount() < 1 {
		if time.Now().After(deadline) {
			t.Fatal("exit monitor never attempted to persist the finished log run")
		}
		time.Sleep(5 * time.Millisecond)
	}

	m.ReleaseStopPending(slug, 0, gen)

	if got := finishCount(); got != 2 {
		t.Fatalf("log run finish attempts = %d, want 2 (the failed monitor persist, then the release retry)", got)
	}
	mu.Lock()
	retried := finishes[1]
	mu.Unlock()
	if retried.RunID != info.LogRunID || retried.FinishedAt.IsZero() {
		t.Fatalf("retried run = %+v, want the monitor's terminal record for run %q", retried, info.LogRunID)
	}
}

// TestManager_ReleaseStopPending_LeavesDurableLogRunAlone pins the other side
// of the retry: once the exit monitor's persist succeeded, ReleaseStopPending
// must not write the run again, since the releasing caller records its own,
// more specific verdict afterwards and a late rewrite would clobber it.
func TestManager_ReleaseStopPending_LeavesDurableLogRunAlone(t *testing.T) {
	rt := newSignalFailRuntime()
	m := process.NewManager(t.TempDir(), rt)
	m.SetStopGrace(20 * time.Millisecond)

	var finishes atomic.Int32
	m.SetLogRunRecorder(process.LogRunRecorder{
		Finish: func(process.LogRun) error { finishes.Add(1); return nil },
	})

	const slug = "release-durable"
	info, err := m.Start(process.StartParams{Slug: slug, Index: 0, Command: []string{"x"}, Port: 1})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	gen, _, _ := m.ReplicaIncarnation(slug, 0)
	if err := m.StopReplicaConfirmed(slug, 0); err == nil {
		t.Fatal("StopReplicaConfirmed returned nil despite the signal being rejected")
	}
	if !m.ClaimStopPending(slug, 0, gen) {
		t.Fatal("ClaimStopPending returned false")
	}
	rt.triggerExit(info.PID)
	deadline := time.Now().Add(5 * time.Second)
	for finishes.Load() < 1 {
		if time.Now().After(deadline) {
			t.Fatal("exit monitor never persisted the finished log run")
		}
		time.Sleep(5 * time.Millisecond)
	}

	m.ReleaseStopPending(slug, 0, gen)

	if got := finishes.Load(); got != 1 {
		t.Fatalf("log run finish attempts = %d, want 1 (no rewrite of a durable record)", got)
	}
}
