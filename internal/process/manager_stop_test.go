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
