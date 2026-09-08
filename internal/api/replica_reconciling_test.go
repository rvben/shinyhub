package api

import (
	"testing"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/process"
)

// reconcilingFixture returns a server whose process manager holds nothing, and
// one stored replica row recorded as running with a PID. That is exactly the
// state a server is in immediately after a restart in which the app's process
// survived: the row is real, the manager has not looked yet.
func reconcilingFixture(t *testing.T) (*Server, *db.Replica) {
	t.Helper()
	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	pid, port := 4242, 18080
	return &Server{manager: mgr},
		&db.Replica{Index: 0, Status: db.ReplicaStatusRunning, DesiredState: "running", PID: &pid, Port: &port}
}

// TestLiveReplicaView_StartupWindowReportsReconcilingNotStopped pins the fact
// the startup window turns on: an empty manager means "nothing adopted yet"
// while recovery is outstanding, and "nothing is running" once it has finished.
// Reading the first as the second reports a healthy, serving app as down for as
// long as re-adoption takes, and erases the PID that identifies the process the
// operator would otherwise go look at.
func TestLiveReplicaView_StartupWindowReportsReconcilingNotStopped(t *testing.T) {
	srv, stored := reconcilingFixture(t)
	srv.manager.MarkRecoveryPending()

	out := srv.liveReplicaView("survivor", []*db.Replica{stored})
	if len(out) != 1 {
		t.Fatalf("expected one replica, got %d", len(out))
	}
	got := out[0]
	if got.Status != replicaStatusReconciling {
		t.Errorf("status = %q, want %q: while re-adoption is outstanding the server has not established that this process is gone",
			got.Status, replicaStatusReconciling)
	}
	// The recorded identity is the whole point of saying "reconciling" rather
	// than "stopped": it names the process recovery is about to adjudicate.
	if got.PID == nil || *got.PID != *stored.PID {
		t.Errorf("pid = %v, want the recorded pid %d", got.PID, *stored.PID)
	}
	if got.Port == nil || *got.Port != *stored.Port {
		t.Errorf("port = %v, want the recorded port %d", got.Port, *stored.Port)
	}
}

// TestLiveReplicaView_AfterRecoveryAnUnadoptedReplicaIsStopped is the other
// bound. Without it the test above passes on a build that reports "reconciling"
// forever, which would replace a status that is wrong for fifteen seconds with
// one that is wrong permanently: a genuinely dead replica would never read as
// stopped again.
func TestLiveReplicaView_AfterRecoveryAnUnadoptedReplicaIsStopped(t *testing.T) {
	srv, stored := reconcilingFixture(t)
	srv.manager.MarkRecoveryPending()
	srv.manager.ClearRecoveryPending()

	out := srv.liveReplicaView("survivor", []*db.Replica{stored})
	if len(out) != 1 {
		t.Fatalf("expected one replica, got %d", len(out))
	}
	got := out[0]
	if got.Status != string(process.StatusStopped) {
		t.Errorf("status = %q, want %q: recovery has run and adopted nothing, so this row is stale",
			got.Status, process.StatusStopped)
	}
	if got.PID != nil || got.Port != nil {
		t.Errorf("pid/port = %v/%v, want both cleared once the process is known to be gone", got.PID, got.Port)
	}
}

// TestManagerRecoveryPendingDefaultsToSettled guards the default. A manager
// nobody schedules recovery for - every test fixture, every embedded use - has
// nothing outstanding, and defaulting the other way would put every replica in
// the codebase permanently into the startup window.
func TestManagerRecoveryPendingDefaultsToSettled(t *testing.T) {
	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	if mgr.RecoveryPending() {
		t.Error("a freshly built manager reports a recovery pass outstanding; nothing scheduled one")
	}
	var nilMgr *process.Manager
	if nilMgr.RecoveryPending() {
		t.Error("a nil manager reports a recovery pass outstanding")
	}
}
