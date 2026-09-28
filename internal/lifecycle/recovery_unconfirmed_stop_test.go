package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/process"
)

// startUnreadyNativeProcess launches a real process-group leader rooted at
// bundleDir that never listens on a port, so it passes the PID-liveness and
// cwd-identity checks but fails the readiness probe - the "not ready" branch
// recoverNativeReplica must handle without guessing at the outcome.
func startUnreadyNativeProcess(t *testing.T, bundleDir string) int {
	t.Helper()
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("sleep unavailable: %v", err)
	}
	cmd := exec.Command(sleep, "30")
	cmd.Dir = bundleDir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start native test process: %v", err)
	}
	pid := cmd.Process.Pid
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() {
		// The scripted-stop fake Runtime never actually signals the real OS
		// process - that is the point, it lets the test control whether the
		// confirmed stop appears to succeed - so the test must reap it directly.
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Errorf("native test process %d did not exit", pid)
		}
	})
	return pid
}

// scriptedStopRuntime is a minimal process.Runtime fake giving deterministic
// control over Signal, so a confirmed stop can be driven through exactly one
// of the two ways it can fail to confirm an exit: a rejected SIGTERM, or a
// Wait that never observes one. Every other method is an unused stub -
// recovery adopts via Manager.Adopt, which never calls Start, Stats, or
// RunOnce.
type scriptedStopRuntime struct {
	signalErr error
}

func (r *scriptedStopRuntime) Start(context.Context, process.StartParams, io.Writer) (process.ReplicaEndpoint, error) {
	return process.ReplicaEndpoint{}, errors.New("scriptedStopRuntime: Start not used")
}

func (r *scriptedStopRuntime) Signal(process.RunHandle, syscall.Signal) error { return r.signalErr }

func (r *scriptedStopRuntime) Wait(context.Context, process.RunHandle) error {
	// Never confirms the exit, matching a process wedged past both grace
	// windows; acceptable to leak in tests (mirrors process.captureRuntime).
	select {}
}

func (r *scriptedStopRuntime) Stats(context.Context, process.RunHandle) (*float64, uint64, error) {
	return nil, 0, nil
}

func (r *scriptedStopRuntime) RunOnce(context.Context, process.StartParams, io.Writer) (process.ExitInfo, error) {
	return process.ExitInfo{}, errors.New("scriptedStopRuntime: RunOnce not used")
}

func (r *scriptedStopRuntime) HostPreparesDeps() bool    { return true }
func (r *scriptedStopRuntime) AppBindHost() string       { return "127.0.0.1" }
func (r *scriptedStopRuntime) HostProvidesAppData() bool { return true }

// fakePendingQueue records every PendingStopEntry handed to it, standing in
// for the watcher's real confirmed-stop retry loop.
type fakePendingQueue struct {
	entries []PendingStopEntry
}

func (q *fakePendingQueue) QueuePendingStop(e PendingStopEntry) {
	q.entries = append(q.entries, e)
}

// seedUnreadyReplica creates an app with one running replica row backed by a
// real, cwd-matched, port-silent process: it passes the liveness and identity
// checks but fails the readiness probe, so recoverNativeReplica must take the
// not-ready branch and call StopReplicaConfirmed.
func seedUnreadyReplica(t *testing.T, port int) (store *db.Store, app *db.App, r *db.Replica, bundleDir string, pid int) {
	t.Helper()
	store, app = seedWarmApp(t)
	bundleDir = t.TempDir()
	pid = startUnreadyNativeProcess(t, bundleDir)
	dep, err := store.CreateDeployment(db.CreateDeploymentParams{AppID: app.ID, Version: "v1", BundleDir: bundleDir})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: 0, PID: &pid, Port: &port,
		Status: db.ReplicaStatusRunning, DesiredState: "running",
		Provider: "native", Tier: "default",
		EndpointURL: fmt.Sprintf("http://127.0.0.1:%d", port), WorkerID: strconv.Itoa(pid),
		DeploymentID: &dep.ID,
	}); err != nil {
		t.Fatal(err)
	}
	return store, app, replicaAt(t, store, app.ID, 0), bundleDir, pid
}

