package lifecycle

import (
	"sync"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/process"
)

func newElasticHibernateConcurrencyFixture(term *fakeElasticTerminator, slots int) (*Watcher, *db.App) {
	mgr := &fakeManager{}
	for i := 0; i < slots; i++ {
		mgr.entries = append(mgr.entries, &process.ProcessInfo{
			Slug: "app", Index: i, Status: process.StatusRunning,
			AppID: 1, DeploymentID: 5, PID: 300 + i, Tier: "python",
		})
	}
	app := &db.App{ID: 1, Slug: "app", Status: "running", WorkerIsolation: string(config.IsolationGrouped)}
	st := newFakeStore(map[string]*db.App{"app": app}, nil)
	w := newTestWatcher(Config{}, mgr, newFakeProxy(), st,
		func(string, string, int) (*deploy.Result, error) { return &deploy.Result{}, nil })
	w.SetElasticTerminator(term)
	return w, app
}

// TestHibernateElasticPool_StopsWorkersConcurrently asserts every worker's
// confirmed stop is in flight at once. Each TerminateConfirmed can block for
// a full stop grace window, and hibernation runs on the watcher tick, so
// stopping a pool one worker at a time holds the tick for workers x grace
// instead of a single grace window. The fake holds every call open until all
// slots have entered it; run one at a time, the first call never sees the
// others arrive and gives up.
func TestHibernateElasticPool_StopsWorkersConcurrently(t *testing.T) {
	const slots = 3
	var (
		mu       sync.Mutex
		entered  int
		all      = make(chan struct{})
		timedOut []int
	)
	term := &fakeElasticTerminator{during: func(_ string, slotID int) {
		mu.Lock()
		entered++
		if entered == slots {
			close(all)
		}
		mu.Unlock()
		select {
		case <-all:
		case <-time.After(2 * time.Second):
			mu.Lock()
			timedOut = append(timedOut, slotID)
			mu.Unlock()
		}
	}}
	w, app := newElasticHibernateConcurrencyFixture(term, slots)

	if !w.hibernateElasticPool(app) {
		t.Fatal("hibernateElasticPool returned false, want true")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(timedOut) != 0 {
		t.Fatalf("slots %v waited for siblings that never started; want every worker's stop in flight at once", timedOut)
	}
	if len(term.calls) != slots {
		t.Fatalf("TerminateConfirmed calls = %v, want one per slot", term.calls)
	}
	if len(w.pendingStops) != 0 {
		t.Fatalf("pending stops = %v, want none when every stop confirms", w.pendingStops)
	}
}

// TestHibernateElasticPool_PanickingStopQueuesThatSlot asserts a panic in one
// worker's stop neither crashes the server (the stop runs on its own
// goroutine, where no caller's recover reaches) nor drops the slot: with no
// result to say otherwise, the stop is unconfirmed, so the slot is queued for
// retry under the identity captured from the manager, while its siblings
// finish normally.
func TestHibernateElasticPool_PanickingStopQueuesThatSlot(t *testing.T) {
	term := &fakeElasticTerminator{during: func(_ string, slotID int) {
		if slotID == 1 {
			panic("boom-in-terminate")
		}
	}}
	w, app := newElasticHibernateConcurrencyFixture(term, 3)

	if !w.hibernateElasticPool(app) {
		t.Fatal("hibernateElasticPool returned false, want true")
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.pendingStops) != 1 {
		t.Fatalf("pending stops = %v, want exactly the panicked slot", w.pendingStops)
	}
	for _, e := range w.pendingStops {
		if e.Kind != pendingStopElasticHibernate || e.Index != 1 || e.PID != 301 || e.AppID != 1 || e.DeploymentID != 5 || e.Stopped {
			t.Fatalf("queued entry = %+v, want an unconfirmed elastic-hibernate retry for slot 1 (pid 301, app 1, deployment 5)", e)
		}
	}
}
