package lifecycle_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"syscall"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/lifecycle"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
)

// liveListener opens a real loopback listener and returns its port. Native
// recovery now validates that the recorded port is actually serving before
// adopting a replica, so tests that exercise the "alive replica" path must
// have something listening — a bare PID is no longer sufficient.
func liveListener(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	return ln.Addr().(*net.TCPAddr).Port
}

// fakeDockerRuntime is a process.Runtime that also implements
// lifecycle.ContainerLister, so a Manager can be constructed with it (as the
// default tier) or have it registered to a tier. Recovery routes a replica to
// the container path when its tier's runtime is a ContainerLister.
type fakeDockerRuntime struct {
	containers []process.ContainerInfo
	pids       map[string]int // containerID → host PID
}

func (f *fakeDockerRuntime) Start(context.Context, process.StartParams, io.Writer) (process.ReplicaEndpoint, error) {
	return process.ReplicaEndpoint{}, nil
}
func (f *fakeDockerRuntime) Signal(process.RunHandle, syscall.Signal) error { return nil }
func (f *fakeDockerRuntime) Wait(context.Context, process.RunHandle) error  { return nil }
func (f *fakeDockerRuntime) Stats(context.Context, process.RunHandle) (float64, uint64, error) {
	return 0, 0, nil
}
func (f *fakeDockerRuntime) RunOnce(context.Context, process.StartParams, io.Writer) (process.ExitInfo, error) {
	return process.ExitInfo{}, nil
}
func (f *fakeDockerRuntime) HostPreparesDeps() bool { return false }
func (f *fakeDockerRuntime) AppBindHost() string    { return "0.0.0.0" }

func (f *fakeDockerRuntime) ListByLabel(_ string) ([]process.ContainerInfo, error) {
	return f.containers, nil
}

func (f *fakeDockerRuntime) InspectPID(id string) (int, error) {
	if pid, ok := f.pids[id]; ok {
		return pid, nil
	}
	return 0, fmt.Errorf("container %s not found", id)
}

// mustCreateApp creates a test app and returns it.
func mustCreateApp(t *testing.T, store *db.Store, slug string) *db.App {
	t.Helper()
	if err := store.CreateUser(db.CreateUserParams{Username: "u-" + slug, PasswordHash: "h", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	u, err := store.GetUserByUsername("u-" + slug)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateApp(db.CreateAppParams{Slug: slug, Name: slug, OwnerID: u.ID, Access: "private"}); err != nil {
		t.Fatal(err)
	}
	app, err := store.GetAppBySlug(slug)
	if err != nil {
		t.Fatal(err)
	}
	return app
}

// mustOpenStore creates an in-memory store with migrations applied.
func mustOpenStore(t *testing.T) *db.Store {
	t.Helper()
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestRecoverProcesses_DeadPID(t *testing.T) {
	store := mustOpenStore(t)
	app := mustCreateApp(t, store, "myapp")

	// Set up a replica with a dead PID.
	port, pid := 20001, 99999999
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: 0, PID: &pid, Port: &port, Status: "running",
	}); err != nil {
		t.Fatal(err)
	}
	store.DB().Exec(`UPDATE apps SET status='running', replicas=1 WHERE slug='myapp'`)

	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	prx := proxy.New()
	lifecycle.RecoverProcesses(store, mgr, prx, 0)

	// App should now be stopped in the DB.
	a, err := store.GetAppBySlug("myapp")
	if err != nil {
		t.Fatal(err)
	}
	if a.Status != "stopped" {
		t.Errorf("expected status=stopped after recovery of dead PID, got %s", a.Status)
	}
}

func TestRecoverProcesses_NoPID(t *testing.T) {
	store := mustOpenStore(t)
	mustCreateApp(t, store, "myapp")

	// Simulate status=running with no replicas (corrupted state).
	store.DB().Exec(`UPDATE apps SET status='running', replicas=1 WHERE slug='myapp'`)

	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	prx := proxy.New()
	lifecycle.RecoverProcesses(store, mgr, prx, 0) // must not panic


	a, err := store.GetAppBySlug("myapp")
	if err != nil {
		t.Fatal(err)
	}
	if a.Status != "stopped" {
		t.Errorf("expected stopped, got %s", a.Status)
	}
}

func TestRecoverProcesses_AlivePID(t *testing.T) {
	store := mustOpenStore(t)
	app := mustCreateApp(t, store, "myapp")

	port, pid := liveListener(t), os.Getpid() // alive PID + a real listener
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: 0, PID: &pid, Port: &port, Status: "running",
	}); err != nil {
		t.Fatal(err)
	}
	store.DB().Exec(`UPDATE apps SET status='running', replicas=1 WHERE slug='myapp'`)

	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	prx := proxy.New()
	lifecycle.RecoverProcesses(store, mgr, prx, 0)

	// App should still be running in the DB.
	a, err := store.GetAppBySlug("myapp")
	if err != nil {
		t.Fatal(err)
	}
	if a.Status != "running" {
		t.Errorf("expected status=running for alive PID, got %s", a.Status)
	}

	// Manager should have the replica entry.
	info, ok := mgr.GetReplica("myapp", 0)
	if !ok {
		t.Error("expected manager to have myapp replica 0 after recovery")
	} else if info.PID != pid {
		t.Errorf("expected PID %d in manager, got %d", pid, info.PID)
	}
}

