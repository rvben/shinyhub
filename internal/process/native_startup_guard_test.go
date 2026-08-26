package process_test

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/process"
)

func TestNativeStartupGuardBlocksExecUntilDurableAcknowledgement(t *testing.T) {
	bundle := t.TempDir()
	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	marker := filepath.Join(bundle, "started")

	start := func(slug string, reservationHeld bool) *process.ProcessInfo {
		info, err := mgr.Start(process.StartParams{
			Slug: slug, Index: 0, Dir: bundle, Port: 19001,
			Command:                []string{"/bin/sh", "-c", "printf started > started; sleep 30"},
			GuardUntilAcknowledged: true,
			LaunchReservationHeld:  reservationHeld,
		})
		if err != nil {
			t.Fatalf("guarded Start: %v", err)
		}
		return info
	}

	// Closing an unacknowledged launch (the same pipe outcome as control-plane
	// death) must stop the supervisor without ever executing app code.
	start("guard-abort", false)
	time.Sleep(50 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("app executed before acknowledgement: stat err=%v", err)
	}
	if err := mgr.StopReplicaConfirmed("guard-abort", 0); err != nil {
		t.Fatalf("stop unacknowledged guard: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("aborted guard executed app code: stat err=%v", err)
	}

	release := mgr.AcquireLaunchReservation()
	info := start("guard-ack", true)
	competitorDone := make(chan error, 1)
	go func() {
		_, err := mgr.Start(process.StartParams{
			Slug: "competing-app", Index: 0, Dir: bundle, Port: 19002,
			Command: []string{"/bin/sh", "-c", "sleep 30"},
		})
		competitorDone <- err
	}()
	select {
	case err := <-competitorDone:
		t.Fatalf("competing launch crossed reservation before acknowledgement: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if err := mgr.AcknowledgeReplicaStart("guard-ack", 0); err != nil {
		t.Fatalf("acknowledge: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("acknowledged guard did not exec app")
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case err := <-competitorDone:
		t.Fatalf("competing launch crossed reservation while guarded app became ready: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	release()
	select {
	case err := <-competitorDone:
		if err != nil {
			t.Fatalf("competing launch after reservation release: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("competing launch remained blocked after reservation release")
	}
	// exec preserves the supervisor PID, which is the durable recovery handle.
	if err := syscall.Kill(info.PID, 0); err != nil {
		t.Fatalf("acknowledged app PID %d is not alive: %v", info.PID, err)
	}
	if err := mgr.StopReplicaConfirmed("guard-ack", 0); err != nil {
		t.Fatalf("stop acknowledged app: %v", err)
	}
	if err := mgr.StopReplicaConfirmed("competing-app", 0); err != nil {
		t.Fatalf("stop competing app: %v", err)
	}
}
