package lifecycle

import (
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
)

// lastUpsertStatus returns the most recent UpsertReplica status recorded for the
// given replica index.
func lastUpsertStatus(st *fakeStore, index int) (string, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	for i := len(st.upsertedReplicas) - 1; i >= 0; i-- {
		if st.upsertedReplicas[i].Index == index {
			return st.upsertedReplicas[i].Status, true
		}
	}
	return "", false
}

func TestHandleIdle_Suspends_WhenFreed(t *testing.T) {
	st := newFakeStore(map[string]*db.App{"app": {ID: 1, Slug: "app", Status: "running", Replicas: 1}}, nil)
	mgr := &fakeManager{suspendFreed: true}
	prx := newFakeProxy()
	prx.seen["app"] = time.Now().Add(-2 * time.Hour)
	prx.hibernateAlways = true
	w := newTestWatcher(Config{HibernateTimeout: time.Minute}, mgr, prx, st, nil)

	w.handleIdle("app", 1)

	if mgr.suspendCalls != 1 {
		t.Fatalf("suspendCalls = %d, want 1", mgr.suspendCalls)
	}
	if len(mgr.stopped) != 0 {
		t.Fatalf("Stop must not be called when suspend freed RAM, got %v", mgr.stopped)
	}
	if got, ok := lastUpsertStatus(st, 0); !ok || got != db.ReplicaStatusSuspended {
		t.Fatalf("replica 0 status = %q ok=%v, want suspended", got, ok)
	}
	if st.appStatus["app"] != "hibernated" {
		t.Fatalf("app status = %q, want hibernated", st.appStatus["app"])
	}
}

func TestHandleIdle_FallsBackToStop_WhenNotFreed(t *testing.T) {
	st := newFakeStore(map[string]*db.App{"app": {ID: 1, Slug: "app", Status: "running", Replicas: 1}}, nil)
	mgr := &fakeManager{suspendFreed: false}
	prx := newFakeProxy()
	prx.seen["app"] = time.Now().Add(-2 * time.Hour)
	prx.hibernateAlways = true
	w := newTestWatcher(Config{HibernateTimeout: time.Minute}, mgr, prx, st, nil)

	w.handleIdle("app", 1)

	if len(mgr.stopped) != 1 {
		t.Fatalf("stopped = %v, want exactly one Stop (fallback)", mgr.stopped)
	}
	if got, ok := lastUpsertStatus(st, 0); !ok || got != "stopped" {
		t.Fatalf("replica 0 status = %q ok=%v, want stopped", got, ok)
	}
}