func TestRecovery_PartialPool(t *testing.T) {
	store := mustOpenStore(t)
	app := mustCreateApp(t, store, "partial-pool")

	// Give app 2 replicas in DB.
	if _, err := store.DB().Exec(`UPDATE apps SET status='running', replicas=2 WHERE slug='partial-pool'`); err != nil {
		t.Fatal(err)
	}

	// Replica 0: alive (current process PID) with a real listener.
	pidAlive, port0 := os.Getpid(), liveListener(t)
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: 0, PID: &pidAlive, Port: &port0, Status: "running",
	}); err != nil {
		t.Fatal(err)
	}

	// Replica 1: dead PID.
	pidDead, port1 := 99999999, 20012
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: 1, PID: &pidDead, Port: &port1, Status: "running",
	}); err != nil {
		t.Fatal(err)
	}

	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	prx := proxy.New()
	lifecycle.RecoverProcesses(store, mgr, prx, 0)

	// Replica 0 adopted, replica 1 not.
	if _, ok := mgr.GetReplica("partial-pool", 0); !ok {
		t.Error("expected replica 0 adopted")
	}
	if _, ok := mgr.GetReplica("partial-pool", 1); ok {
		t.Error("expected replica 1 NOT adopted")
	}

	// App stays running (at least one replica alive).
	a, _ := store.GetAppBySlug("partial-pool")
	if a.Status != "running" {
		t.Errorf("expected app running, got %s", a.Status)
	}

	// Replica 1 marked crashed in the replica table.
	reps, err := store.ListReplicas(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	var rep1 *db.Replica
	for _, r := range reps {
		if r.Index == 1 {
			rep1 = r
			break
		}
	}
	if rep1 == nil || rep1.Status != "crashed" {
		t.Errorf("expected replica 1 status=crashed, got %+v", rep1)
	}
}

func TestRecoverDockerProcesses(t *testing.T) {
	store := mustOpenStore(t)
	prx := proxy.New()

	app := mustCreateApp(t, store, "docker-app")
	port := 20500
	pid := 99001
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: 0, PID: &pid, Port: &port, Status: "running",
	}); err != nil {
		t.Fatal(err)
	}
	store.DB().Exec(`UPDATE apps SET status='running', replicas=1 WHERE slug='docker-app'`)

	rt := &fakeDockerRuntime{
		containers: []process.ContainerInfo{
			{ID: "cont-abc", Labels: map[string]string{
				"shinyhub.slug":          "docker-app",
				"shinyhub.replica_index": "0",
			}},
		},
		pids: map[string]int{"cont-abc": 99001},
	}
	mgr := process.NewManager(t.TempDir(), rt)

	lifecycle.RecoverProcesses(store, mgr, prx, 0)

	info, ok := mgr.GetReplica("docker-app", 0)
	if !ok {
		t.Fatal("expected docker-app to be adopted after recovery")
	}
	if info.Port != port {
		t.Errorf("expected port %d, got %d", port, info.Port)
	}
	if info.PID != pid {
		t.Errorf("expected pid %d, got %d", pid, info.PID)
	}
}

