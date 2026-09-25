package process

import (
	"context"
	"io"
	"testing"
	"time"
)

// raceFake is a Runtime+Snapshotter used to reproduce a race between an
// in-flight Suspend/Resume call and an unrelated exit (crash/OOM/lost worker)
// landing on the same replica while that call is blocked in the runtime,
// after Manager has released m.mu. Wait blocks until the test closes exited,
// so the exit-monitor goroutine spawned by Start only completes when the test
// says so. suspendGate/resumeGate, when non-nil, block the corresponding
// Snapshotter method until the test closes them, and close entered first so
// the test can deterministically wait for the call to be in flight before
// triggering the exit — no sleeps.
type raceFake struct {
	NativeRuntime
	exited      chan struct{}
	suspendGate chan struct{}
	resumeGate  chan struct{}
	entered     chan struct{}
}

func (f *raceFake) Start(context.Context, StartParams, io.Writer) (ReplicaEndpoint, error) {
	return ReplicaEndpoint{URL: "http://127.0.0.1:9", Handle: RunHandle{ContainerID: "c1"}}, nil
}

func (f *raceFake) Wait(_ context.Context, _ RunHandle) error {
	<-f.exited
	return nil
}

func (f *raceFake) Suspend(_ context.Context, _ RunHandle) (bool, error) {
	if f.suspendGate != nil {
		close(f.entered)
		<-f.suspendGate
	}
	return true, nil
}

func (f *raceFake) Resume(_ context.Context, _ RunHandle) (ReplicaEndpoint, error) {
	if f.resumeGate != nil {
		close(f.entered)
		<-f.resumeGate
	}
	return ReplicaEndpoint{URL: "http://127.0.0.1:9", Handle: RunHandle{ContainerID: "c1"}}, nil
}

// waitForReplicaStatus polls GetReplica until it reports the wanted status or
// fails the test after a bounded deadline. Used instead of a sleep to
// deterministically observe the exit-monitor goroutine's async status update
// before releasing a runtime call blocked on a gate.
func waitForReplicaStatus(t *testing.T, m *Manager, slug string, index int, want Status) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		info, ok := m.GetReplica(slug, index)
		if ok && info.Status == want {
			return
		}
		if time.Now().After(deadline) {
			got := Status("<missing>")
			if ok {
				got = info.Status
			}
			t.Fatalf("replica %s/%d status = %v, want %v within deadline", slug, index, got, want)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestManager_SuspendReplica_DoesNotResurrectAReplicaThatExitedMidSuspend
// reproduces: SuspendReplica reads the entry's handle, releases m.mu, and
// blocks in the runtime's Suspend call. While it is blocked, the replica's
// process exits for an unrelated reason (e.g. OOM) and the exit-monitor
// goroutine records StatusCrashed on the very same entry — without removing
// it from the pool or changing its handle. When the blocked Suspend call
// returns, SuspendReplica's post-unlock re-check only compares
// pool[index].handle == handle, which still matches, so it overwrites
// StatusCrashed back to StatusSuspended: a dead replica is reported suspended.
func TestManager_SuspendReplica_DoesNotResurrectAReplicaThatExitedMidSuspend(t *testing.T) {
	f := &raceFake{
		exited:      make(chan struct{}),
		suspendGate: make(chan struct{}),
		entered:     make(chan struct{}),
	}
	m := NewManager(t.TempDir(), f)
	if _, err := m.Start(StartParams{Slug: "app", Index: 0, Command: []string{"true"}, Dir: t.TempDir(), Port: 9}); err != nil {
		t.Fatalf("start: %v", err)
	}

	suspendDone := make(chan error, 1)
	go func() {
		_, err := m.SuspendReplica("app", 0)
		suspendDone <- err
	}()

	<-f.entered // Suspend is blocked inside the runtime call, holding no lock.

	close(f.exited) // the replica dies for an unrelated reason mid-suspend.
	waitForReplicaStatus(t, m, "app", 0, StatusCrashed)

	close(f.suspendGate) // let the blocked Suspend call return.
	if err := <-suspendDone; err != nil {
		t.Fatalf("SuspendReplica: %v", err)
	}

	info, ok := m.GetReplica("app", 0)
	if !ok {
		t.Fatal("GetReplica: replica not found")
	}
	if info.Status != StatusCrashed {
		t.Fatalf("status = %v, want %v (a replica that exited mid-suspend must not be resurrected)", info.Status, StatusCrashed)
	}
}

// TestManager_Resume_DoesNotResurrectAReplicaThatExitedMidResume is the mirror
// image for Resume: a replica is suspended, then Resume is raced against an
// unrelated exit landing on the same entry while Resume is blocked in the
// runtime's Resume call.
func TestManager_Resume_DoesNotResurrectAReplicaThatExitedMidResume(t *testing.T) {
	f := &raceFake{
		exited:     make(chan struct{}),
		resumeGate: make(chan struct{}),
		entered:    make(chan struct{}),
	}
	m := NewManager(t.TempDir(), f)
	if _, err := m.Start(StartParams{Slug: "app", Index: 0, Command: []string{"true"}, Dir: t.TempDir(), Port: 9}); err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := m.SuspendReplica("app", 0); err != nil {
		t.Fatalf("SuspendReplica: %v", err)
	}
	if info, ok := m.GetReplica("app", 0); !ok || info.Status != StatusSuspended {
		t.Fatalf("precondition: status = %v, want suspended", info)
	}

	resumeDone := make(chan error, 1)
	go func() {
		_, err := m.Resume("app", 0)
		resumeDone <- err
	}()

	<-f.entered // Resume is blocked inside the runtime call, holding no lock.

	close(f.exited) // the replica dies for an unrelated reason mid-resume.
	waitForReplicaStatus(t, m, "app", 0, StatusCrashed)

	close(f.resumeGate) // let the blocked Resume call return.
	if err := <-resumeDone; err != nil {
		t.Fatalf("Resume: %v", err)
	}

	info, ok := m.GetReplica("app", 0)
	if !ok {
		t.Fatal("GetReplica: replica not found")
	}
	if info.Status != StatusCrashed {
		t.Fatalf("status = %v, want %v (a replica that exited mid-resume must not be resurrected)", info.Status, StatusCrashed)
	}
}
