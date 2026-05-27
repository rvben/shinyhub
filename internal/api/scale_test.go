package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
)

// stopFailRuntime is a minimal Runtime whose Start succeeds (registering a
// manager entry) but whose Signal always fails, so Manager.StopReplica returns a
// non-benign error rather than ErrReplicaNotFound. It lets the scale-down abort
// path be exercised against the real Manager.StopReplica code.
type stopFailRuntime struct{ nextPID int }

func (r *stopFailRuntime) Start(_ context.Context, p process.StartParams, _ io.Writer) (process.ReplicaEndpoint, error) {
	r.nextPID++
	pid := 60000 + r.nextPID
	return process.ReplicaEndpoint{
		URL:      fmt.Sprintf("http://127.0.0.1:%d", p.Port),
		Provider: "native",
		WorkerID: fmt.Sprintf("%d", pid),
		Handle:   process.RunHandle{PID: pid},
	}, nil
}

func (r *stopFailRuntime) Signal(process.RunHandle, syscall.Signal) error {
	return errors.New("worker refused SIGTERM")
}
func (r *stopFailRuntime) Wait(context.Context, process.RunHandle) error { return nil }
func (r *stopFailRuntime) Stats(context.Context, process.RunHandle) (float64, uint64, error) {
	return 0, 0, nil
}
func (r *stopFailRuntime) RunOnce(context.Context, process.StartParams, io.Writer) (process.ExitInfo, error) {
	return process.ExitInfo{}, nil
}
func (r *stopFailRuntime) HostPreparesDeps() bool    { return false }
func (r *stopFailRuntime) AppBindHost() string       { return "127.0.0.1" }
func (r *stopFailRuntime) HostProvidesAppData() bool { return false }