func TestRecoverDockerProcesses_OrphanMarkedStopped(t *testing.T) {
	store := mustOpenStore(t)
	prx := proxy.New()

	if err := store.CreateUser(db.CreateUserParams{
		Username: "u2", PasswordHash: "x", Role: "developer",
	}); err != nil {
		t.Fatal(err)
	}
	user, _ := store.GetUserByUsername("u2")

	// Create two apps both marked as running in the DB.
	for _, slug := range []string{"alive-app", "orphan-app"} {
		if err := store.CreateApp(db.CreateAppParams{
			Slug: slug, Name: slug, OwnerID: user.ID,
		}); err != nil {
			t.Fatal(err)
		}
		a, _ := store.GetAppBySlug(slug)
		port := 20600
		pid := 99002
		if err := store.UpsertReplica(db.UpsertReplicaParams{
			AppID: a.ID, Index: 0, PID: &pid, Port: &port, Status: "running",
		}); err != nil {
			t.Fatal(err)
		}
		store.DB().Exec(`UPDATE apps SET status='running', replicas=1 WHERE slug=?`, slug)
	}

	// Only "alive-app" has a running container.
	rt := &fakeDockerRuntime{
		containers: []process.ContainerInfo{
			{ID: "cont-alive", Labels: map[string]string{
				"shinyhub.slug":          "alive-app",
				"shinyhub.replica_index": "0",
			}},
		},
		pids: map[string]int{"cont-alive": 99002},
	}
	mgr := process.NewManager(t.TempDir(), rt)

	lifecycle.RecoverProcesses(store, mgr, prx, 0)

	// "alive-app" should be adopted.
	if _, ok := mgr.GetReplica("alive-app", 0); !ok {
		t.Error("expected alive-app to be adopted")
	}

	// "orphan-app" should NOT be in the manager.
	if _, ok := mgr.GetReplica("orphan-app", 0); ok {
		t.Error("expected orphan-app to not be adopted (no container found)")
	}

	// "orphan-app" should be marked stopped in the DB.
	orphan, err := store.GetApp("orphan-app")
	if err != nil {
		t.Fatalf("GetApp: %v", err)
	}
	if orphan.Status != "stopped" {
		t.Errorf("expected orphan-app status=stopped, got %s", orphan.Status)
	}
}

func TestRecoverDockerProcesses_MultiReplica(t *testing.T) {
	store := mustOpenStore(t)
	prx := proxy.New()
	app := mustCreateApp(t, store, "multi-docker")

	// Two replicas in DB.
	port0, pid0, port1, pid1 := 20700, 99010, 20701, 99011
	if err := store.UpsertReplica(db.UpsertReplicaParams{AppID: app.ID, Index: 0, PID: &pid0, Port: &port0, Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertReplica(db.UpsertReplicaParams{AppID: app.ID, Index: 1, PID: &pid1, Port: &port1, Status: "running"}); err != nil {
		t.Fatal(err)
	}
	store.DB().Exec(`UPDATE apps SET status='running', replicas=2 WHERE slug='multi-docker'`)

	rt := &fakeDockerRuntime{
		containers: []process.ContainerInfo{
			{ID: "c0", Labels: map[string]string{"shinyhub.slug": "multi-docker", "shinyhub.replica_index": "0"}},
			{ID: "c1", Labels: map[string]string{"shinyhub.slug": "multi-docker", "shinyhub.replica_index": "1"}},
		},
		pids: map[string]int{"c0": pid0, "c1": pid1},
	}
	mgr := process.NewManager(t.TempDir(), rt)
	lifecycle.RecoverProcesses(store, mgr, prx, 0)

	if _, ok := mgr.GetReplica("multi-docker", 0); !ok {
		t.Error("want replica 0 adopted")
	}
	if _, ok := mgr.GetReplica("multi-docker", 1); !ok {
		t.Error("want replica 1 adopted")
	}
}

func TestRecovery_NilPIDMarkedCrashed(t *testing.T) {
	store := mustOpenStore(t)
	app := mustCreateApp(t, store, "nil-pid")

	port := 20099
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: 0, PID: nil, Port: &port, Status: "running",
	}); err != nil {
		t.Fatal(err)
	}
	store.DB().Exec(`UPDATE apps SET status='running', replicas=1 WHERE slug='nil-pid'`)

	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	prx := proxy.New()
	lifecycle.RecoverProcesses(store, mgr, prx, 0)

	reps, _ := store.ListReplicas(app.ID)
	if len(reps) != 1 || reps[0].Status != "crashed" {
		t.Fatalf("expected replica 0 status=crashed after nil-PID recovery, got %+v", reps)
	}
}

