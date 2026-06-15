package lifecycle

import (
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

func TestEnforceSuspendedCap_EvictsOldestOverCap(t *testing.T) {
	st := newFakeStore(map[string]*db.App{"app": {ID: 1, Slug: "app", Status: "hibernated", Replicas: 5}}, nil)
	// ListSuspendedReplicas returns oldest-first; index 0,1 are the oldest.
	st.suspendedReplicas = []db.SuspendedReplica{
		{Slug: "app", AppID: 1, Index: 0},
		{Slug: "app", AppID: 1, Index: 1},
		{Slug: "app", AppID: 1, Index: 2},
		{Slug: "app", AppID: 1, Index: 3},
		{Slug: "app", AppID: 1, Index: 4},
	}
	mgr := &fakeManager{}
	w := newTestWatcher(Config{MaxSuspended: 3}, mgr, newFakeProxy(), st, nil)

	w.enforceSuspendedCap()

	// Excess = 5 - 3 = 2 -> evict the two oldest (index 0 and 1).
	if len(mgr.stoppedReplicas) != 2 {
		t.Fatalf("evicted %d replicas, want 2: %v", len(mgr.stoppedReplicas), mgr.stoppedReplicas)
	}
	if mgr.stoppedReplicas[0] != (replicaKey{"app", 0}) || mgr.stoppedReplicas[1] != (replicaKey{"app", 1}) {
		t.Fatalf("evicted %v, want oldest-first index 0,1", mgr.stoppedReplicas)
	}
	stopped := 0
	for _, ur := range st.upsertedReplicas {
		if ur.Status == "stopped" && (ur.Index == 0 || ur.Index == 1) {
			stopped++
		}
	}
	if stopped != 2 {
		t.Fatalf("expected 2 evicted replicas marked stopped, got %d", stopped)
	}
}

func TestEnforceSuspendedCap_NoopUnderCapAndWhenDisabled(t *testing.T) {
	st := newFakeStore(map[string]*db.App{"app": {ID: 1, Slug: "app", Status: "hibernated", Replicas: 2}}, nil)
	st.suspendedReplicas = []db.SuspendedReplica{
		{Slug: "app", AppID: 1, Index: 0},
		{Slug: "app", AppID: 1, Index: 1},
	}
	mgr := &fakeManager{}

	// Under the cap: no eviction.
	newTestWatcher(Config{MaxSuspended: 5}, mgr, newFakeProxy(), st, nil).enforceSuspendedCap()
	if len(mgr.stoppedReplicas) != 0 {
		t.Fatalf("evicted under cap: %v", mgr.stoppedReplicas)
	}

	// Disabled (0): no eviction even when far over.
	newTestWatcher(Config{MaxSuspended: 0}, mgr, newFakeProxy(), st, nil).enforceSuspendedCap()
	if len(mgr.stoppedReplicas) != 0 {
		t.Fatalf("evicted when disabled: %v", mgr.stoppedReplicas)
	}
}
