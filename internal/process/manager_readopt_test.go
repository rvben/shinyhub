package process

import (
	"errors"
	"testing"
)

// TestManager_Adopt_ReregistersWarmState verifies that adopting a replica after
// a restart re-registers its warm-wake state via the runtime's WarmReadopter, so
// the re-adopted replica can later be warm-frozen and warm-resumed instead of
// cold-booting on its next hibernate.
func TestManager_Adopt_ReregistersWarmState(t *testing.T) {
	m := NewManager(t.TempDir(), NewNativeRuntime())
	rt := &fakeSnapshotRuntime{}
	m.RegisterRuntime("snap", rt)

	m.Adopt("demo", ProcessInfo{Slug: "demo", Index: 0, PID: 4242, Tier: "snap", Status: StatusRunning}, RunHandle{PID: 4242})

	if rt.readoptCalls != 1 {
		t.Fatalf("ReadoptWarm called %d times, want 1", rt.readoptCalls)
	}
	if got := rt.readoptLast; got.slug != "demo" || got.index != 0 || got.pid != 4242 {
		t.Fatalf("ReadoptWarm args = %+v, want {demo 0 4242}", got)
	}
}

// TestManager_Adopt_WarmReadoptOff is silent when warm-wake is unavailable: a
// runtime that reports ErrRuntimeNotSnapshotter must not fail the adoption - the
// replica simply hibernates via Stop as before.
func TestManager_Adopt_WarmReadoptOff(t *testing.T) {
	m := NewManager(t.TempDir(), NewNativeRuntime())
	rt := &fakeSnapshotRuntime{readoptErr: ErrRuntimeNotSnapshotter}
	m.RegisterRuntime("snap", rt)

	m.Adopt("demo", ProcessInfo{Slug: "demo", Index: 0, PID: 1, Tier: "snap", Status: StatusRunning}, RunHandle{PID: 1})

	if _, ok := m.GetReplica("demo", 0); !ok {
		t.Fatalf("adopted replica must be present even when warm-wake is unavailable")
	}
	if rt.readoptCalls != 1 {
		t.Fatalf("ReadoptWarm called %d times, want 1", rt.readoptCalls)
	}
}

// TestManager_Adopt_WarmReadoptErrorIsNonFatal: a real re-adopt error (the
// cgroup is gone) must not abort the adoption; the replica still adopts and
// degrades to stop-hibernate.
func TestManager_Adopt_WarmReadoptErrorIsNonFatal(t *testing.T) {
	m := NewManager(t.TempDir(), NewNativeRuntime())
	rt := &fakeSnapshotRuntime{readoptErr: errors.New("cgroup gone")}
	m.RegisterRuntime("snap", rt)

	m.Adopt("demo", ProcessInfo{Slug: "demo", Index: 0, PID: 1, Tier: "snap", Status: StatusRunning}, RunHandle{PID: 1})

	if _, ok := m.GetReplica("demo", 0); !ok {
		t.Fatalf("a warm re-adopt error must not abort the adoption")
	}
}