func TestRecoverDockerProcesses_IdxBeyondPool(t *testing.T) {
	store := mustOpenStore(t)
	prx := proxy.New()
	app := mustCreateApp(t, store, "shrunk-docker")

	// The app was scaled down to 1 replica, but a stale replica row and its
	// container at index 1 survive from before the scale-down. Both indexes
	// still have a live container; recovery must adopt idx 0 (within the pool)
	// and skip idx 1 (r.Index >= app.Replicas).
	port0, pid0 := 20800, 99020
	port1, pid1 := 20801, 99021
	if err := store.UpsertReplica(db.UpsertReplicaParams{AppID: app.ID, Index: 0, PID: &pid0, Port: &port0, Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertReplica(db.UpsertReplicaParams{AppID: app.ID, Index: 1, PID: &pid1, Port: &port1, Status: "running"}); err != nil {
		t.Fatal(err)
	}
	store.DB().Exec(`UPDATE apps SET status='running', replicas=1 WHERE slug='shrunk-docker'`)

	rt := &fakeDockerRuntime{
		containers: []process.ContainerInfo{
			{ID: "c0", Labels: map[string]string{"shinyhub.slug": "shrunk-docker", "shinyhub.replica_index": "0"}},
			{ID: "c1", Labels: map[string]string{"shinyhub.slug": "shrunk-docker", "shinyhub.replica_index": "1"}},
		},
		pids: map[string]int{"c0": pid0, "c1": pid1},
	}
	mgr := process.NewManager(t.TempDir(), rt)
	lifecycle.RecoverProcesses(store, mgr, prx, 0)

	// idx 0 is within the pool of 1 → adopted.
	if _, ok := mgr.GetReplica("shrunk-docker", 0); !ok {
		t.Error("expected in-pool replica 0 to be adopted")
	}
	// idx 1 is beyond the pool of 1 → skipped despite a live container.
	if _, ok := mgr.GetReplica("shrunk-docker", 1); ok {
		t.Error("expected out-of-pool replica 1 to be skipped")
	}
	// One replica adopted, so the app stays running.
	a, _ := store.GetAppBySlug("shrunk-docker")
	if a.Status != "running" {
		t.Errorf("expected running, got %s", a.Status)
	}
}

// TestRecoverProcesses_MixedTier exercises an app whose replicas span a native
// default tier and a container-backed burst tier. Recovery must route each
// replica to its tier's runtime: the native replica through the PID path and
// the burst replica through the container path, adopting both.
func TestRecoverProcesses_MixedTier(t *testing.T) {
	store := mustOpenStore(t)
	prx := proxy.New()
	app := mustCreateApp(t, store, "mixed-tier")

	// Replica 0 on the native default tier: alive PID + a real listener.
	pidNative, portNative := os.Getpid(), liveListener(t)
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: 0, PID: &pidNative, Port: &portNative,
		Status: "running", Provider: "native", Tier: "local",
	}); err != nil {
		t.Fatal(err)
	}
	// Replica 1 on the container-backed "burst" tier.
	pidBurst, portBurst := 99030, 20900
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: 1, PID: &pidBurst, Port: &portBurst,
		Status: "running", Provider: "docker", Tier: "burst",
	}); err != nil {
		t.Fatal(err)
	}
	store.DB().Exec(`UPDATE apps SET status='running', replicas=2 WHERE slug='mixed-tier'`)

	burst := &fakeDockerRuntime{
		containers: []process.ContainerInfo{
			{ID: "cb1", Labels: map[string]string{"shinyhub.slug": "mixed-tier", "shinyhub.replica_index": "1"}},
		},
		pids: map[string]int{"cb1": pidBurst},
	}
	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	mgr.RegisterRuntime("burst", burst)
	lifecycle.RecoverProcesses(store, mgr, prx, 0)

	if info, ok := mgr.GetReplica("mixed-tier", 0); !ok {
		t.Error("expected native replica 0 to be adopted")
	} else if info.PID != pidNative {
		t.Errorf("native replica: expected pid %d, got %d", pidNative, info.PID)
	}
	if info, ok := mgr.GetReplica("mixed-tier", 1); !ok {
		t.Error("expected burst replica 1 to be adopted")
	} else if info.PID != pidBurst {
		t.Errorf("burst replica: expected pid %d, got %d", pidBurst, info.PID)
	}

	a, _ := store.GetAppBySlug("mixed-tier")
	if a.Status != "running" {
		t.Errorf("expected running, got %s", a.Status)
	}
}

