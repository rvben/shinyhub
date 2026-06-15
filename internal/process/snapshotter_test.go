package process

import (
	"context"
	"testing"
)

// fakeSnapshotRuntime implements Runtime + Snapshotter for tests. It embeds
// NativeRuntime for the Runtime method set and lets each test dictate freed/err
// on Suspend and the endpoint/err on Resume. The embedded NativeRuntime methods
// are never invoked by the Suspend/Resume paths, so its zero maps are fine.
type fakeSnapshotRuntime struct {
	NativeRuntime
	suspendFreed bool
	suspendErr   error
	resumeEP     ReplicaEndpoint
	resumeErr    error
	suspendCalls int
	resumeCalls  int
}

func (f *fakeSnapshotRuntime) Suspend(_ context.Context, _ RunHandle) (bool, error) {
	f.suspendCalls++
	return f.suspendFreed, f.suspendErr
}

func (f *fakeSnapshotRuntime) Resume(_ context.Context, _ RunHandle) (ReplicaEndpoint, error) {
	f.resumeCalls++
	return f.resumeEP, f.resumeErr
}

func TestSnapshotter_InterfaceSatisfied(t *testing.T) {
	var _ Snapshotter = (*fakeSnapshotRuntime)(nil)
	var _ Runtime = (*fakeSnapshotRuntime)(nil)
	if StatusSuspended != "suspended" {
		t.Fatalf("StatusSuspended = %q, want %q", StatusSuspended, "suspended")
	}
}
