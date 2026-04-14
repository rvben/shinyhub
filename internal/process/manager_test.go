package process_test

import (
	"syscall"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/process"
)

func TestManagerStartStop(t *testing.T) {
	m := process.NewManager()

	info, err := m.Start(process.StartParams{
		Slug:    "test-app",
		Dir:     t.TempDir(),
		Command: []string{"sleep", "10"},
		Port:    19000,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if info.PID <= 0 {
		t.Errorf("expected valid PID, got %d", info.PID)
	}

	time.Sleep(100 * time.Millisecond)

	if err := m.Stop("test-app"); err != nil {
		t.Fatalf("stop: %v", err)
	}

	// verify the process is actually gone
	if err := syscall.Kill(info.PID, 0); err == nil {
		t.Error("expected process to be dead after Stop")
	}
}

func TestManagerStatus(t *testing.T) {
	m := process.NewManager()
	_, err := m.Start(process.StartParams{
		Slug:    "status-app",
		Dir:     t.TempDir(),
		Command: []string{"sleep", "10"},
		Port:    19001,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Stop("status-app")

	info, err := m.Status("status-app")
	if err != nil {
		t.Fatal(err)
	}
	if info.Status != process.StatusRunning {
		t.Errorf("expected running, got %s", info.Status)
	}
}

func TestManagerStatusUnknown(t *testing.T) {
	m := process.NewManager()
	info, err := m.Status("no-such-app")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Status != process.StatusStopped {
		t.Errorf("expected stopped, got %s", info.Status)
	}
	if info.Slug != "no-such-app" {
		t.Errorf("expected slug preserved, got %s", info.Slug)
	}
}
