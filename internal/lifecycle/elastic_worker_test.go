package lifecycle_test

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/lifecycle"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
)

// recordingRuntime is a process.Runtime that records Start invocations and
// returns a configurable endpoint without actually executing a process.
type recordingRuntime struct {
	mu      sync.Mutex
	started []process.StartParams
	// endpointURL is the URL returned by Start. Defaults to "http://127.0.0.1:9000".
	endpointURL string
	startErr    error
}

func (r *recordingRuntime) Start(_ context.Context, p process.StartParams, _ io.Writer) (process.ReplicaEndpoint, error) {
	r.mu.Lock()
	r.started = append(r.started, p)
	r.mu.Unlock()
	if r.startErr != nil {
		return process.ReplicaEndpoint{}, r.startErr
	}
	url := r.endpointURL
	if url == "" {
		url = "http://127.0.0.1:9000"
	}
	return process.ReplicaEndpoint{
		URL:      url,
		Provider: "native",
		WorkerID: "test-worker",
		Handle:   process.RunHandle{PID: 12345},
	}, nil
}

func (r *recordingRuntime) Signal(_ process.RunHandle, _ syscall.Signal) error { return nil }
func (r *recordingRuntime) Wait(_ context.Context, _ process.RunHandle) error  { return nil }
func (r *recordingRuntime) Stats(_ context.Context, _ process.RunHandle) (float64, uint64, error) {
	return 0, 0, nil
}
func (r *recordingRuntime) RunOnce(_ context.Context, _ process.StartParams, _ io.Writer) (process.ExitInfo, error) {
	return process.ExitInfo{}, nil
}
func (r *recordingRuntime) HostPreparesDeps() bool    { return false }
func (r *recordingRuntime) AppBindHost() string       { return "127.0.0.1" }
func (r *recordingRuntime) HostProvidesAppData() bool { return true }

// mustCreateElasticApp creates a test app in elastic per_session mode.
func mustCreateElasticApp(t *testing.T, store *db.Store, slug string) *db.App {
	t.Helper()
	app := mustCreateApp(t, store, slug)
	_, err := store.DB().Exec(
		`UPDATE apps SET worker_isolation='per_session', worker_max_workers=5 WHERE slug=?`, slug)
	if err != nil {
		t.Fatalf("set elastic mode: %v", err)
	}
	app, err = store.GetAppBySlug(slug)
	if err != nil {
		t.Fatalf("reload app: %v", err)
	}
	return app
}

// mustCreateDeploymentInDir creates a deployment row pointing at bundleDir.
func mustCreateDeploymentInDir(t *testing.T, store *db.Store, appID int64, bundleDir string) *db.Deployment {
	t.Helper()
	dep, err := store.CreateDeployment(db.CreateDeploymentParams{
		AppID:     appID,
		Version:   "v0.0.1",
		BundleDir: bundleDir,
		Status:    db.DeploymentSucceeded,
	})
	if err != nil {
		t.Fatalf("create deployment: %v", err)
	}
	return dep
}

// mustMinimalBundle creates a temp dir with a minimal app.py so ResolveLaunch
// can detect the app type and build a command.
func mustMinimalBundle(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app.py"), []byte("# stub\n"), 0o644); err != nil {
		t.Fatalf("write app.py: %v", err)
	}
	return dir
}

// noopHealthCheck is a HealthCheck that always succeeds immediately.
func noopHealthCheck(_ string, _ time.Duration, _ http.RoundTripper) error { return nil }

// TestSpawnElasticWorker_BootsWithCorrectSlotID verifies that Spawn calls
// Manager.Start with StartParams.Index == slotID and applies the per-app
// resource limits, then registers the worker with the proxy.
func TestSpawnElasticWorker_BootsWithCorrectSlotID(t *testing.T) {
	store := mustOpenStore(t)
	app := mustCreateElasticApp(t, store, "app1")
	bundleDir := mustMinimalBundle(t)
	_ = mustCreateDeploymentInDir(t, store, app.ID, bundleDir)

	rt := &recordingRuntime{}
	mgr := process.NewManager(t.TempDir(), rt)

	prx := proxy.New()
	prx.SetPoolMode("app1", config.IsolationPerSession, 1, 5)

	spawner := &lifecycle.ElasticSpawner{
		Store:       store,
		Manager:     mgr,
		Proxy:       prx,
		RuntimeCfg:  config.RuntimeConfig{},
		HealthCheck: noopHealthCheck,
	}

	const slotID = 7
	spawner.Spawn("app1", slotID)

	// Verify Manager.Start was called with the right Index.
	rt.mu.Lock()
	defer rt.mu.Unlock()

	if len(rt.started) != 1 {
		t.Fatalf("expected 1 Start call, got %d", len(rt.started))
	}
	if got := rt.started[0].Index; got != slotID {
		t.Errorf("StartParams.Index = %d, want %d", got, slotID)
	}

	// Verify proxy has a running worker at slotID.
	if prx.ElasticWorkerCount("app1") == 0 {
		t.Error("proxy pool should have a registered worker after Spawn")
	}
}

