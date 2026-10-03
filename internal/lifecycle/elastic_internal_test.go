package lifecycle

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
)

func TestWaitElasticHealthyUsesReadinessContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health/ready" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	if err := waitElasticHealthy(server.URL, "/health/ready", http.StatusNoContent, time.Second, nil, func() bool { return true }); err != nil {
		t.Fatalf("declared readiness contract failed: %v", err)
	}
	if err := waitElasticHealthy(server.URL, "/", 0, 300*time.Millisecond, nil, func() bool { return true }); err == nil {
		t.Fatal("default readiness accepted a 404")
	}
}

// TestElasticSpawner_PanicAfterReserveReleasesReservation proves a panic
// inside the fenced spawn body, before anything is started, still releases
// the workerBooting reservation the proxy made before dispatching Spawn. The
// panic fires via testPanicAfterReserve, an unexported hook standing in for a
// real panic partway through the fenced body without needing a runtime that
// actually crashes mid-launch. The Manager has nothing adopted for this slot,
// so recoverSpawn's TerminateConfirmed call takes its GetReplica-miss early
// return; only releaseReservation has anything to do.
//
// This calls spawnFenced directly rather than through Spawn: Spawn's own
// pre-fence recover is deferred before spawnFenced is even called, so it
// would also catch (and mask the absence of) spawnFenced's own recover,
// since Go propagates a panic up through every enclosing frame on the same
// goroutine until something recovers it. Calling spawnFenced directly keeps
// spawnFenced's own recoverSpawn defer the only thing that can catch this.
func TestElasticSpawner_PanicAfterReserveReleasesReservation(t *testing.T) {
	prx := proxy.New()
	prx.SetPoolMode("panicreserve", config.IsolationPerSession, 1, 5)
	const slotID = 2
	// Stands in for the workerBooting placeholder the proxy inserts before
	// dispatching `go spawner.Spawn(...)`, so the assertion below proves the
	// panic path actually releases it rather than merely observing that
	// nothing was ever there.
	if err := prx.RegisterElasticWorker("panicreserve", slotID, "http://127.0.0.1:9778", nil, 1); err != nil {
		t.Fatalf("seed reservation: %v", err)
	}
	if got := prx.ElasticWorkerCount("panicreserve"); got != 1 {
		t.Fatalf("expected 1 pre-existing slot before spawn, got %d", got)
	}

	spawner := &ElasticSpawner{
		Manager: process.NewManager(t.TempDir(), &scriptedStopRuntime{}),
		Proxy:   prx,
	}
	spawner.testPanicAfterReserve = func() { panic("boom-after-reserve") }

	spawner.spawnFenced("panicreserve", slotID) // must not crash the test binary

	if got := prx.ElasticWorkerCount("panicreserve"); got != 0 {
		t.Errorf("elastic worker count = %d after panic recovery, want 0 (reservation released)", got)
	}
}

// TestSpawn_PanicInAppOperationFenceReleasesReservation covers the other
// panic site Spawn recovers itself, before the fenced body: a panic raised by
// the AcquireAppOperation callback. Nothing has been started or registered at
// that point in a real spawn, so this exercises Spawn's own pre-fence recover
// rather than recoverSpawn.
func TestSpawn_PanicInAppOperationFenceReleasesReservation(t *testing.T) {
	prx := proxy.New()
	prx.SetPoolMode("panicfence", config.IsolationPerSession, 1, 5)
	const slotID = 4
	if err := prx.RegisterElasticWorker("panicfence", slotID, "http://127.0.0.1:9779", nil, 1); err != nil {
		t.Fatalf("seed reservation: %v", err)
	}

	spawner := &ElasticSpawner{
		Manager:             process.NewManager(t.TempDir(), &scriptedStopRuntime{}),
		Proxy:               prx,
		AcquireAppOperation: func(string) (func(), error) { panic("boom-acquire-app-operation") },
	}

	spawner.Spawn("panicfence", slotID) // must not crash the test binary

	if got := prx.ElasticWorkerCount("panicfence"); got != 0 {
		t.Errorf("elastic worker count = %d after panic recovery, want 0 (reservation released)", got)
	}
}

// resumeConfirmingRuntime behaves like scriptedStopRuntime but confirms the
// exit immediately (Wait returns nil right away instead of blocking forever),
// so StopReplicaConfirmed succeeds within the grace window instead of always
// timing out into ErrStopUnconfirmed.
type resumeConfirmingRuntime struct{ scriptedStopRuntime }

func (r *resumeConfirmingRuntime) Wait(context.Context, process.RunHandle) error { return nil }

