package process

import (
	"context"
	"io"
	"syscall"
	"testing"
)

// stopFake is a Runtime+Snapshotter whose Wait blocks until Signal fires
// (simulating a process that exits on SIGTERM), so StopReplica's wait-for-exit
// loop completes. It records whether Resume (unfreeze) ran before Signal.
type stopFake struct {
	NativeRuntime
	resumed            bool
	signaled           bool
	resumeBeforeSignal bool
	wait               chan struct{}
}

func (f *stopFake) Start(context.Context, StartParams, io.Writer) (ReplicaEndpoint, error) {
	return ReplicaEndpoint{URL: "http://127.0.0.1:9", Handle: RunHandle{ContainerID: "c1"}}, nil
}
func (f *stopFake) Wait(_ context.Context, _ RunHandle) error { <-f.wait; return nil }
func (f *stopFake) Signal(_ RunHandle, _ syscall.Signal) error {
	if !f.signaled {
		f.signaled = true
		f.resumeBeforeSignal = f.resumed
		close(f.wait) // the "process" exits
	}
	return nil
}
func (f *stopFake) Suspend(context.Context, RunHandle) (bool, error) { return true, nil }
func (f *stopFake) Resume(context.Context, RunHandle) (ReplicaEndpoint, error) {
	f.resumed = true
	return ReplicaEndpoint{URL: "http://127.0.0.1:9", Handle: RunHandle{ContainerID: "c1"}}, nil
}

func TestStopReplica_UnfreezesSuspendedFirst(t *testing.T) {
	f := &stopFake{wait: make(chan struct{})}
	m := NewManager(t.TempDir(), f)
	if _, err := m.Start(StartParams{Slug: "app", Index: 0, Command: []string{"true"}, Dir: t.TempDir(), Port: 9}); err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := m.Suspend("app"); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	if info, _ := m.GetReplica("app", 0); info.Status != StatusSuspended {
		t.Fatalf("precondition: status = %v, want suspended", info.Status)
	}

	if err := m.StopReplica("app", 0); err != nil {
		t.Fatalf("StopReplica: %v", err)
	}
	if !f.resumed {
		t.Fatal("a suspended replica must be unfrozen (Resume) before stop")
	}
	if !f.resumeBeforeSignal {
		t.Fatal("unfreeze (Resume) must happen BEFORE Signal")
	}
}
