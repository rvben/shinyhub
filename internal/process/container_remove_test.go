package process_test

import (
	"context"
	"io"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/process"
)

// fakeContainerRuntime is a Runtime whose handles carry a ContainerID and
// which records RemoveHandle calls, so we can assert the Manager deletes the
// backing container once a replica has stopped (no AutoRemove on long-running
// app containers, so they must be explicitly reaped on stop/replace).
type fakeContainerRuntime struct {
	mu      sync.Mutex
	nextID  int
	stops   map[string]chan struct{}
	removed []string
}

func newFakeContainerRuntime() *fakeContainerRuntime {
	return &fakeContainerRuntime{nextID: 1, stops: make(map[string]chan struct{})}
}

func (f *fakeContainerRuntime) Start(_ context.Context, _ process.StartParams, _ io.Writer) (process.RunHandle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := "ctr-" + time.Now().Format("150405.000000") + "-" + strconv.Itoa(f.nextID)
	f.nextID++
	f.stops[id] = make(chan struct{})
	return process.RunHandle{ContainerID: id}, nil
}

func (f *fakeContainerRuntime) Signal(h process.RunHandle, sig syscall.Signal) error {
	f.mu.Lock()
	ch, ok := f.stops[h.ContainerID]
	f.mu.Unlock()
	if ok && (sig == syscall.SIGTERM || sig == syscall.SIGKILL) {
		select {
		case <-ch:
		default:
			close(ch)
		}
	}
	return nil
}

func (f *fakeContainerRuntime) Wait(_ context.Context, h process.RunHandle) error {
	f.mu.Lock()
	ch, ok := f.stops[h.ContainerID]
	f.mu.Unlock()
	if ok {
		<-ch
	}
	return nil
}

func (f *fakeContainerRuntime) Stats(context.Context, process.RunHandle) (float64, uint64, error) {
	return 0, 0, nil
}

func (f *fakeContainerRuntime) RunOnce(context.Context, process.StartParams, io.Writer) (process.ExitInfo, error) {
	return process.ExitInfo{}, nil
}

func (f *fakeContainerRuntime) HostPreparesDeps() bool { return false }
func (f *fakeContainerRuntime) AppBindHost() string    { return "0.0.0.0" }

// RemoveHandle satisfies the optional containerRemover capability the Manager
// type-asserts for after a replica stops.
func (f *fakeContainerRuntime) RemoveHandle(h process.RunHandle) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, h.ContainerID)
	return nil
}

// TestStopReplica_RemovesContainer verifies that stopping a replica backed by
// a container runtime force-removes the container, so stopped containers do
// not accumulate across deploys/restarts.
func TestStopReplica_RemovesContainer(t *testing.T) {
	rt := newFakeContainerRuntime()
	m := process.NewManager(t.TempDir(), rt)

	info, err := m.Start(process.StartParams{
		Slug:    "demo",
		Dir:     t.TempDir(),
		Command: []string{"app"},
		Port:    19010,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := m.StopReplica("demo", info.Index); err != nil {
		t.Fatalf("stop replica: %v", err)
	}

	rt.mu.Lock()
	removed := append([]string(nil), rt.removed...)
	rt.mu.Unlock()
	if len(removed) != 1 {
		t.Fatalf("RemoveHandle calls = %v, want exactly 1", removed)
	}
}