// seedSuspendedElasticReplica adopts a suspended replica into mgr and
// registers a matching suspended worker with prx, standing in for a warm
// spare that Resume was dispatched to wake.
func seedSuspendedElasticReplica(t *testing.T, mgr *process.Manager, prx *proxy.Proxy, slug string, slotID int, appID, depID int64) {
	t.Helper()
	mgr.Adopt(slug, process.ProcessInfo{
		Slug: slug, Index: slotID, Status: process.StatusSuspended,
		AppID: appID, DeploymentID: depID, PID: 4242,
	}, process.RunHandle{PID: 4242})
	prx.SetPoolAppID(slug, appID)
	if err := prx.RegisterSuspendedElasticWorker(slug, slotID, "http://127.0.0.1:9780", nil, depID, appID); err != nil {
		t.Fatalf("seed suspended worker: %v", err)
	}
}

// TestResume_PanicInAppOperationFenceStopsConfirmedAndReleasesSlot proves a
// panic before Resume's app-operation fence - recovered by the same
// recoverSpawn used after the fence, reused directly as Resume's own
// pre-fence recover - stops a warm spare it already owns with confirmed
// semantics rather than leaving a suspended, unreachable process with
// nothing to wake or reclaim it.
func TestResume_PanicInAppOperationFenceStopsConfirmedAndReleasesSlot(t *testing.T) {
	prx := proxy.New()
	prx.SetPoolMode("resumeconfirmed", config.IsolationPerSession, 1, 5)
	mgr := process.NewManager(t.TempDir(), &resumeConfirmingRuntime{})
	const slotID = 1
	seedSuspendedElasticReplica(t, mgr, prx, "resumeconfirmed", slotID, 1, 1)

	spawner := &ElasticSpawner{
		Manager:             mgr,
		Proxy:               prx,
		AcquireAppOperation: func(string) (func(), error) { panic("boom-resume-acquire") },
	}

	spawner.Resume("resumeconfirmed", slotID) // must not crash the test binary

	if _, ok := mgr.GetReplica("resumeconfirmed", slotID); ok {
		t.Error("manager entry still present after a confirmed stop; want it gone")
	}
	if got := prx.ElasticWorkerCount("resumeconfirmed"); got != 0 {
		t.Errorf("elastic worker count = %d after panic recovery, want 0 (slot released)", got)
	}
}

// TestResume_PanicInAppOperationFenceQueuesUnconfirmedStop covers the other
// half of the same panic site: when the stop cannot be confirmed within the
// grace window, the worker's captured identity must be queued for the
// watcher's pending-stop retry loop rather than dropped, even though the
// proxy slot is still released immediately.
func TestResume_PanicInAppOperationFenceQueuesUnconfirmedStop(t *testing.T) {
	prx := proxy.New()
	prx.SetPoolMode("resumeunconfirmed", config.IsolationPerSession, 1, 5)
	mgr := process.NewManager(t.TempDir(), &scriptedStopRuntime{})
	mgr.SetStopGrace(20 * time.Millisecond)
	const slotID = 1
	seedSuspendedElasticReplica(t, mgr, prx, "resumeunconfirmed", slotID, 7, 3)

	queue := &fakePendingQueue{}
	spawner := &ElasticSpawner{
		Manager:             mgr,
		Proxy:               prx,
		AcquireAppOperation: func(string) (func(), error) { panic("boom-resume-acquire") },
		EnqueuePendingStop:  queue.QueuePendingStop,
	}

	spawner.Resume("resumeunconfirmed", slotID) // must not crash the test binary

	if got := prx.ElasticWorkerCount("resumeunconfirmed"); got != 0 {
		t.Errorf("elastic worker count = %d after panic recovery, want 0 (slot released)", got)
	}
	if len(queue.entries) != 1 {
		t.Fatalf("queued %d entries, want exactly 1", len(queue.entries))
	}
	e := queue.entries[0]
	if e.Kind != pendingStopElasticHibernate {
		t.Errorf("Kind = %v, want pendingStopElasticHibernate", e.Kind)
	}
	if e.Slug != "resumeunconfirmed" || e.Index != slotID || e.AppID != 7 || e.DeploymentID != 3 {
		t.Errorf("queued entry = %+v, want slug=resumeunconfirmed index=%d appID=7 depID=3", e, slotID)
	}
	// The retry must target the incarnation that is still in the slot: a
	// missing one reads as "already gone" and would drop a live process.
	gen, _, ok := mgr.ReplicaIncarnation("resumeunconfirmed", slotID)
	if !ok {
		t.Fatal("unconfirmed stop removed the manager entry")
	}
	if e.Incarnation != gen || e.Stopped {
		t.Errorf("queued Incarnation=%d Stopped=%v, want Incarnation=%d Stopped=false", e.Incarnation, e.Stopped, gen)
	}
}
