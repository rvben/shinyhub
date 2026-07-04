package process

import (
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
