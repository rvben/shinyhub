package lifecycle

import (
	"sync"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/process"
)

// TestWatchdog_LostReplicaIsNotCrashed asserts the watchdog leaves a replica
// whose worker is gone alone: no crash is persisted and no restart is attempted.
//
// This is the invariant the worker-down recovery path depends on. The heartbeat
// monitor transitions a lost worker's replicas by finding their rows still
// 'running' and CAS-ing them to 'lost'; a fabricated crash row would flip the
// status out from under it, so the sweep would find nothing to lose, the app
// would burn its restart budget re-placing onto a dead worker, and once that
// budget was spent the app would go terminally crashed and leave the
// reconcilable set - never recovering when a replacement worker joins.
func TestWatchdog_LostReplicaIsNotCrashed(t *testing.T) {
	mgr := &fakeManager{entries: []*process.ProcessInfo{
		{Slug: "myapp", Index: 0, Status: process.StatusLost, WorkerID: "node-a"},
	}}
	st := newFakeStore(
		map[string]*db.App{"myapp": {ID: 1, Slug: "myapp", Status: "running", Replicas: 1}},
		[]*db.Deployment{{BundleDir: "/bundles/v1"}},
	)
	// The persisted row the worker-down sweep will act on.
	st.replicas = map[int64][]*db.Replica{1: {
		{AppID: 1, Index: 0, Status: db.ReplicaStatusRunning, WorkerID: "node-a", Tier: "remote"},
	}}

	var mu sync.Mutex
	var deployed []string
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, mgr, newFakeProxy(), st,
		func(slug, _ string, idx int) (*deploy.Result, error) {
			mu.Lock()
			deployed = append(deployed, slug)
			mu.Unlock()
			return &deploy.Result{Index: idx, PID: 11, Port: 20011}, nil
		})

	w.runOnce()

	mu.Lock()
	defer mu.Unlock()
	if len(deployed) != 0 {
		t.Errorf("a lost replica must not be re-deployed by the watchdog, got %v", deployed)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, p := range st.upsertedReplicas {
		if p.Status == "crashed" {
			t.Fatalf("worker loss persisted a crash: %+v", p)
		}
	}
	if got := st.replicas[1][0].Status; got != db.ReplicaStatusRunning {
		t.Fatalf("replica row status = %q, want %q so the worker-down sweep can still transition it",
			got, db.ReplicaStatusRunning)
	}
}