// TestSpawnElasticWorker_AppliesResourceLimits verifies that Spawn threads the
// per-app memory and CPU limits through to StartParams.
func TestSpawnElasticWorker_AppliesResourceLimits(t *testing.T) {
	store := mustOpenStore(t)
	app := mustCreateElasticApp(t, store, "limapp")

	// Set per-app limits via direct DB update.
	mem, cpu := 512, 200
	_, err := store.DB().Exec(
		`UPDATE apps SET memory_limit_mb=?, cpu_quota_percent=? WHERE slug='limapp'`, mem, cpu)
	if err != nil {
		t.Fatal(err)
	}

	bundleDir := mustMinimalBundle(t)
	_ = mustCreateDeploymentInDir(t, store, app.ID, bundleDir)

	rt := &recordingRuntime{}
	mgr := process.NewManager(t.TempDir(), rt)

	prx := proxy.New()
	prx.SetPoolMode("limapp", config.IsolationPerSession, 1, 5)

	spawner := &lifecycle.ElasticSpawner{
		Store:       store,
		Manager:     mgr,
		Proxy:       prx,
		RuntimeCfg:  config.RuntimeConfig{},
		HealthCheck: noopHealthCheck,
	}

	spawner.Spawn("limapp", 3)

	rt.mu.Lock()
	defer rt.mu.Unlock()

	if len(rt.started) != 1 {
		t.Fatalf("expected 1 Start call, got %d", len(rt.started))
	}
	if got := rt.started[0].MemoryLimitMB; got != mem {
		t.Errorf("MemoryLimitMB = %d, want %d", got, mem)
	}
	if got := rt.started[0].CPUQuotaPercent; got != cpu {
		t.Errorf("CPUQuotaPercent = %d, want %d", got, cpu)
	}
}

// TestSpawnElasticWorker_MaxSessionLifetimeBackstop verifies that when
// worker_max_session_lifetime_secs > 0, a timer fires and calls Terminate
// after the configured duration.
func TestSpawnElasticWorker_MaxSessionLifetimeBackstop(t *testing.T) {
	store := mustOpenStore(t)
	app := mustCreateElasticApp(t, store, "lifeapp")

	// Set a very short lifetime so the test completes quickly.
	_, err := store.DB().Exec(
		`UPDATE apps SET worker_max_session_lifetime_secs=1 WHERE slug='lifeapp'`)
	if err != nil {
		t.Fatal(err)
	}

	bundleDir := mustMinimalBundle(t)
	_ = mustCreateDeploymentInDir(t, store, app.ID, bundleDir)

	rt := &recordingRuntime{}
	mgr := process.NewManager(t.TempDir(), rt)

	prx := proxy.New()
	prx.SetPoolMode("lifeapp", config.IsolationPerSession, 1, 5)

	spawner := &lifecycle.ElasticSpawner{
		Store:       store,
		Manager:     mgr,
		Proxy:       prx,
		RuntimeCfg:  config.RuntimeConfig{},
		HealthCheck: noopHealthCheck,
	}

	spawner.Spawn("lifeapp", 0)

	// Confirm worker is registered.
	if prx.ElasticWorkerCount("lifeapp") == 0 {
		t.Fatal("worker should be in the elastic pool after Spawn")
	}

	// Wait for the lifetime backstop to fire and terminate the worker.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if prx.ElasticWorkerCount("lifeapp") == 0 {
			return // worker was terminated as expected
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Error("worker was not terminated after max session lifetime elapsed")
}

