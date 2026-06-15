package process

import (
	"errors"
	"testing"
)

// seedRunningEntry registers rt under a tier and seeds one running entry for
// (slug, index) without launching a real process, so Suspend/Resume can be
// unit-tested in isolation.
func seedRunningEntry(m *Manager, slug, tier string, index int, rt Runtime) {
	m.RegisterRuntime(tier, rt)
	m.mu.Lock()
	defer m.mu.Unlock()
	pool := m.entries[slug]
	for len(pool) <= index {
		pool = append(pool, nil)
	}
	pool[index] = &entry{
		info:   &ProcessInfo{Slug: slug, Index: index, Status: StatusRunning, Tier: tier, EndpointURL: "http://127.0.0.1:1000"},
		handle: RunHandle{PID: 4242},
		tier:   tier,
		done:   make(chan struct{}),
	}
	m.entries[slug] = pool
}

func TestManager_Suspend_FreedMarksSuspended(t *testing.T) {
	m := NewManager(t.TempDir(), NewNativeRuntime())
	rt := &fakeSnapshotRuntime{suspendFreed: true}
	seedRunningEntry(m, "app", "snap", 0, rt)

	freed, err := m.Suspend("app")
	if err != nil || !freed {
		t.Fatalf("Suspend = (%v, %v), want (true, nil)", freed, err)
	}
	info, ok := m.GetReplica("app", 0)
	if !ok || info.Status != StatusSuspended {
		t.Fatalf("status = %v ok=%v, want suspended", info.Status, ok)
	}
	if rt.suspendCalls != 1 {
		t.Fatalf("suspendCalls = %d, want 1", rt.suspendCalls)
	}
}

func TestManager_Suspend_NotFreedRestoresAndReportsFalse(t *testing.T) {
	m := NewManager(t.TempDir(), NewNativeRuntime())
	rt := &fakeSnapshotRuntime{suspendFreed: false}
	seedRunningEntry(m, "app", "snap", 0, rt)

	freed, _ := m.Suspend("app")
	if freed {
		t.Fatalf("freed = true, want false (reclaim did not free RAM)")
	}
	info, _ := m.GetReplica("app", 0)
	if info.Status != StatusRunning {
		t.Fatalf("status = %v, want running (left stoppable)", info.Status)
	}
}

func TestManager_Suspend_NotSnapshotterReturnsSentinel(t *testing.T) {
	m := NewManager(t.TempDir(), NewNativeRuntime())
	seedRunningEntry(m, "app", "plain", 0, NewNativeRuntime())

	freed, err := m.Suspend("app")
	if freed || !errors.Is(err, ErrRuntimeNotSnapshotter) {
		t.Fatalf("Suspend = (%v, %v), want (false, ErrRuntimeNotSnapshotter)", freed, err)
	}
}

func TestManager_Resume_RestoresEndpointAndStatus(t *testing.T) {
	m := NewManager(t.TempDir(), NewNativeRuntime())
	rt := &fakeSnapshotRuntime{
		suspendFreed: true,
		resumeEP:     ReplicaEndpoint{URL: "http://127.0.0.1:2000", Provider: "fake", WorkerID: "w1", Handle: RunHandle{PID: 4242}},
	}
	seedRunningEntry(m, "app", "snap", 0, rt)
	if _, err := m.Suspend("app"); err != nil {
		t.Fatalf("Suspend: %v", err)
	}

	ep, err := m.Resume("app", 0)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if ep.URL != "http://127.0.0.1:2000" {
		t.Fatalf("ep.URL = %q", ep.URL)
	}
	info, _ := m.GetReplica("app", 0)
	if info.Status != StatusRunning || info.EndpointURL != "http://127.0.0.1:2000" {
		t.Fatalf("after resume info = %+v", info)
	}
}

func TestManager_Resume_PreservesURLWhenDriverReturnsEmpty(t *testing.T) {
	m := NewManager(t.TempDir(), NewNativeRuntime())
	// Driver returns an empty URL (in-place resume, e.g. docker unpause); the
	// Manager must preserve the entry's known route URL.
	rt := &fakeSnapshotRuntime{suspendFreed: true, resumeEP: ReplicaEndpoint{Handle: RunHandle{PID: 4242}}}
	seedRunningEntry(m, "app", "snap", 0, rt) // EndpointURL "http://127.0.0.1:1000"
	if _, err := m.Suspend("app"); err != nil {
		t.Fatalf("suspend: %v", err)
	}

	ep, err := m.Resume("app", 0)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if ep.URL != "http://127.0.0.1:1000" {
		t.Fatalf("ep.URL = %q, want preserved http://127.0.0.1:1000", ep.URL)
	}
	info, _ := m.GetReplica("app", 0)
	if info.EndpointURL != "http://127.0.0.1:1000" {
		t.Fatalf("entry URL = %q, want preserved", info.EndpointURL)
	}
}

func TestManager_Resume_NotSuspendedReturnsSentinel(t *testing.T) {
	m := NewManager(t.TempDir(), NewNativeRuntime())
	rt := &fakeSnapshotRuntime{}
	seedRunningEntry(m, "app", "snap", 0, rt) // running, not suspended

	_, err := m.Resume("app", 0)
	if !errors.Is(err, ErrReplicaNotSuspended) {
		t.Fatalf("Resume err = %v, want ErrReplicaNotSuspended", err)
	}
}
