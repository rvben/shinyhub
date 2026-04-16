package process_test

import (
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/process"
)

func TestManagerStartStop(t *testing.T) {
	m := process.NewManager(t.TempDir(), process.NewNativeRuntime())

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
	m := process.NewManager(t.TempDir(), process.NewNativeRuntime())
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
	m := process.NewManager(t.TempDir(), process.NewNativeRuntime())
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

func TestManagerCrashDetection(t *testing.T) {
	m := process.NewManager(t.TempDir(), process.NewNativeRuntime())

	_, err := m.Start(process.StartParams{
		Slug:    "crash-test",
		Dir:     t.TempDir(),
		Command: []string{"sh", "-c", "exit 1"},
		Port:    19200,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Wait for the exit-monitoring goroutine to update status.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		info, err := m.Status("crash-test")
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if info.Status == process.StatusCrashed {
			return // pass
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("expected StatusCrashed after process exited unexpectedly")
}

func TestManagerAdopt(t *testing.T) {
	m := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	pid := os.Getpid()
	handle := process.RunHandle{PID: pid}

	info := process.ProcessInfo{
		Slug:   "adopted",
		PID:    pid,
		Port:   19201,
		Status: process.StatusRunning,
	}
	m.Adopt("adopted", info, handle)

	got, err := m.Status("adopted")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if got.Status != process.StatusRunning {
		t.Errorf("expected StatusRunning, got %s", got.Status)
	}
	if got.PID != pid {
		t.Errorf("expected PID %d, got %d", pid, got.PID)
	}

	gotHandle, ok := m.Handle("adopted")
	if !ok {
		t.Fatal("Handle: not found")
	}
	if gotHandle != handle {
		t.Errorf("expected handle %+v, got %+v", handle, gotHandle)
	}
}