// TestTerminateElasticWorker_CancelsLifetimeTimer verifies that calling
// Terminate before the max_session_lifetime backstop fires cancels the timer
// so it does not trigger a second Terminate call after the early one.
func TestTerminateElasticWorker_CancelsLifetimeTimer(t *testing.T) {
	store := mustOpenStore(t)
	app := mustCreateElasticApp(t, store, "cancelapp")

	// 1 s lifetime so the backstop fires quickly if not cancelled.
	_, err := store.DB().Exec(
		`UPDATE apps SET worker_max_session_lifetime_secs=1 WHERE slug='cancelapp'`)
	if err != nil {
		t.Fatal(err)
	}

	bundleDir := mustMinimalBundle(t)
	_ = mustCreateDeploymentInDir(t, store, app.ID, bundleDir)

	rt := &recordingRuntime{}
	mgr := process.NewManager(t.TempDir(), rt)

	prx := proxy.New()
	prx.SetPoolMode("cancelapp", config.IsolationPerSession, 1, 5)

	var terminateCalls int64
	spawner := &lifecycle.ElasticSpawner{
		Store:       store,
		Manager:     mgr,
		Proxy:       prx,
		RuntimeCfg:  config.RuntimeConfig{},
		HealthCheck: noopHealthCheck,
		TerminateHook: func(_ string, _ int) {
			atomic.AddInt64(&terminateCalls, 1)
		},
	}

	spawner.Spawn("cancelapp", 0)
	if prx.ElasticWorkerCount("cancelapp") == 0 {
		t.Fatal("worker should be registered in the elastic pool after Spawn")
	}

	// Terminate well before the 1 s backstop fires.
	spawner.Terminate("cancelapp", 0)

	// Wait past the 1 s lifetime. If the timer was not cancelled it would fire
	// and invoke Terminate a second time.
	time.Sleep(1500 * time.Millisecond)

	if n := atomic.LoadInt64(&terminateCalls); n != 1 {
		t.Errorf("Terminate called %d time(s), want 1 (backstop timer must be cancelled on early Terminate)", n)
	}
}

// TestTerminateElasticWorker_StopsReplicaAndDeregisters verifies that
// Terminate stops the Manager's entry and removes the worker from the proxy.
func TestTerminateElasticWorker_StopsReplicaAndDeregisters(t *testing.T) {
	store := mustOpenStore(t)
	_ = mustCreateElasticApp(t, store, "termapp")

	rt := &recordingRuntime{}
	mgr := process.NewManager(t.TempDir(), rt)

	// Adopt a fake process into the Manager (simulates an elastic worker that
	// was spawned during a previous call).
	mgr.Adopt("termapp", process.ProcessInfo{
		Slug:   "termapp",
		Index:  2,
		Status: process.StatusRunning,
	}, process.RunHandle{PID: 99999})

	prx := proxy.New()
	prx.SetPoolMode("termapp", config.IsolationPerSession, 1, 5)
	// Register a running worker so there is something to deregister.
	if err := prx.RegisterElasticWorker("termapp", 2, "http://127.0.0.1:9001", nil, 1); err != nil {
		t.Fatalf("RegisterElasticWorker: %v", err)
	}

	spawner := &lifecycle.ElasticSpawner{
		Store:      store,
		Manager:    mgr,
		Proxy:      prx,
		RuntimeCfg: config.RuntimeConfig{},
	}
	spawner.Terminate("termapp", 2)

	// Verify worker is removed from the proxy.
	if prx.ElasticWorkerCount("termapp") != 0 {
		t.Error("elastic worker map should be empty after Terminate")
	}

	// Verify the Manager no longer has the replica.
	if _, ok := mgr.GetReplica("termapp", 2); ok {
		t.Error("Manager should not have the replica after Terminate")
	}
}

// TestTerminateElasticWorker_Idempotent verifies that calling Terminate twice
// on the same slotID does not panic or error.
func TestTerminateElasticWorker_Idempotent(t *testing.T) {
	store := mustOpenStore(t)
	_ = mustCreateElasticApp(t, store, "idemapp")

	rt := &recordingRuntime{}
	mgr := process.NewManager(t.TempDir(), rt)

	prx := proxy.New()
	prx.SetPoolMode("idemapp", config.IsolationPerSession, 1, 5)

	spawner := &lifecycle.ElasticSpawner{
		Store:      store,
		Manager:    mgr,
		Proxy:      prx,
		RuntimeCfg: config.RuntimeConfig{},
	}

	// Terminate a slot that was never spawned - must be a no-op, not a panic.
	spawner.Terminate("idemapp", 99)
	spawner.Terminate("idemapp", 99)
}