// assertUnconfirmedStopLeftIndeterminate is the shared assertion body for
// both failure modes a confirmed stop of an unready process can take: the
// slot must be reported indeterminate rather than driven to crashed, the row
// must be untouched, the retry must be queued with the right identity, and
// the real process (which the scripted runtime never actually signals) must
// still be alive.
func assertUnconfirmedStopLeftIndeterminate(t *testing.T, store *db.Store, app *db.App, pid int, alive, indeterminate bool, queue *fakePendingQueue) {
	t.Helper()
	if alive {
		t.Fatal("an unconfirmed stop must not be reported as a live re-adopt")
	}
	if !indeterminate {
		t.Fatal("an unconfirmed stop must be reported indeterminate, so RecoverProcesses does not mark the app down")
	}
	rep := replicaAt(t, store, app.ID, 0)
	if rep.Status == "crashed" {
		t.Fatal("row marked crashed on an UNCONFIRMED stop; the process may still be running")
	}
	if rep.PID == nil || *rep.PID != pid {
		t.Fatalf("PID = %v, want %d preserved", rep.PID, pid)
	}
	if syscall.Kill(pid, 0) != nil {
		t.Fatal("the real process was reaped even though the scripted runtime never signalled it; the test's own control is broken")
	}
	if len(queue.entries) != 1 {
		t.Fatalf("queued %d entries, want exactly 1", len(queue.entries))
	}
	e := queue.entries[0]
	if e.Kind != pendingStopRecoveryUnready {
		t.Errorf("Kind = %v, want pendingStopRecoveryUnready", e.Kind)
	}
	if e.Slug != app.Slug || e.Index != 0 || e.AppID != app.ID || e.PID != pid {
		t.Errorf("queued entry = %+v, want slug=%s index=0 appID=%d pid=%d", e, app.Slug, app.ID, pid)
	}
	if e.Incarnation == 0 {
		t.Error("Incarnation = 0, want the adopted entry's real incarnation so a stale retry can be told apart from a later one")
	}
	// The retry may only crash-mark the row it was queued for, so it must
	// carry the row's whole runtime identity, not just the PID.
	want := db.ReplicaRuntimeIdentity{
		PID: pid, Port: *rep.Port, EndpointURL: rep.EndpointURL,
		WorkerID: rep.WorkerID, DeploymentID: *rep.DeploymentID,
	}
	if got := e.replicaIdentity(); got != want {
		t.Errorf("queued identity = %+v, want the row's %+v", got, want)
	}
}

// TestRecoverNativeReplica_UnconfirmedStopDoesNotMarkCrashed is the grace-
// timeout variant: SIGTERM is delivered without an OS error, but the process
// never confirms its exit within either grace window (StopReplicaConfirmed
// returns ErrStopUnconfirmed). The row must be left alone rather than guessed
// at, and a retry queued to keep confirming later.
func TestRecoverNativeReplica_UnconfirmedStopDoesNotMarkCrashed(t *testing.T) {
	store, app, r, bundleDir, pid := seedUnreadyReplica(t, 20221)
	mgr := process.NewManager(t.TempDir(), &scriptedStopRuntime{})
	mgr.SetStopGrace(20 * time.Millisecond)
	queue := &fakePendingQueue{}

	alive, indeterminate := recoverNativeReplica(store, mgr, nil, app, r, bundleDir, "run-1", queue)

	assertUnconfirmedStopLeftIndeterminate(t, store, app, pid, alive, indeterminate, queue)
}

// TestRecoverNativeReplica_SigtermFailureDoesNotMarkCrashed is the second
// failure mode: SIGTERM delivery itself fails (a signal(2) error), which
// StopReplicaConfirmed reports immediately without ever waiting. The
// not-ready branch must survive this exactly like the grace-timeout case.
func TestRecoverNativeReplica_SigtermFailureDoesNotMarkCrashed(t *testing.T) {
	store, app, r, bundleDir, pid := seedUnreadyReplica(t, 20222)
	mgr := process.NewManager(t.TempDir(), &scriptedStopRuntime{signalErr: errors.New("operation not permitted")})
	mgr.SetStopGrace(20 * time.Millisecond)
	queue := &fakePendingQueue{}

	alive, indeterminate := recoverNativeReplica(store, mgr, nil, app, r, bundleDir, "run-2", queue)

	assertUnconfirmedStopLeftIndeterminate(t, store, app, pid, alive, indeterminate, queue)
}

// TestRecoverNativeReplica_UnconfirmedStopWithoutQueueStillSurvives proves the
// nil-queue path (a caller with no watcher wired up) still gets the safety
// property - the row is left alone - even though it loses the retry-queue
// convenience.
func TestRecoverNativeReplica_UnconfirmedStopWithoutQueueStillSurvives(t *testing.T) {
	store, app, r, bundleDir, _ := seedUnreadyReplica(t, 20223)
	mgr := process.NewManager(t.TempDir(), &scriptedStopRuntime{})
	mgr.SetStopGrace(20 * time.Millisecond)

	alive, indeterminate := recoverNativeReplica(store, mgr, nil, app, r, bundleDir, "run-3", nil)

	if alive {
		t.Fatal("an unconfirmed stop must not be reported as a live re-adopt")
	}
	if !indeterminate {
		t.Fatal("an unconfirmed stop must be reported indeterminate even with no queue wired up")
	}
	rep := replicaAt(t, store, app.ID, 0)
	if rep.Status == "crashed" {
		t.Fatal("row marked crashed on an unconfirmed stop even with no queue wired up")
	}
}
