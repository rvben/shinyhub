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
	readoptCalls int
	readoptLast  readoptCall
	readoptErr   error
}

type readoptCall struct {
	slug  string
	index int
	pid   int
}

func (f *fakeSnapshotRuntime) Suspend(_ context.Context, _ RunHandle) (bool, error) {
	f.suspendCalls++
	return f.suspendFreed, f.suspendErr
}

func (f *fakeSnapshotRuntime) Resume(_ context.Context, _ RunHandle) (ReplicaEndpoint, error) {
	f.resumeCalls++
	return f.resumeEP, f.resumeErr
}

// ReadoptWarm overrides the embedded NativeRuntime's so the Adopt wiring can be
// asserted without touching real cgroups.
func (f *fakeSnapshotRuntime) ReadoptWarm(slug string, index, pid int) error {
	f.readoptCalls++
	f.readoptLast = readoptCall{slug: slug, index: index, pid: pid}
	return f.readoptErr
}

func TestSnapshotter_InterfaceSatisfied(t *testing.T) {
	var _ Snapshotter = (*fakeSnapshotRuntime)(nil)
	var _ Runtime = (*fakeSnapshotRuntime)(nil)
	// The native runtime (and the fake) must satisfy WarmReadopter so Manager.
	// Adopt can re-register warm-wake state after a restart.
	var _ WarmReadopter = (*NativeRuntime)(nil)
	var _ WarmReadopter = (*fakeSnapshotRuntime)(nil)
	if StatusSuspended != "suspended" {
		t.Fatalf("StatusSuspended = %q, want %q", StatusSuspended, "suspended")
	}
}
