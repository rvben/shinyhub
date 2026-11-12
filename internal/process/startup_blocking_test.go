package process_test

import (
	"context"
	"errors"
	"io"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/process"
)

// gatedStartRuntime holds every Start for the slug "slow" until release is
// closed, modelling a runtime that waits on a remote resource before it can
// hand back an endpoint.
type gatedStartRuntime struct {
	*perPIDRuntime
	enteredOnce sync.Once
	entered     chan struct{}
	release     chan struct{}
}

func newGatedStartRuntime() *gatedStartRuntime {
	return &gatedStartRuntime{perPIDRuntime: newPerPIDRuntime(), entered: make(chan struct{}), release: make(chan struct{})}
}

func (r *gatedStartRuntime) Start(ctx context.Context, p process.StartParams, w io.Writer) (process.ReplicaEndpoint, error) {
	if p.Slug == "slow" {
		r.enteredOnce.Do(func() { close(r.entered) })
		<-r.release
	}
	return r.perPIDRuntime.Start(ctx, p, w)
}

// lastPID is the PID the runtime handed out most recently.
func (r *gatedStartRuntime) lastPID() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.nextPID
}

func within(d time.Duration, fn func()) bool {
	done := make(chan struct{})
	go func() { fn(); close(done) }()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

// slowLaunch starts replica 0 of "slow" in the background and returns once
// the runtime is holding it. The returned channel yields the Start result.
func slowLaunch(t *testing.T, mgr *process.Manager, rt *gatedStartRuntime) <-chan error {
	t.Helper()
	startDone := make(chan error, 1)
	go func() {
		_, err := mgr.Start(process.StartParams{Slug: "slow", Index: 0, Port: 20002, Command: []string{"app"}, Dir: t.TempDir()})
		startDone <- err
	}()
	select {
	case <-rt.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the runtime never received the start")
	}
	return startDone
}

func awaitStart(t *testing.T, startDone <-chan error) error {
	t.Helper()
	select {
	case err := <-startDone:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return after the runtime released it")
		return nil
	}
}

// While one app's runtime is still starting, the manager must keep answering
// for every other app.
func TestSlowRuntimeStartDoesNotBlockOtherApps(t *testing.T) {
	rt := newGatedStartRuntime()
	mgr := process.NewManager(t.TempDir(), rt)
	if _, err := mgr.Start(process.StartParams{Slug: "other", Index: 0, Port: 20001, Command: []string{"app"}, Dir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	startDone := slowLaunch(t, mgr, rt)
	defer func() {
		close(rt.release)
		<-startDone
		_ = mgr.StopAll()
	}()

	const budget = 500 * time.Millisecond
	if !within(budget, func() { _ = mgr.HasRunning("other") }) {
		t.Errorf("a status read for an unrelated app waited more than %s on another app's runtime start", budget)
	}
	if !within(budget, func() { _ = mgr.All() }) {
		t.Errorf("listing every replica waited more than %s on another app's runtime start", budget)
	}
	if !within(budget, func() { _ = mgr.StopReplica("other", 0) }) {
		t.Errorf("stopping an unrelated app waited more than %s on another app's runtime start", budget)
	}
}

// A second Start of a slot whose launch is in flight is refused as already
// running, so callers that treat that as a no-op retry keep doing so. An
// ordinary Start waits for the launch reservation, so only a caller that
// already holds it can reach the slot while the first launch is in flight.
func TestStartOfLaunchingSlotIsRefused(t *testing.T) {
	rt := newGatedStartRuntime()
	mgr := process.NewManager(t.TempDir(), rt)
	startDone := slowLaunch(t, mgr, rt)
	defer func() { _ = mgr.StopAll() }()

	_, err := mgr.Start(process.StartParams{Slug: "slow", Index: 0, Port: 20003, Command: []string{"app"}, Dir: t.TempDir(), LaunchReservationHeld: true})
	if !errors.Is(err, process.ErrReplicaStarting) {
		t.Fatalf("second Start of a launching slot: got %v, want ErrReplicaStarting", err)
	}
	if !errors.Is(err, process.ErrReplicaAlreadyRunning) {
		t.Fatalf("ErrReplicaStarting must also match ErrReplicaAlreadyRunning, got %v", err)
	}
	if !mgr.HoldsLiveProcess("slow") {
		t.Error("HoldsLiveProcess must count a replica that is still launching")
	}
	if got := mgr.NextReplicaIndex("slow"); got != 1 {
		t.Errorf("NextReplicaIndex while replica 0 launches = %d, want 1", got)
	}

	close(rt.release)
	if err := awaitStart(t, startDone); err != nil {
		t.Fatalf("the first Start must still succeed: %v", err)
	}
	if !mgr.HasRunning("slow") {
		t.Fatal("the launched replica was not published")
	}
}

// A stop that arrives while the replica is launching returns within the stop
// bound instead of waiting out the launch, and the replica the launch then
// produces is stopped rather than published as running.
func TestStopDuringHungLaunchStopsTheReplicaOnceLaunched(t *testing.T) {
	rt := newGatedStartRuntime()
	mgr := process.NewManager(t.TempDir(), rt)
	const grace = 50 * time.Millisecond
	mgr.SetStopGrace(grace)
	startDone := slowLaunch(t, mgr, rt)

	var stopErr error
	if !within(2*grace+time.Second, func() { stopErr = mgr.StopReplica("slow", 0) }) {
		t.Fatal("StopReplica of a launching replica did not return within its bound")
	}
	if stopErr != nil {
		t.Fatalf("StopReplica of a launching replica: %v", stopErr)
	}

	close(rt.release)
	if err := awaitStart(t, startDone); !errors.Is(err, process.ErrStartCancelled) {
		t.Fatalf("Start cancelled by a stop: got %v, want ErrStartCancelled", err)
	}
	pid := rt.lastPID()
	if sigs := rt.signalsTo(pid); len(sigs) == 0 || sigs[0] != syscall.SIGTERM {
		t.Fatalf("the launched replica was not stopped: signals %v", sigs)
	}
	if _, ok := mgr.GetReplica("slow", 0); ok {
		t.Error("a stopped launch left an entry in its slot")
	}
	if mgr.HoldsLiveProcess("slow") {
		t.Error("HoldsLiveProcess reports a live process after the cancelled launch was stopped")
	}
	if _, err := mgr.Start(process.StartParams{Slug: "slow", Index: 0, Port: 20004, Command: []string{"app"}, Dir: t.TempDir()}); err != nil {
		t.Fatalf("the slot must be free for a new Start: %v", err)
	}
	_ = mgr.StopAll()
}

// A confirmed stop of a launch that finishes within the stop bound reports
// the stop as confirmed only after the launched replica is gone.
func TestConfirmedStopDuringShortLaunchIsConfirmed(t *testing.T) {
	rt := newGatedStartRuntime()
	mgr := process.NewManager(t.TempDir(), rt)
	mgr.SetStopGrace(2 * time.Second)
	startDone := slowLaunch(t, mgr, rt)

	stopDone := make(chan error, 1)
	go func() { stopDone <- mgr.StopReplicaConfirmed("slow", 0) }()
	// Let the stop register the cancellation before the launch returns.
	time.Sleep(50 * time.Millisecond)
	close(rt.release)

	if err := awaitStart(t, startDone); !errors.Is(err, process.ErrStartCancelled) {
		t.Fatalf("Start cancelled by a stop: got %v, want ErrStartCancelled", err)
	}
	select {
	case err := <-stopDone:
		if err != nil {
			t.Fatalf("StopReplicaConfirmed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("StopReplicaConfirmed did not return")
	}
	if sigs := rt.signalsTo(rt.lastPID()); len(sigs) == 0 {
		t.Fatal("the stop was confirmed but the launched replica was never signalled")
	}
	if mgr.HoldsLiveProcess("slow") {
		t.Error("a confirmed stop left a live process")
	}
}

// A confirmed stop cannot be confirmed while the launch has produced nothing
// it could observe exiting.
func TestConfirmedStopDuringHungLaunchIsUnconfirmed(t *testing.T) {
	rt := newGatedStartRuntime()
	mgr := process.NewManager(t.TempDir(), rt)
	mgr.SetStopGrace(20 * time.Millisecond)
	startDone := slowLaunch(t, mgr, rt)
	defer func() { _ = mgr.StopAll() }()

	if err := mgr.StopReplicaConfirmed("slow", 0); !errors.Is(err, process.ErrStopUnconfirmed) {
		t.Fatalf("StopReplicaConfirmed of a hung launch: got %v, want ErrStopUnconfirmed", err)
	}
	if !mgr.HoldsLiveProcess("slow") {
		t.Error("an unconfirmed stop of a launch must still count as a live process")
	}
	close(rt.release)
	if err := awaitStart(t, startDone); !errors.Is(err, process.ErrStartCancelled) {
		t.Fatalf("Start cancelled by a stop: got %v, want ErrStartCancelled", err)
	}
}

// A cancelled launch whose replica does not exit when signalled stays tracked
// and fenced, so nothing can start a second replica into the slot.
func TestCancelledLaunchThatWillNotExitStaysFenced(t *testing.T) {
	rt := newGatedStartRuntime()
	mgr := process.NewManager(t.TempDir(), rt)
	mgr.SetStopGrace(20 * time.Millisecond)
	startDone := slowLaunch(t, mgr, rt)

	if err := mgr.StopReplica("slow", 0); err != nil {
		t.Fatalf("StopReplica of a launching replica: %v", err)
	}
	rt.perPIDRuntime.mu.Lock()
	rt.ignoreNext = true
	rt.perPIDRuntime.mu.Unlock()
	close(rt.release)
	if err := awaitStart(t, startDone); !errors.Is(err, process.ErrStartCancelled) {
		t.Fatalf("Start cancelled by a stop: got %v, want ErrStartCancelled", err)
	}
	if !mgr.HoldsLiveProcess("slow") {
		t.Fatal("a launched replica that ignored the stop is no longer tracked")
	}
	_, err := mgr.Start(process.StartParams{Slug: "slow", Index: 0, Port: 20005, Command: []string{"app"}, Dir: t.TempDir()})
	if !errors.Is(err, process.ErrReplicaAlreadyRunning) && !errors.Is(err, process.ErrReplicaStopPending) {
		t.Fatalf("Start into a slot whose replica did not exit: got %v, want it refused as running or stop-pending", err)
	}
	rt.exit(rt.lastPID())
	// The exit monitor observes the exit asynchronously; until it has, a
	// retried stop sees an undeliverable signal and stays unconfirmed.
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := mgr.StopReplicaConfirmed("slow", 0)
		if err == nil || errors.Is(err, process.ErrReplicaNotFound) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("retrying the stop once the replica exited: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if mgr.HoldsLiveProcess("slow") {
		t.Error("the slot still holds a live process after the retried stop")
	}
}

// Stopping an app whose only replica is still launching is a stop of that
// replica, not a stop of an app with nothing to stop.
func TestAppStopCoversLaunchingReplica(t *testing.T) {
	rt := newGatedStartRuntime()
	mgr := process.NewManager(t.TempDir(), rt)
	mgr.SetStopGrace(20 * time.Millisecond)
	startDone := slowLaunch(t, mgr, rt)

	if err := mgr.Stop("slow"); err != nil {
		t.Fatalf("Stop of an app whose replica is launching: %v", err)
	}
	close(rt.release)
	if err := awaitStart(t, startDone); !errors.Is(err, process.ErrStartCancelled) {
		t.Fatalf("Start cancelled by an app stop: got %v, want ErrStartCancelled", err)
	}
	if mgr.HoldsLiveProcess("slow") {
		t.Error("the cancelled launch was left running")
	}
}

// StopAll returns only once a launch it cancelled has stopped what it
// launched, so shutdown never leaves a replica behind.
func TestStopAllWaitsForLaunchToBeStopped(t *testing.T) {
	rt := newGatedStartRuntime()
	mgr := process.NewManager(t.TempDir(), rt)
	mgr.SetStopGrace(20 * time.Millisecond)
	startDone := slowLaunch(t, mgr, rt)

	allDone := make(chan error, 1)
	go func() { allDone <- mgr.StopAll() }()
	select {
	case <-allDone:
		t.Fatal("StopAll returned while a launch was still in flight")
	case <-time.After(200 * time.Millisecond):
	}
	close(rt.release)
	select {
	case err := <-allDone:
		if err != nil {
			t.Fatalf("StopAll: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("StopAll did not return after the launch finished")
	}
	if err := awaitStart(t, startDone); !errors.Is(err, process.ErrStartCancelled) {
		t.Fatalf("Start cancelled by StopAll: got %v, want ErrStartCancelled", err)
	}
	if sigs := rt.signalsTo(rt.lastPID()); len(sigs) == 0 {
		t.Fatal("StopAll returned without stopping the launched replica")
	}
	if mgr.HoldsLiveProcess("slow") {
		t.Error("StopAll left a live process")
	}
}

// armedGateRuntime holds a Start only while armed, so a test can launch a
// replica normally and then hold the launch that restarts it.
type armedGateRuntime struct {
	*perPIDRuntime
	armed   chan struct{} // closed: hold the next Start
	entered chan struct{}
	release chan struct{}
}

func (r *armedGateRuntime) Start(ctx context.Context, p process.StartParams, w io.Writer) (process.ReplicaEndpoint, error) {
	select {
	case <-r.armed:
		close(r.entered)
		<-r.release
	default:
	}
	return r.perPIDRuntime.Start(ctx, p, w)
}

// Stopping a generation while one of its crashed replicas is being restarted
// stops the restart, and the crashed replica the restart replaced is not
// reported as missing.
func TestStopGenerationDuringRestartOfCrashedReplica(t *testing.T) {
	rt := &armedGateRuntime{perPIDRuntime: newPerPIDRuntime(), armed: make(chan struct{}), entered: make(chan struct{}), release: make(chan struct{})}
	mgr := process.NewManager(t.TempDir(), rt)
	mgr.SetStopGrace(2 * time.Second)
	params := process.StartParams{Slug: "demo", Index: 0, Port: 20001, Command: []string{"app"}, Dir: t.TempDir(), DeploymentID: 101}
	first, err := mgr.Start(params)
	if err != nil {
		t.Fatal(err)
	}
	rt.exit(first.PID)
	deadline := time.Now().Add(5 * time.Second)
	for mgr.HasRunning("demo") {
		if time.Now().After(deadline) {
			t.Fatal("the crashed replica was never recorded as exited")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, ok := mgr.GetGenerationReplica("demo", 101, 0); !ok {
		t.Fatal("the crashed replica must still be tracked in its slot for this test")
	}

	close(rt.armed)
	restartDone := make(chan error, 1)
	go func() {
		params.Port = 20002
		_, err := mgr.Start(params)
		restartDone <- err
	}()
	select {
	case <-rt.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the runtime never received the restart")
	}

	stopDone := make(chan error, 1)
	go func() { stopDone <- mgr.StopGeneration("demo", 101) }()
	// Let the stop register the cancellation before the restart returns.
	time.Sleep(50 * time.Millisecond)
	close(rt.release)

	if err := awaitStart(t, restartDone); !errors.Is(err, process.ErrStartCancelled) {
		t.Fatalf("restart cancelled by the generation stop: got %v, want ErrStartCancelled", err)
	}
	select {
	case err := <-stopDone:
		if err != nil {
			t.Fatalf("StopGeneration: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("StopGeneration did not return")
	}
	if mgr.HoldsLiveProcess("demo") {
		t.Error("the generation stop left a live process")
	}
}

// failingGatedRuntime holds the first Start until release is closed and then
// fails it, modelling a remote launch that errors after a stop arrived.
type failingGatedRuntime struct {
	*perPIDRuntime
	entered chan struct{}
	release chan struct{}
}

func (r *failingGatedRuntime) Start(context.Context, process.StartParams, io.Writer) (process.ReplicaEndpoint, error) {
	close(r.entered)
	<-r.release
	return process.ReplicaEndpoint{}, errors.New("remote launch failed")
}

// A launch that a stop cancelled stays cancelled when the runtime then fails
// it, so a caller that retries failed launches does not undo the stop.
func TestCancelledLaunchThatFailsReportsCancelled(t *testing.T) {
	rt := &failingGatedRuntime{perPIDRuntime: newPerPIDRuntime(), entered: make(chan struct{}), release: make(chan struct{})}
	mgr := process.NewManager(t.TempDir(), rt)
	mgr.SetStopGrace(20 * time.Millisecond)
	startDone := make(chan error, 1)
	go func() {
		_, err := mgr.Start(process.StartParams{Slug: "slow", Index: 0, Port: 20002, Command: []string{"app"}, Dir: t.TempDir()})
		startDone <- err
	}()
	select {
	case <-rt.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the runtime never received the start")
	}
	if err := mgr.StopReplica("slow", 0); err != nil {
		t.Fatalf("StopReplica of a launching replica: %v", err)
	}
	close(rt.release)
	err := awaitStart(t, startDone)
	if !errors.Is(err, process.ErrStartCancelled) {
		t.Fatalf("a cancelled launch that failed: got %v, want ErrStartCancelled", err)
	}
	if mgr.HoldsLiveProcess("slow") {
		t.Error("a failed launch left a live process")
	}
}

// A launch that fails without any stop is an ordinary failure, not a
// cancellation, so callers still retry it.
func TestFailedLaunchWithoutStopIsNotCancelled(t *testing.T) {
	rt := &failingGatedRuntime{perPIDRuntime: newPerPIDRuntime(), entered: make(chan struct{}), release: make(chan struct{})}
	close(rt.release)
	mgr := process.NewManager(t.TempDir(), rt)
	_, err := mgr.Start(process.StartParams{Slug: "slow", Index: 0, Port: 20002, Command: []string{"app"}, Dir: t.TempDir()})
	if err == nil || errors.Is(err, process.ErrStartCancelled) {
		t.Fatalf("a failed launch nobody stopped: got %v, want a plain launch error", err)
	}
}

// Log pruning while a replica is launching keeps the launch's log file: the
// run exists on disk before the database records it.
func TestLogPruneKeepsTheFileOfALaunchingReplica(t *testing.T) {
	rt := newGatedStartRuntime()
	mgr := process.NewManager(t.TempDir(), rt)
	startDone := slowLaunch(t, mgr, rt)
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(rt.release) }) }
	defer func() {
		release()
		<-startDone
		_ = mgr.StopAll()
	}()

	runs, err := mgr.LogRuns("slow")
	if err != nil || len(runs) != 1 {
		t.Fatalf("log runs while launching = %v, %v; want the launch's one run", runs, err)
	}
	removed, err := mgr.PruneLogRunFiles(map[string]struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	if removed != 0 {
		t.Fatalf("pruning removed %d files, want 0: the launching replica's log was deleted", removed)
	}
	if runs, _ := mgr.LogRuns("slow"); len(runs) != 1 {
		t.Fatalf("log runs after pruning = %d, want 1", len(runs))
	}
}
