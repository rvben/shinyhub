package process_test

import (
	"context"
	"io"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/process"
)

// fakeRuntime is a minimal in-process Runtime for tests.
// Start returns a synthetic RunHandle with an incrementing PID.
// Wait blocks until Signal is called with SIGTERM or SIGKILL.
type fakeRuntime struct {
	mu      sync.Mutex
	nextPID int
	stops   map[int]chan struct{}
}

func newFakeRuntime() *fakeRuntime {
	return &fakeRuntime{
		nextPID: 10000,
		stops:   make(map[int]chan struct{}),
	}
}

func (f *fakeRuntime) Start(_ context.Context, p process.StartParams, _ io.Writer) (process.RunHandle, error) {
	f.mu.Lock()
	pid := f.nextPID
	f.nextPID++
	ch := make(chan struct{})
	f.stops[pid] = ch
	f.mu.Unlock()
	return process.RunHandle{PID: pid}, nil
}

func (f *fakeRuntime) Signal(h process.RunHandle, sig syscall.Signal) error {
	f.mu.Lock()
	ch, ok := f.stops[h.PID]
	f.mu.Unlock()
	if ok && (sig == syscall.SIGTERM || sig == syscall.SIGKILL) {
		// close only once
		select {
		case <-ch:
		default:
			close(ch)
		}
	}
	return nil
}

func (f *fakeRuntime) Wait(_ context.Context, h process.RunHandle) error {
	f.mu.Lock()
	ch, ok := f.stops[h.PID]
	f.mu.Unlock()
	if ok {
		<-ch
	}
	return nil
}

func (f *fakeRuntime) Stats(_ context.Context, _ process.RunHandle) (float64, uint64, error) {
	return 0, 0, nil
}

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
	// Status() only returns running replicas; use GetReplica to observe crashed state.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		info, ok := m.GetReplica("crash-test", 0)
		if ok && info.Status == process.StatusCrashed {
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

	gotHandle, ok := m.HandleReplica("adopted", 0)
	if !ok {
		t.Fatal("HandleReplica: not found")
	}
	if gotHandle != handle {
		t.Errorf("expected handle %+v, got %+v", handle, gotHandle)
	}
}

func TestManager_PoolStartStop(t *testing.T) {
	rt := newFakeRuntime()
	m := process.NewManager(t.TempDir(), rt)

	p0, err := m.Start(process.StartParams{
		Slug: "demo", Index: 0, Port: 20001, Command: []string{"/bin/true"},
	})
	if err != nil {
		t.Fatalf("start 0: %v", err)
	}
	p1, err := m.Start(process.StartParams{
		Slug: "demo", Index: 1, Port: 20002, Command: []string{"/bin/true"},
	})
	if err != nil {
		t.Fatalf("start 1: %v", err)
	}

	if p0.Index != 0 || p1.Index != 1 {
		t.Fatalf("indices: %d,%d", p0.Index, p1.Index)
	}

	all := m.All()
	if len(all) != 2 {
		t.Fatalf("want 2 entries, got %d", len(all))
	}

	if err := m.StopReplica("demo", 0); err != nil {
		t.Fatalf("stop 0: %v", err)
	}
	all = m.All()
	if len(all) != 1 || all[0].Index != 1 {
		t.Fatalf("after stop: %+v", all)
	}

	if err := m.Stop("demo"); err != nil {
		t.Fatalf("stop pool: %v", err)
	}
	if len(m.All()) != 0 {
		t.Fatalf("pool not empty after Stop")
	}
}

func TestManager_DuplicateIndex(t *testing.T) {
	rt := newFakeRuntime()
	m := process.NewManager(t.TempDir(), rt)
	_, _ = m.Start(process.StartParams{Slug: "demo", Index: 0, Port: 20001, Command: []string{"/bin/true"}})
	_, err := m.Start(process.StartParams{Slug: "demo", Index: 0, Port: 20002, Command: []string{"/bin/true"}})
	if err == nil {
		t.Fatalf("expected error for duplicate index")
	}
}