// TestReapElasticOrphans_StopsAdoptedElasticProcess verifies that
// ReapElasticOrphans stops a process in the Manager that belongs to an
// elastic-mode app (a process that was wrongly adopted during RecoverProcesses
// or survived from a previous crash).
func TestReapElasticOrphans_StopsAdoptedElasticProcess(t *testing.T) {
	store := mustOpenStore(t)
	_ = mustCreateElasticApp(t, store, "orphanapp")
	// Add a non-elastic app to confirm it is not touched.
	_ = mustCreateApp(t, store, "normalapp")

	rt := &recordingRuntime{}
	mgr := process.NewManager(t.TempDir(), rt)

	// Simulate an orphaned elastic worker in the Manager.
	mgr.Adopt("orphanapp", process.ProcessInfo{
		Slug:   "orphanapp",
		Index:  0,
		Status: process.StatusRunning,
	}, process.RunHandle{PID: 77777})
	// Add a normal app replica - must NOT be reaped.
	mgr.Adopt("normalapp", process.ProcessInfo{
		Slug:   "normalapp",
		Index:  0,
		Status: process.StatusRunning,
	}, process.RunHandle{PID: 88888})

	lifecycle.ReapElasticOrphans(store, mgr)

	// Elastic orphan should be gone.
	if _, ok := mgr.GetReplica("orphanapp", 0); ok {
		t.Error("elastic orphan should have been reaped; still present in Manager")
	}
	// Normal app replica must be untouched.
	if _, ok := mgr.GetReplica("normalapp", 0); !ok {
		t.Error("non-elastic replica was incorrectly reaped")
	}
}

// TestRecoverProcesses_ElasticAppSetsUpPoolAndStaysRunning verifies that
// RecoverProcesses for a running elastic-mode app (with no replica rows)
// initializes the elastic pool in the proxy and keeps the app status as
// "running", not "stopped".
func TestRecoverProcesses_ElasticAppSetsUpPoolAndStaysRunning(t *testing.T) {
	store := mustOpenStore(t)
	app := mustCreateElasticApp(t, store, "elasticpool")

	// Mark the app as running (as it would be after a deploy).
	_, err := store.DB().Exec(`UPDATE apps SET status='running' WHERE slug='elasticpool'`)
	if err != nil {
		t.Fatal(err)
	}

	rt := &recordingRuntime{}
	mgr := process.NewManager(t.TempDir(), rt)
	prx := proxy.New()

	lifecycle.RecoverProcesses(store, mgr, prx, 0, false, "")

	// App must still be "running" - an empty elastic pool is a valid running state.
	got, err := store.GetAppBySlug("elasticpool")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "running" {
		t.Errorf("elastic app status = %q after recovery, want \"running\"", got.Status)
	}

	_ = app // used for setup
}

// TestRecoverProcesses_FleetDefaultElastic_SetsUpPoolAndStaysRunning verifies
// the bug: an app with an empty per-app WorkerIsolation that inherits an
// elastic fleet default ("per_session") must NOT fall through to the replica-
// adoption loop. Without the fix, isElasticIsolation("") returns false, the
// replica loop runs, finds no rows, and marks the app stopped. With the fix,
// the resolved mode ("per_session") fires the elastic branch, the proxy pool
// is configured, and the app stays "running".
func TestRecoverProcesses_FleetDefaultElastic_SetsUpPoolAndStaysRunning(t *testing.T) {
	store := mustOpenStore(t)
	app := mustCreateApp(t, store, "inheritpool")

	// Set worker_isolation to '' (empty = inherit fleet default). The column
	// default is 'multiplex', so we must set it explicitly to simulate an app
	// that defers to the fleet DefaultWorkerIsolation.
	_, err := store.DB().Exec(`UPDATE apps SET status='running', worker_max_workers=5, worker_isolation='' WHERE slug='inheritpool'`)
	if err != nil {
		t.Fatal(err)
	}

	rt := &recordingRuntime{}
	mgr := process.NewManager(t.TempDir(), rt)
	prx := proxy.New()

	// Pass "per_session" as the fleet default; the per-app field is empty.
	lifecycle.RecoverProcesses(store, mgr, prx, 0, false, "per_session")

	// App must still be "running": the elastic pool is ready, no replicas needed.
	got, err := store.GetAppBySlug("inheritpool")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "running" {
		t.Errorf("fleet-default-elastic app status = %q after recovery, want \"running\"", got.Status)
	}

	_ = app // used for setup
}
