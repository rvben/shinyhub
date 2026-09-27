package process

import (
	"errors"
	"testing"
	"time"
)

// TestStopReplica_BoundedWaitOnWedgedProcess proves StopReplica does not block
// forever when a process never exits even after SIGKILL (e.g. uninterruptible
// D-state sleep on a hung shared-mount backend). Blocking here would freeze the
// watchdog and stall crash-restart/hibernation fleet-wide (PROD-1).
func TestStopReplica_BoundedWaitOnWedgedProcess(t *testing.T) {
	rt := &captureRuntime{} // Wait blocks forever, Signal is a no-op
	m := NewManager(t.TempDir(), rt)
	m.SetStopGrace(20 * time.Millisecond)

	if _, err := m.Start(StartParams{
		Slug:    "wedged",
		Dir:     t.TempDir(),
		Command: []string{"true"},
		Port:    19950,
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	returned := make(chan error, 1)
	go func() { returned <- m.StopReplica("wedged", 0) }()

	select {
	case <-returned:
		// Returned within the bounded window - correct.
	case <-time.After(3 * time.Second):
		t.Fatal("StopReplica hung on a process that never exits after SIGKILL")
	}
}

// TestStopReplica_WedgedProcessIsForgottenDespiteUnconfirmedExit reproduces a
// bug distinct from the bounded-wait guarantee above: when neither SIGTERM nor
// SIGKILL produces an observed exit, plain StopReplica (unlike
// StopReplicaConfirmed) drops the manager's entry anyway and returns nil, even
// though the process was never confirmed dead. The replica may still be
// running (e.g. genuinely wedged in D-state), but nothing tracks it any
// longer: its log file is never closed, its log run is never finished, and a
// later Start at the same slug+index would place a new process into a slot
// the old one may still occupy.
func TestStopReplica_WedgedProcessIsForgottenDespiteUnconfirmedExit(t *testing.T) {
	rt := &captureRuntime{} // Signal is a no-op, Wait blocks forever: the process never confirms exit.
	m := NewManager(t.TempDir(), rt)
	m.SetStopGrace(20 * time.Millisecond)

	if _, err := m.Start(StartParams{
		Slug:    "wedged-forgotten",
		Dir:     t.TempDir(),
		Command: []string{"true"},
		Port:    19953,
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if err := m.StopReplica("wedged-forgotten", 0); err != nil {
		t.Fatalf("StopReplica: %v", err)
	}

	if _, ok := m.GetReplica("wedged-forgotten", 0); !ok {
		t.Error("StopReplica forgot a replica whose exit was never confirmed: " +
			"the process may still be running, untracked, and its log run is never finished")
	}
}

func TestStopReplicaConfirmed_LeavesWedgedReplicaTracked(t *testing.T) {
	rt := &captureRuntime{}
	m := NewManager(t.TempDir(), rt)
	m.SetStopGrace(20 * time.Millisecond)
	if _, err := m.Start(StartParams{Slug: "wedged", Dir: t.TempDir(), Command: []string{"true"}, Port: 19951}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	err := m.StopReplicaConfirmed("wedged", 0)
	if !errors.Is(err, ErrStopUnconfirmed) {
		t.Fatalf("StopReplicaConfirmed error = %v, want ErrStopUnconfirmed", err)
	}
	if _, ok := m.GetReplica("wedged", 0); !ok {
		t.Fatal("unconfirmed replica was removed from the manager")
	}
}

func TestStopConfirmed_RejectsPoolWithWedgedReplicaAndKeepsItTracked(t *testing.T) {
	rt := &captureRuntime{}
	m := NewManager(t.TempDir(), rt)
	m.SetStopGrace(20 * time.Millisecond)
	if _, err := m.Start(StartParams{Slug: "wedged-pool", Dir: t.TempDir(), Command: []string{"true"}, Port: 19952}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	err := m.StopConfirmed("wedged-pool")
	if !errors.Is(err, ErrStopUnconfirmed) {
		t.Fatalf("StopConfirmed error = %v, want ErrStopUnconfirmed", err)
	}
	if _, ok := m.GetReplica("wedged-pool", 0); !ok {
		t.Fatal("unconfirmed pool replica was removed from the manager")
	}
}
