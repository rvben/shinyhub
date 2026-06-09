package lifecycle

import (
	"sync"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/process"
)

// fakeRecorder captures lifecycle business-metric calls so tests can assert the
// watcher emits a transition/restart at the right event sites.
type fakeRecorder struct {
	mu          sync.Mutex
	transitions []string
	restarts    int
}

func (f *fakeRecorder) RecordStateTransition(event string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.transitions = append(f.transitions, event)
}

func (f *fakeRecorder) RecordReplicaRestart() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.restarts++
}

func (f *fakeRecorder) hasTransition(event string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, e := range f.transitions {
		if e == event {
			return true
		}
	}
	return false
}

func (f *fakeRecorder) restartCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.restarts
}

// TestMetrics_HibernateRecordsTransition proves a successful hibernation emits a
// "hibernate" state-transition metric, so the rate of apps going idle is
// observable in Prometheus.
func TestMetrics_HibernateRecordsTransition(t *testing.T) {
	st := idleApp()
	w := idleHibernationWatcher(runningMgr(nil), st)
	rec := &fakeRecorder{}
	w.SetMetrics(rec)

	w.runOnce()

	if !rec.hasTransition("hibernate") {
		t.Fatalf("expected a hibernate transition to be recorded, got %v", rec.transitions)
	}
}

// TestMetrics_WakeRecordsTransition proves waking a hibernated app on a proxy
// miss emits a "wake" state-transition metric.
func TestMetrics_WakeRecordsTransition(t *testing.T) {
	prx := newFakeProxy()
	st := newFakeStore(
		map[string]*db.App{"app": {ID: 1, Slug: "app", Status: "hibernated", Replicas: 1}},
		[]*db.Deployment{{BundleDir: "/bundles/v1"}},
	)
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, &fakeManager{}, prx, st,
		func(slug, bundleDir string, idx int) (*deploy.Result, error) {
			return &deploy.Result{Index: idx, PID: 33, Port: 20033}, nil
		})
	rec := &fakeRecorder{}
	w.SetMetrics(rec)

	w.WakeTrigger("app")
	waitNotWaking(t, st, "app")

	if !rec.hasTransition("wake") {
		t.Fatalf("expected a wake transition to be recorded, got %v", rec.transitions)
	}
}

// TestMetrics_RestartRecordsReplicaRestart proves a successful crash-restart
// increments the replica-restart metric, so a flapping app surfaces as a rising
// restart rate.
func TestMetrics_RestartRecordsReplicaRestart(t *testing.T) {
	mgr := &fakeManager{entries: []*process.ProcessInfo{
		{Slug: "myapp", Index: 0, Status: process.StatusCrashed},
	}}
	st := newFakeStore(
		map[string]*db.App{"myapp": {ID: 1, Slug: "myapp", Status: "running", Replicas: 1}},
		[]*db.Deployment{{BundleDir: "/bundles/v1"}},
	)
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, mgr, newFakeProxy(), st,
		func(slug, bundleDir string, idx int) (*deploy.Result, error) {
			return &deploy.Result{Index: idx, PID: 33, Port: 20033}, nil
		})
	rec := &fakeRecorder{}
	w.SetMetrics(rec)

	w.handleCrashed("myapp", 0)

	if rec.restartCount() != 1 {
		t.Fatalf("expected 1 replica restart recorded, got %d", rec.restartCount())
	}
}

// TestMetrics_NilRecorderIsSafe proves the watcher tolerates an unset recorder
// (metrics disabled) without panicking on the event paths.
func TestMetrics_NilRecorderIsSafe(t *testing.T) {
	st := idleApp()
	w := idleHibernationWatcher(runningMgr(nil), st)
	// No SetMetrics call: recorder stays nil.
	w.runOnce() // must not panic
}