// TestRecoveryRegistersPersistedEndpointURL verifies that when a replica row
// carries a non-empty endpoint_url, recovery registers that exact URL with the
// proxy rather than constructing a fresh localhost URL from the port alone.
func TestRecoveryRegistersPersistedEndpointURL(t *testing.T) {
	store := mustOpenStore(t)
	app := mustCreateApp(t, store, "rec-endpoint")

	pid := os.Getpid()
	port := liveListener(t) // real listener so validateNativeProcess passes
	endpoint := fmt.Sprintf("http://worker-host.internal:%d", port)

	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID:        app.ID,
		Index:        0,
		PID:          &pid,
		Port:         &port,
		Status:       "running",
		Provider:     "native",
		Tier:         "local",
		EndpointURL:  endpoint,
		WorkerID:     strconv.Itoa(pid),
		DesiredState: "running",
	}); err != nil {
		t.Fatalf("seed replica: %v", err)
	}
	store.DB().Exec(`UPDATE apps SET status='running', replicas=1 WHERE slug='rec-endpoint'`)

	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	prx := proxy.New()

	lifecycle.RecoverProcesses(store, mgr, prx, 0)

	if got := prx.ReplicaTargetURL("rec-endpoint", 0); got != endpoint {
		t.Fatalf("recovered replica registered %q; want stored endpoint %q", got, endpoint)
	}
}

// TestReconcileInflightDeployments verifies a deploy interrupted before
// promotion is failed at startup so the previous good deployment remains the
// authoritative live bundle.
func TestReconcileInflightDeployments(t *testing.T) {
	store := mustOpenStore(t)
	app := mustCreateApp(t, store, "app")

	if _, err := store.CreateDeployment(db.CreateDeploymentParams{
		AppID: app.ID, Version: "v1", BundleDir: "/b/v1",
	}); err != nil {
		t.Fatalf("seed v1: %v", err)
	}
	// Simulate a server crash mid-deploy: a pending row was written but never
	// promoted.
	if _, err := store.BeginDeployment(app.ID, "v2", "/b/v2"); err != nil {
		t.Fatalf("BeginDeployment: %v", err)
	}

	lifecycle.ReconcileInflightDeployments(store)

	if in, err := store.ListInflightDeployments(); err != nil || len(in) != 0 {
		t.Fatalf("after reconcile, inflight = %+v err=%v, want none", in, err)
	}
	live, err := store.ListDeployments(app.ID)
	if err != nil {
		t.Fatalf("ListDeployments: %v", err)
	}
	if len(live) != 1 || live[0].Version != "v1" {
		t.Fatalf("after reconcile, live = %+v, want only v1", live)
	}
}