// newScaleTestServer seeds an in-memory store with one admin user, a running
// app at the given replica count, a promoted deployment, and one running
// replica row per index. It returns the server and the app. The server is
// wired with a real native process manager and proxy so scale operations
// exercise the production stop/route paths.
func newScaleTestServer(t *testing.T, slug string, replicas int, cfg *config.Config) (*Server, *db.App) {
	t.Helper()
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	hash, _ := auth.HashPassword("pass")
	if err := store.CreateUser(db.CreateUserParams{Username: "bob", PasswordHash: hash, Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateApp(db.CreateAppParams{Slug: slug, Name: slug, OwnerID: 1}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: slug, Status: "running"}); err != nil {
		t.Fatal(err)
	}
	app, err := store.GetAppBySlug(slug)
	if err != nil {
		t.Fatal(err)
	}
	dep, err := store.BeginDeployment(app.ID, "v1", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PromoteDeployment(dep.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateAppReplicas(app.ID, replicas); err != nil {
		t.Fatal(err)
	}
	depID := dep.ID
	for i := 0; i < replicas; i++ {
		pid, port := 1000+i, 9000+i
		if err := store.UpsertReplica(db.UpsertReplicaParams{
			AppID:        app.ID,
			Index:        i,
			PID:          &pid,
			Port:         &port,
			Status:       "running",
			Provider:     "native",
			Tier:         "default",
			AppVersion:   "v1",
			DesiredState: "running",
			DeploymentID: &depID,
		}); err != nil {
			t.Fatal(err)
		}
	}
	app, err = store.GetAppBySlug(slug)
	if err != nil {
		t.Fatal(err)
	}
	srv := New(cfg, store, process.NewManager(t.TempDir(), process.NewNativeRuntime()), proxy.New())
	return srv, app
}

// TestScaleUp_BootsTrailingIndexAndGrowsPool proves ScaleUp boots only the new
// trailing index (not a full pool cycle), grows the proxy pool by one, persists
// the new replica row, and bumps the app's replica count.
func TestScaleUp_BootsTrailingIndexAndGrowsPool(t *testing.T) {
	cfg := &config.Config{}
	cfg.Runtime.MaxReplicas = 8
	srv, app := newScaleTestServer(t, "demo", 2, cfg)

	var bootedIndex = -1
	srv.deployReplica = func(p deploy.Params, index int) (*deploy.Result, error) {
		bootedIndex = index
		return &deploy.Result{
			Index:       index,
			PID:         4242,
			Port:        9100 + index,
			Provider:    "native",
			Tier:        "default",
			EndpointURL: "http://127.0.0.1:9100",
		}, nil
	}
	srv.proxy.SetPoolSize("demo", 2)

	scaled, err := srv.ScaleUp("demo")
	if err != nil {
		t.Fatalf("ScaleUp: %v", err)
	}
	if !scaled {
		t.Fatal("ScaleUp reported no change for an app below the ceiling")
	}
	if bootedIndex != 2 {
		t.Errorf("ScaleUp booted index %d; want the new trailing index 2", bootedIndex)
	}
	if got := srv.proxy.ReplicaSessionCounts("demo"); len(got) != 3 {
		t.Errorf("proxy pool grew to %d slots; want 3", len(got))
	}
	got, err := srv.store.GetAppBySlug("demo")
	if err != nil {
		t.Fatal(err)
	}
	if got.Replicas != 3 {
		t.Errorf("app replica count = %d; want 3", got.Replicas)
	}
	reps, err := srv.store.ListReplicas(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, r := range reps {
		if r.Index == 2 && r.Status == "running" {
			found = true
		}
	}
	if !found {
		t.Errorf("new replica index 2 not persisted as running; rows=%+v", reps)
	}
}

// TestScaleUp_TierPlaced_KeepsPlacementInSync proves that for an app using
// explicit tier placement, ScaleUp grows the tier owning the highest index and
// persists the updated placement, so the booted index maps to a real tier and
// the stored placement still sums to the replica count. Without this the new
// index falls back to the default tier and replica_placement desyncs from
// apps.replicas.
func TestScaleUp_TierPlaced_KeepsPlacementInSync(t *testing.T) {
	cfg := &config.Config{}
	cfg.Runtime.MaxReplicas = 8
	cfg.Runtime.Tiers = []config.TierConfig{
		{Name: "local", Runtime: "native"},
		{Name: "burst", Runtime: "native"},
	}
	srv, app := newScaleTestServer(t, "demo", 2, cfg)
	if err := srv.store.SetAppPlacement(app.ID, `{"burst":2}`, 2); err != nil {
		t.Fatal(err)
	}

	var gotPlacement map[string]int
	bootedIndex := -1
	srv.deployReplica = func(p deploy.Params, index int) (*deploy.Result, error) {
		bootedIndex = index
		gotPlacement = p.Placement
		return &deploy.Result{Index: index, PID: 1, Port: 9100 + index, Provider: "native", Tier: "burst", EndpointURL: "http://127.0.0.1:9100"}, nil
	}
	srv.proxy.SetPoolSize("demo", 2)

	scaled, err := srv.ScaleUp("demo")
	if err != nil {
		t.Fatalf("ScaleUp: %v", err)
	}
	if !scaled {
		t.Fatal("ScaleUp reported no change for a tier-placed app below the ceiling")
	}
	if bootedIndex != 2 {
		t.Errorf("ScaleUp booted index %d; want 2", bootedIndex)
	}
	// The boot must see burst=3 so global index 2 maps to the burst tier rather
	// than falling back to the default tier.
	if gotPlacement["burst"] != 3 {
		t.Errorf("boot placement burst=%d; want 3 so index 2 lands on burst", gotPlacement["burst"])
	}
	got, _ := srv.store.GetAppBySlug("demo")
	if got.Replicas != 3 {
		t.Errorf("app replica count = %d; want 3", got.Replicas)
	}
	if pm := got.PlacementMap(); pm["burst"] != 3 {
		t.Errorf("stored placement burst=%d; want 3 (in sync with replica count)", pm["burst"])
	}
}

// TestScaleDown_TierPlaced_KeepsPlacementInSync proves that for a tier-placed
// app, ScaleDown shrinks the tier owning the highest index and persists the
// updated placement, so a later full deploy does not recreate the replica that
// autoscale just removed.
func TestScaleDown_TierPlaced_KeepsPlacementInSync(t *testing.T) {
	cfg := &config.Config{}
	cfg.Runtime.MaxReplicas = 8
	cfg.Runtime.Tiers = []config.TierConfig{
		{Name: "local", Runtime: "native"},
		{Name: "burst", Runtime: "native"},
	}
	srv, app := newScaleTestServer(t, "demo", 2, cfg)
	if err := srv.store.SetAppPlacement(app.ID, `{"burst":2}`, 2); err != nil {
		t.Fatal(err)
	}
	info, err := srv.manager.Start(process.StartParams{
		Slug: "demo", Index: 1, Tier: "burst", Dir: t.TempDir(),
		Command: []string{"sleep", "30"}, Port: 19600,
	})
	if err != nil {
		t.Fatalf("seed victim process: %v", err)
	}
	srv.proxy.SetPoolSize("demo", 2)

	scaled, err := srv.ScaleDown("demo", 500*time.Millisecond)
	if err != nil {
		t.Fatalf("ScaleDown: %v", err)
	}
	if !scaled {
		t.Fatal("ScaleDown reported no change for a 2-replica tier-placed app")
	}
	if err := syscall.Kill(info.PID, 0); err == nil {
		t.Errorf("victim process (pid %d) still alive after ScaleDown", info.PID)
	}
	got, _ := srv.store.GetAppBySlug("demo")
	if got.Replicas != 1 {
		t.Errorf("app replica count = %d; want 1", got.Replicas)
	}
	if pm := got.PlacementMap(); pm["burst"] != 1 {
		t.Errorf("stored placement burst=%d; want 1 (in sync with replica count)", pm["burst"])
	}
}

// TestScaleUp_RefusesAboveMaxReplicas proves ScaleUp is a benign no-op when the
// app is already at the runtime max-replicas ceiling: no boot, no count change.
func TestScaleUp_RefusesAboveMaxReplicas(t *testing.T) {
	cfg := &config.Config{}
	cfg.Runtime.MaxReplicas = 2
	srv, _ := newScaleTestServer(t, "demo", 2, cfg)

	called := false
	srv.deployReplica = func(p deploy.Params, index int) (*deploy.Result, error) {
		called = true
		return &deploy.Result{Index: index}, nil
	}

	scaled, err := srv.ScaleUp("demo")
	if err != nil {
		t.Fatalf("ScaleUp: %v", err)
	}
	if scaled {
		t.Error("ScaleUp grew the pool past the max-replicas ceiling")
	}
	if called {
		t.Error("ScaleUp booted a replica despite being at the ceiling")
	}
	got, _ := srv.store.GetAppBySlug("demo")
	if got.Replicas != 2 {
		t.Errorf("replica count changed to %d at the ceiling; want 2", got.Replicas)
	}
}

// TestScaleDown_DrainsAndRemovesHighestReplica proves ScaleDown drains and
// stops the highest-index replica, shrinks the proxy pool, deletes the replica
// row, and decrements the app's replica count. The victim is a real native
// process so the stop is exercised end to end.
func TestScaleDown_DrainsAndRemovesHighestReplica(t *testing.T) {
	cfg := &config.Config{}
	cfg.Runtime.MaxReplicas = 8
	srv, app := newScaleTestServer(t, "demo", 2, cfg)

	// Start a real process at the victim index (1) so StopReplica has something
	// to terminate, and size the proxy pool to match.
	info, err := srv.manager.Start(process.StartParams{
		Slug:    "demo",
		Index:   1,
		Dir:     t.TempDir(),
		Command: []string{"sleep", "30"},
		Port:    19500,
	})
	if err != nil {
		t.Fatalf("seed victim process: %v", err)
	}
	srv.proxy.SetPoolSize("demo", 2)

	scaled, err := srv.ScaleDown("demo", 500*time.Millisecond)
	if err != nil {
		t.Fatalf("ScaleDown: %v", err)
	}
	if !scaled {
		t.Fatal("ScaleDown reported no change for a 2-replica app")
	}
	if err := syscall.Kill(info.PID, 0); err == nil {
		t.Errorf("victim process (pid %d) still alive after ScaleDown", info.PID)
	}
	if got := srv.proxy.ReplicaSessionCounts("demo"); len(got) != 1 {
		t.Errorf("proxy pool shrank to %d slots; want 1", len(got))
	}
	got, _ := srv.store.GetAppBySlug("demo")
	if got.Replicas != 1 {
		t.Errorf("app replica count = %d; want 1", got.Replicas)
	}
	reps, err := srv.store.ListReplicas(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range reps {
		if r.Index == 1 {
			t.Errorf("replica row index 1 not deleted after ScaleDown; rows=%+v", reps)
		}
	}
}

// TestScaleDown_StopFailureKeepsStateIntact proves that when stopping the victim
// replica fails for a reason other than a benign already-gone entry (e.g. a
// remote worker rejects the SIGTERM), ScaleDown aborts and leaves all state
// intact: it returns an error, does not shrink the proxy pool, does not delete
// the replica row, does not decrement the count, and clears the drain flag it
// optimistically set so the still-running replica resumes full service. Without
// this guard a stop failure would orphan a running replica while the control
// plane believes capacity was removed.
func TestScaleDown_StopFailureKeepsStateIntact(t *testing.T) {
	cfg := &config.Config{}
	cfg.Runtime.MaxReplicas = 8
	srv, app := newScaleTestServer(t, "demo", 2, cfg)

	// Register a runtime that fails to stop, and start the victim (index 1) on
	// its tier so Manager.StopReplica dispatches to it and returns a real error.
	srv.manager.RegisterRuntime("failstop", &stopFailRuntime{})
	if _, err := srv.manager.Start(process.StartParams{
		Slug: "demo", Index: 1, Tier: "failstop", Dir: t.TempDir(),
		Command: []string{"sleep", "30"}, Port: 19700,
	}); err != nil {
		t.Fatalf("seed victim process: %v", err)
	}

	// Give the proxy a live backend at the victim slot so the drain mark and its
	// rollback are observable.
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer backend.Close()
	srv.proxy.SetPoolSize("demo", 2)
	if err := srv.proxy.RegisterReplica("demo", 1, backend.URL, nil); err != nil {
		t.Fatalf("register victim backend: %v", err)
	}

	scaled, err := srv.ScaleDown("demo", 100*time.Millisecond)
	if err == nil {
		t.Fatal("ScaleDown returned nil error despite the stop failing")
	}
	if scaled {
		t.Error("ScaleDown reported success despite the stop failing")
	}
	if errors.Is(err, process.ErrReplicaNotFound) {
		t.Errorf("stop failure surfaced as ErrReplicaNotFound (benign); want a real error: %v", err)
	}
	// Proxy pool must not shrink: the replica is still running.
	if got := srv.proxy.ReplicaSessionCounts("demo"); len(got) != 2 {
		t.Errorf("proxy pool shrank to %d slots after a failed stop; want 2 untouched", len(got))
	}
	// Drain flag must be cleared so the surviving replica is back in rotation.
	if srv.proxy.IsDraining("demo", 1) {
		t.Error("victim slot left draining after the scale-down aborted")
	}
	// Replica row must survive and the count stay at 2.
	got, _ := srv.store.GetAppBySlug("demo")
	if got.Replicas != 2 {
		t.Errorf("app replica count = %d after a failed stop; want 2 untouched", got.Replicas)
	}
	reps, err := srv.store.ListReplicas(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	var foundVictim bool
	for _, r := range reps {
		if r.Index == 1 {
			foundVictim = true
		}
	}
	if !foundVictim {
		t.Errorf("replica row index 1 deleted after a failed stop; rows=%+v", reps)
	}
}

// TestScaleDown_ForceStopsAfterGraceWithActiveSessions proves the drain is
// deadline-bounded: when a sticky session never finishes, ScaleDown still
// completes after the grace period rather than blocking forever. This is the
// guarantee that keeps the autoscale controller from stalling under sustained
// long-lived sessions.
func TestScaleDown_ForceStopsAfterGraceWithActiveSessions(t *testing.T) {
	cfg := &config.Config{}
	cfg.Runtime.MaxReplicas = 8
	srv, _ := newScaleTestServer(t, "demo", 2, cfg)

	// A backend that blocks until released, holding one active connection open
	// on the victim slot so the drain can never reach zero on its own.
	release := make(chan struct{})
	var once sync.Once
	releaseOnce := func() { once.Do(func() { close(release) }) }
	defer releaseOnce()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer backend.Close()

	srv.proxy.SetPoolSize("demo", 2)
	if err := srv.proxy.RegisterReplica("demo", 1, backend.URL, nil); err != nil {
		t.Fatalf("register victim backend: %v", err)
	}

	// Pin a sticky request to the victim so its activeConns stays at 1.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		req := httptest.NewRequest(http.MethodGet, "/app/demo/", nil)
		req.AddCookie(&http.Cookie{Name: "shinyhub_rep_demo", Value: "1"})
		srv.proxy.ServeHTTP(httptest.NewRecorder(), req)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		c := srv.proxy.ReplicaSessionCounts("demo")
		if len(c) == 2 && c[1] >= 1 {
			break
		}
		if !time.Now().Before(deadline) {
			releaseOnce()
			wg.Wait()
			t.Fatalf("victim slot never registered an active session: %v", c)
		}
		time.Sleep(5 * time.Millisecond)
	}

	grace := 150 * time.Millisecond
	start := time.Now()
	scaled, err := srv.ScaleDown("demo", grace)
	elapsed := time.Since(start)
	if err != nil {
		releaseOnce()
		wg.Wait()
		t.Fatalf("ScaleDown: %v", err)
	}
	if !scaled {
		releaseOnce()
		wg.Wait()
		t.Fatal("ScaleDown reported no change despite a 2-replica app")
	}
	if elapsed < grace {
		t.Errorf("ScaleDown returned in %s, before the %s grace elapsed", elapsed, grace)
	}
	if elapsed > grace+2*time.Second {
		t.Errorf("ScaleDown took %s; it must be bounded near the %s grace, not block on the stuck session", elapsed, grace)
	}
	got, _ := srv.store.GetAppBySlug("demo")
	if got.Replicas != 1 {
		t.Errorf("app replica count = %d after force scale-down; want 1", got.Replicas)
	}

	releaseOnce()
	wg.Wait()
}

// TestScaleDown_SkipsNonRunningApp proves ScaleDown honours a concurrent
// stop/delete: it must not mutate the DB or fabricate a proxy pool for an app
// the operator has torn down.
func TestScaleDown_SkipsNonRunningApp(t *testing.T) {
	cfg := &config.Config{}
	cfg.Runtime.MaxReplicas = 8
	srv, _ := newScaleTestServer(t, "demo", 2, cfg)
	if err := srv.store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: "demo", Status: "stopped"}); err != nil {
		t.Fatal(err)
	}

	scaled, err := srv.ScaleDown("demo", 100*time.Millisecond)
	if err != nil {
		t.Fatalf("ScaleDown: %v", err)
	}
	if scaled {
		t.Error("ScaleDown acted on a stopped app")
	}
	got, _ := srv.store.GetAppBySlug("demo")
	if got.Replicas != 2 {
		t.Errorf("replica count changed to %d for a stopped app; want 2 untouched", got.Replicas)
	}
	if srv.proxy.HasLiveReplica("demo") {
		t.Error("ScaleDown fabricated a proxy pool for a stopped app")
	}
}

// TestScaleDown_RefusesBelowOne proves ScaleDown will not drop the last replica:
// a single-replica app is left untouched.
func TestScaleDown_RefusesBelowOne(t *testing.T) {
	cfg := &config.Config{}
	cfg.Runtime.MaxReplicas = 8
	srv, _ := newScaleTestServer(t, "demo", 1, cfg)

	scaled, err := srv.ScaleDown("demo", 100*time.Millisecond)
	if err != nil {
		t.Fatalf("ScaleDown: %v", err)
	}
	if scaled {
		t.Error("ScaleDown removed the last replica; the floor is 1")
	}
	got, _ := srv.store.GetAppBySlug("demo")
	if got.Replicas != 1 {
		t.Errorf("replica count changed to %d at the floor; want 1", got.Replicas)
	}
}
