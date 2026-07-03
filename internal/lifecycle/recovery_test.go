package lifecycle_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
	"github.com/rvben/shinyhub/internal/lifecycle"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
	"github.com/rvben/shinyhub/internal/worker"
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
	removed    []string       // container IDs passed to RemoveContainer
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
func (f *fakeDockerRuntime) HostPreparesDeps() bool    { return false }
func (f *fakeDockerRuntime) AppBindHost() string       { return "0.0.0.0" }
func (f *fakeDockerRuntime) HostProvidesAppData() bool { return true }

func (f *fakeDockerRuntime) ListByLabel(_ string) ([]process.ContainerInfo, error) {
	return f.containers, nil
}

func (f *fakeDockerRuntime) InspectPID(id string) (int, error) {
	if pid, ok := f.pids[id]; ok {
		return pid, nil
	}
	return 0, fmt.Errorf("container %s not found", id)
}

func (f *fakeDockerRuntime) RemoveContainer(id string) error {
	f.removed = append(f.removed, id)
	return nil
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
	return dbtest.New(t)
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
	lifecycle.RecoverProcesses(store, mgr, prx, 0, false, "")

	// A running app whose process did not survive the restart is marked
	// "hibernated" (not "stopped") so the warm-restore pass re-boots it - it
	// survives the restart instead of being stranded down.
	a, err := store.GetAppBySlug("myapp")
	if err != nil {
		t.Fatal(err)
	}
	if a.Status != "hibernated" {
		t.Errorf("expected status=hibernated after recovery of dead PID, got %s", a.Status)
	}
}

func TestRecoverProcesses_NoPID(t *testing.T) {
	store := mustOpenStore(t)
	mustCreateApp(t, store, "myapp")

	// Simulate status=running with no replicas (corrupted state).
	store.DB().Exec(`UPDATE apps SET status='running', replicas=1 WHERE slug='myapp'`)

	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	prx := proxy.New()
	lifecycle.RecoverProcesses(store, mgr, prx, 0, false, "") // must not panic

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
	lifecycle.RecoverProcesses(store, mgr, prx, 0, false, "")

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
	lifecycle.RecoverProcesses(store, mgr, prx, 0, false, "")

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
				process.LabelSlug:         "docker-app",
				process.LabelReplicaIndex: "0",
			}},
		},
		pids: map[string]int{"cont-abc": 99001},
	}
	mgr := process.NewManager(t.TempDir(), rt)

	lifecycle.RecoverProcesses(store, mgr, prx, 0, false, "")

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

// TestRecoverProcesses_ReapsFrozenWarmContainer: a suspended/warm container that
// survived a restart is force-removed (it cannot be re-adopted warm) and its row
// downgraded to stopped/warm so a later expansion cold-boots a fresh container.
func TestRecoverProcesses_ReapsFrozenWarmContainer(t *testing.T) {
	store := mustOpenStore(t)
	prx := proxy.New()

	app := mustCreateApp(t, store, "docker-app")
	port := 20500
	pid := 99001
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: 0, PID: &pid, Port: &port, Status: "suspended",
		DesiredState: db.ReplicaDesiredWarm,
	}); err != nil {
		t.Fatal(err)
	}
	store.DB().Exec(`UPDATE apps SET status='running', replicas=1 WHERE slug='docker-app'`)

	rt := &fakeDockerRuntime{
		containers: []process.ContainerInfo{
			{ID: "cont-abc", Labels: map[string]string{
				process.LabelSlug:         "docker-app",
				process.LabelReplicaIndex: "0",
			}},
		},
		pids: map[string]int{"cont-abc": 99001},
	}
	mgr := process.NewManager(t.TempDir(), rt)

	lifecycle.RecoverProcesses(store, mgr, prx, 0, false, "")

	if len(rt.removed) != 1 || rt.removed[0] != "cont-abc" {
		t.Fatalf("removed = %v, want [cont-abc] (paused container reaped)", rt.removed)
	}
	reps, err := store.ListReplicas(app.ID)
	if err != nil || len(reps) != 1 {
		t.Fatalf("ListReplicas = %v, %v", reps, err)
	}
	if reps[0].Status != "stopped" || reps[0].DesiredState != db.ReplicaDesiredWarm {
		t.Fatalf("row = %s/%s, want stopped/%s", reps[0].Status, reps[0].DesiredState, db.ReplicaDesiredWarm)
	}
	if _, ok := mgr.GetReplica("docker-app", 0); ok {
		t.Error("frozen warm container must not be adopted as running")
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
				process.LabelSlug:         "alive-app",
				process.LabelReplicaIndex: "0",
			}},
		},
		pids: map[string]int{"cont-alive": 99002},
	}
	mgr := process.NewManager(t.TempDir(), rt)

	lifecycle.RecoverProcesses(store, mgr, prx, 0, false, "")

	// "alive-app" should be adopted.
	if _, ok := mgr.GetReplica("alive-app", 0); !ok {
		t.Error("expected alive-app to be adopted")
	}

	// "orphan-app" should NOT be in the manager.
	if _, ok := mgr.GetReplica("orphan-app", 0); ok {
		t.Error("expected orphan-app to not be adopted (no container found)")
	}

	// "orphan-app" (its container vanished) is marked "hibernated" so the
	// warm-restore pass re-boots it, rather than being stranded "stopped".
	orphan, err := store.GetApp("orphan-app")
	if err != nil {
		t.Fatalf("GetApp: %v", err)
	}
	if orphan.Status != "hibernated" {
		t.Errorf("expected orphan-app status=hibernated, got %s", orphan.Status)
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
			{ID: "c0", Labels: map[string]string{process.LabelSlug: "multi-docker", process.LabelReplicaIndex: "0"}},
			{ID: "c1", Labels: map[string]string{process.LabelSlug: "multi-docker", process.LabelReplicaIndex: "1"}},
		},
		pids: map[string]int{"c0": pid0, "c1": pid1},
	}
	mgr := process.NewManager(t.TempDir(), rt)
	lifecycle.RecoverProcesses(store, mgr, prx, 0, false, "")

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
	lifecycle.RecoverProcesses(store, mgr, prx, 0, false, "")

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
			{ID: "c0", Labels: map[string]string{process.LabelSlug: "shrunk-docker", process.LabelReplicaIndex: "0"}},
			{ID: "c1", Labels: map[string]string{process.LabelSlug: "shrunk-docker", process.LabelReplicaIndex: "1"}},
		},
		pids: map[string]int{"c0": pid0, "c1": pid1},
	}
	mgr := process.NewManager(t.TempDir(), rt)
	lifecycle.RecoverProcesses(store, mgr, prx, 0, false, "")

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
			{ID: "cb1", Labels: map[string]string{process.LabelSlug: "mixed-tier", process.LabelReplicaIndex: "1"}},
		},
		pids: map[string]int{"cb1": pidBurst},
	}
	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	mgr.RegisterRuntime("burst", burst)
	lifecycle.RecoverProcesses(store, mgr, prx, 0, false, "")

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

	lifecycle.RecoverProcesses(store, mgr, prx, 0, false, "")

	if got := prx.ReplicaTargetURL("rec-endpoint", 0); got != endpoint {
		t.Fatalf("recovered replica registered %q; want stored endpoint %q", got, endpoint)
	}
}

// fakeRemoteRuntime is a process.Runtime that also implements
// process.ReplicaInventory, standing in for a remote tier whose replicas live
// on a separate worker and are reconciled from the agent inventory.
type fakeRemoteRuntime struct {
	items []process.InventoryItem
	err   error
}

func (f *fakeRemoteRuntime) Start(context.Context, process.StartParams, io.Writer) (process.ReplicaEndpoint, error) {
	return process.ReplicaEndpoint{}, nil
}
func (f *fakeRemoteRuntime) Signal(process.RunHandle, syscall.Signal) error { return nil }
func (f *fakeRemoteRuntime) Wait(context.Context, process.RunHandle) error  { return nil }
func (f *fakeRemoteRuntime) Stats(context.Context, process.RunHandle) (float64, uint64, error) {
	return 0, 0, nil
}
func (f *fakeRemoteRuntime) RunOnce(context.Context, process.StartParams, io.Writer) (process.ExitInfo, error) {
	return process.ExitInfo{}, nil
}
func (f *fakeRemoteRuntime) HostPreparesDeps() bool    { return false }
func (f *fakeRemoteRuntime) AppBindHost() string       { return "0.0.0.0" }
func (f *fakeRemoteRuntime) HostProvidesAppData() bool { return true }
func (f *fakeRemoteRuntime) Inventory(context.Context) ([]process.InventoryItem, error) {
	return f.items, f.err
}

func TestRecoverProcesses_RemoteTierAdoptsByDeploymentID(t *testing.T) {
	store := mustOpenStore(t)
	prx := proxy.New()
	app := mustCreateApp(t, store, "remote-app")

	depID := int64(7)
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: 0, Status: "running",
		Provider: "remote_docker", Tier: "remote",
		WorkerID: "node-a", DeploymentID: &depID,
	}); err != nil {
		t.Fatal(err)
	}
	store.DB().Exec(`UPDATE apps SET status='running', replicas=1 WHERE slug='remote-app'`)

	remote := &fakeRemoteRuntime{items: []process.InventoryItem{
		{ContainerID: "c-1", Running: true, URL: "https://w:8443/v1/data/tok", WorkerID: "node-a",
			Labels: map[string]string{
				process.LabelSlug: "remote-app", process.LabelReplicaIndex: "0",
				process.LabelDeploymentID: "7",
			}},
	}}
	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	mgr.RegisterRuntime("remote", remote)

	lifecycle.RecoverProcesses(store, mgr, prx, 0, false, "")

	info, ok := mgr.GetReplica("remote-app", 0)
	if !ok {
		t.Fatal("expected remote replica 0 adopted from inventory")
	}
	if info.EndpointURL != "https://w:8443/v1/data/tok" {
		t.Errorf("adopted EndpointURL = %q, want inventory URL", info.EndpointURL)
	}
	if got := prx.ReplicaTargetURL("remote-app", 0); got != "https://w:8443/v1/data/tok" {
		t.Errorf("proxy target = %q, want inventory URL", got)
	}
	a, _ := store.GetAppBySlug("remote-app")
	if a.Status != "running" {
		t.Errorf("expected app running, got %s", a.Status)
	}
}

// TestRecoverProcesses_UnreachableUpWorkerLeavesReplicaRunning asserts that when
// a tier's inventory is only partial (one worker could not be queried) but that
// worker is still up in the registry, the replica it owns is left running rather
// than marked lost. A still-up worker is merely unreachable for this one-shot
// startup scan (a transient blip); marking it lost would let the watcher's
// tier-gated healing re-place the slot onto a sibling worker while the original
// container keeps running, orphaning it. The WorkerDownMonitor owns the up->down
// transition and will lose the replica only if the heartbeat genuinely goes
// stale. The app must still be kept out of stopped.
func TestRecoverProcesses_UnreachableUpWorkerLeavesReplicaRunning(t *testing.T) {
	store := mustOpenStore(t)
	prx := proxy.New()
	app := mustCreateApp(t, store, "partial-app")

	// node-b owns the replica and is still up in the registry (its inventory
	// request merely failed for this scan); node-a is a healthy sibling on the
	// same tier, which is exactly the multi-worker case where premature healing
	// would orphan node-b's container.
	if err := store.UpsertWorker(db.Worker{
		NodeID: "node-b", AdvertiseAddr: "b:8443", Tier: "remote", Status: "up",
	}); err != nil {
		t.Fatal(err)
	}
	depID := int64(7)
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: 0, Status: "running",
		Provider: "remote_docker", Tier: "remote",
		WorkerID: "node-b", DeploymentID: &depID,
	}); err != nil {
		t.Fatal(err)
	}
	store.DB().Exec(`UPDATE apps SET status='running', replicas=1 WHERE slug='partial-app'`)

	remote := &fakeRemoteRuntime{
		items: nil,
		err:   &process.PartialInventoryError{Workers: []string{"node-b"}},
	}
	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	mgr.RegisterRuntime("remote", remote)

	lifecycle.RecoverProcesses(store, mgr, prx, 0, false, "")

	a, _ := store.GetAppBySlug("partial-app")
	if a.Status == "stopped" {
		t.Errorf("app marked stopped; an unreachable worker must not stop a live app")
	}
	reps, _ := store.ListReplicas(app.ID)
	if len(reps) != 1 || reps[0].Status != db.ReplicaStatusRunning {
		t.Errorf("replica status = %+v, want %q (still-up worker must not be healed)", reps, db.ReplicaStatusRunning)
	}
}

// TestRecoverProcesses_UnreachableDownWorkerMarksReplicaLost asserts that when
// the owning worker has already been declared down, recovery marks its
// indeterminate replica lost so the watcher re-places it. This is necessary
// because ListWorkersStale skips rows already marked down, so the
// WorkerDownMonitor never re-loses an already-down worker's replicas; recovery is
// the one pass that enters them into the lost-healing path.
func TestRecoverProcesses_UnreachableDownWorkerMarksReplicaLost(t *testing.T) {
	store := mustOpenStore(t)
	prx := proxy.New()
	app := mustCreateApp(t, store, "down-worker-app")

	if err := store.UpsertWorker(db.Worker{
		NodeID: "node-b", AdvertiseAddr: "b:8443", Tier: "remote", Status: "down",
	}); err != nil {
		t.Fatal(err)
	}
	depID := int64(7)
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: 0, Status: "running",
		Provider: "remote_docker", Tier: "remote",
		WorkerID: "node-b", DeploymentID: &depID,
	}); err != nil {
		t.Fatal(err)
	}
	store.DB().Exec(`UPDATE apps SET status='running', replicas=1 WHERE slug='down-worker-app'`)

	remote := &fakeRemoteRuntime{
		items: nil,
		err:   &process.PartialInventoryError{Workers: []string{"node-b"}},
	}
	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	mgr.RegisterRuntime("remote", remote)

	lifecycle.RecoverProcesses(store, mgr, prx, 0, false, "")

	a, _ := store.GetAppBySlug("down-worker-app")
	if a.Status == "stopped" {
		t.Errorf("app marked stopped; an indeterminate replica must not stop a live app")
	}
	reps, _ := store.ListReplicas(app.ID)
	if len(reps) != 1 || reps[0].Status != db.ReplicaStatusLost {
		t.Errorf("replica status = %+v, want %q so the watcher re-places a down worker's slot", reps, db.ReplicaStatusLost)
	}
	// The lost replica must keep its tier and worker identity: the watcher's
	// lost-replica healing is gated on the replica's tier.
	if len(reps) == 1 && (reps[0].Tier != "remote" || reps[0].WorkerID != "node-b") {
		t.Errorf("lost replica tier/worker = %q/%q, want %q/%q preserved for healing",
			reps[0].Tier, reps[0].WorkerID, "remote", "node-b")
	}
}

// TestRecoverProcesses_TotalInventoryOutageLeavesUpWorkerRunning asserts that
// when a tier's inventory fails wholesale (a plain, non-partial error) but the
// replica's owning worker is still up in the registry, recovery neither stops
// the app nor marks the replica lost. A full-tier inventory outage at
// control-plane startup is treated like any other transient unreachability: a
// still-up worker is left for the WorkerDownMonitor to lose only if its
// heartbeat genuinely goes stale, so the watcher cannot orphan its container by
// re-placing the slot onto a sibling worker.
func TestRecoverProcesses_TotalInventoryOutageLeavesUpWorkerRunning(t *testing.T) {
	store := mustOpenStore(t)
	prx := proxy.New()
	app := mustCreateApp(t, store, "outage-app")

	if err := store.UpsertWorker(db.Worker{
		NodeID: "node-a", AdvertiseAddr: "a:8443", Tier: "remote", Status: "up",
	}); err != nil {
		t.Fatal(err)
	}
	depID := int64(7)
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: 0, Status: "running",
		Provider: "remote_docker", Tier: "remote",
		WorkerID: "node-a", DeploymentID: &depID,
	}); err != nil {
		t.Fatal(err)
	}
	store.DB().Exec(`UPDATE apps SET status='running', replicas=1 WHERE slug='outage-app'`)

	// Whole-tier failure: a plain joined error, not a PartialInventoryError.
	remote := &fakeRemoteRuntime{items: nil, err: errors.New("all workers failed")}
	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	mgr.RegisterRuntime("remote", remote)

	lifecycle.RecoverProcesses(store, mgr, prx, 0, false, "")

	a, _ := store.GetAppBySlug("outage-app")
	if a.Status == "stopped" {
		t.Errorf("app marked stopped; a total inventory outage must not stop a live app")
	}
	reps, _ := store.ListReplicas(app.ID)
	if len(reps) != 1 || reps[0].Status != db.ReplicaStatusRunning {
		t.Errorf("replica status = %+v, want %q (still-up worker must not be healed)", reps, db.ReplicaStatusRunning)
	}
}

// TestRecoverProcesses_DownSiblingWorkerReplicaMarkedLost asserts that a replica
// owned by an already-down worker is marked lost even when a sibling worker on
// the same tier is up and reachable. A down worker is never queried, so it
// appears in neither the allDown flag nor the partial-inventory unreachable set;
// without explicit handling its replica would fall through unadopted and
// unhealed, stranding capacity while the app still looks alive via the sibling.
func TestRecoverProcesses_DownSiblingWorkerReplicaMarkedLost(t *testing.T) {
	store := mustOpenStore(t)
	prx := proxy.New()
	app := mustCreateApp(t, store, "sibling-app")

	// node-a (the replica's owner) was persisted down before this restart;
	// node-b is an up sibling on the same tier whose inventory query succeeds.
	if err := store.UpsertWorker(db.Worker{
		NodeID: "node-a", AdvertiseAddr: "a:8443", Tier: "remote", Status: "down",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertWorker(db.Worker{
		NodeID: "node-b", AdvertiseAddr: "b:8443", Tier: "remote", Status: "up",
	}); err != nil {
		t.Fatal(err)
	}
	depID := int64(7)
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: 0, Status: "running",
		Provider: "remote_docker", Tier: "remote",
		WorkerID: "node-a", DeploymentID: &depID,
	}); err != nil {
		t.Fatal(err)
	}
	store.DB().Exec(`UPDATE apps SET status='running', replicas=1 WHERE slug='sibling-app'`)

	// The reachable sibling reports no container for this app (node-a's
	// container is unreachable), and the query itself succeeds (no error).
	remote := &fakeRemoteRuntime{items: nil, err: nil}
	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	mgr.RegisterRuntime("remote", remote)

	lifecycle.RecoverProcesses(store, mgr, prx, 0, false, "")

	reps, _ := store.ListReplicas(app.ID)
	if len(reps) != 1 || reps[0].Status != db.ReplicaStatusLost {
		t.Errorf("replica status = %+v, want %q so the watcher re-places a down worker's slot", reps, db.ReplicaStatusLost)
	}
	if len(reps) == 1 && (reps[0].Tier != "remote" || reps[0].WorkerID != "node-a") {
		t.Errorf("lost replica tier/worker = %q/%q, want %q/%q preserved for healing",
			reps[0].Tier, reps[0].WorkerID, "remote", "node-a")
	}
	// Marking the only replica lost must NOT drive the app to stopped: the
	// lost-replica healer only scans running/degraded apps, so a stopped app
	// would strand the slot it just queued for healing until a manual restart.
	a, _ := store.GetAppBySlug("sibling-app")
	if a.Status == "stopped" {
		t.Errorf("app status = %q, want it kept reconcilable so the watcher can re-place the lost slot", a.Status)
	}
}

func TestRecoverProcesses_RemoteStaleDeploymentNotAdopted(t *testing.T) {
	store := mustOpenStore(t)
	prx := proxy.New()
	app := mustCreateApp(t, store, "stale-app")

	// The owning worker is up and reachable (it is the one reporting the
	// inventory); only the container it reports is from a superseded deployment.
	// An up owner must not be force-lost by recovery, so this isolates the test
	// to deployment staleness rather than worker liveness.
	if err := store.UpsertWorker(db.Worker{
		NodeID: "node-a", AdvertiseAddr: "a:8443", Tier: "remote", Status: "up",
	}); err != nil {
		t.Fatal(err)
	}

	depID := int64(8) // current deployment is 8
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: 0, Status: "running",
		Provider: "remote_docker", Tier: "remote",
		WorkerID: "node-a", DeploymentID: &depID,
	}); err != nil {
		t.Fatal(err)
	}
	store.DB().Exec(`UPDATE apps SET status='running', replicas=1 WHERE slug='stale-app'`)

	// Inventory has a container for slug+index but from a superseded deployment.
	remote := &fakeRemoteRuntime{items: []process.InventoryItem{
		{ContainerID: "c-old", Running: true, URL: "https://w:8443/v1/data/old",
			Labels: map[string]string{
				process.LabelSlug: "stale-app", process.LabelReplicaIndex: "0",
				process.LabelDeploymentID: "5",
			}},
	}}
	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	mgr.RegisterRuntime("remote", remote)

	lifecycle.RecoverProcesses(store, mgr, prx, 0, false, "")

	if _, ok := mgr.GetReplica("stale-app", 0); ok {
		t.Error("stale-deployment container must not be adopted as current")
	}
}

func TestRecoverProcesses_RemoteLostReplicaSkipped(t *testing.T) {
	store := mustOpenStore(t)
	prx := proxy.New()
	app := mustCreateApp(t, store, "lost-app")

	depID := int64(7)
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: 0, Status: db.ReplicaStatusLost,
		Provider: "remote_docker", Tier: "remote",
		WorkerID: "node-a", DeploymentID: &depID,
	}); err != nil {
		t.Fatal(err)
	}
	store.DB().Exec(`UPDATE apps SET status='running', replicas=1 WHERE slug='lost-app'`)

	// A matching live container exists, but a lost replica must be skipped.
	remote := &fakeRemoteRuntime{items: []process.InventoryItem{
		{ContainerID: "c-1", Running: true, URL: "https://w:8443/v1/data/tok",
			Labels: map[string]string{
				process.LabelSlug: "lost-app", process.LabelReplicaIndex: "0",
				process.LabelDeploymentID: "7",
			}},
	}}
	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	mgr.RegisterRuntime("remote", remote)

	lifecycle.RecoverProcesses(store, mgr, prx, 0, false, "")

	if _, ok := mgr.GetReplica("lost-app", 0); ok {
		t.Error("lost replica must not be adopted")
	}
}

// TestRecoverProcesses_SkipsWarmRows is the recovery pin for warm-shrunk apps.
// After a server restart: replica 0 is a live native process that should be
// adopted; replicas 1 and 2 are warm-parked (status=stopped, desired_state='warm').
// Recovery must NOT crash-mark warm rows and must NOT register them into the
// proxy pool. Only slot 0 should be adopted; the app stays running.
func TestRecoverProcesses_SkipsWarmRows(t *testing.T) {
	store := mustOpenStore(t)
	app := mustCreateApp(t, store, "warm-shrunk")

	if _, err := store.DB().Exec(`UPDATE apps SET status='running', replicas=3 WHERE slug='warm-shrunk'`); err != nil {
		t.Fatal(err)
	}

	// Replica 0: alive (current process PID) with a real listener.
	pidAlive, port0 := os.Getpid(), liveListener(t)
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: 0, PID: &pidAlive, Port: &port0,
		Status: "running", DesiredState: "running",
	}); err != nil {
		t.Fatal(err)
	}

	// Replicas 1 and 2: warm-parked — status=stopped, desired_state='warm', no PID.
	for _, idx := range []int{1, 2} {
		if err := store.UpsertReplica(db.UpsertReplicaParams{
			AppID: app.ID, Index: idx,
			Status: "stopped", DesiredState: db.ReplicaDesiredWarm,
		}); err != nil {
			t.Fatal(err)
		}
	}

	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	prx := proxy.New()
	lifecycle.RecoverProcesses(store, mgr, prx, 0, false, "")

	// Only replica 0 should be adopted by the manager.
	if _, ok := mgr.GetReplica("warm-shrunk", 0); !ok {
		t.Error("expected replica 0 to be adopted")
	}
	if _, ok := mgr.GetReplica("warm-shrunk", 1); ok {
		t.Error("replica 1 was adopted; warm rows must not be adopted")
	}
	if _, ok := mgr.GetReplica("warm-shrunk", 2); ok {
		t.Error("replica 2 was adopted; warm rows must not be adopted")
	}

	// Warm rows must retain their status=stopped/desired_state='warm'; they must
	// NOT be crash-marked. A crashed mark would cause the watcher to restart them,
	// defeating the pre-warming feature.
	reps, err := store.ListReplicas(app.ID)
	if err != nil {
		t.Fatalf("ListReplicas: %v", err)
	}
	for _, r := range reps {
		switch r.Index {
		case 1, 2:
			if r.Status != "stopped" {
				t.Errorf("warm replica %d: status = %q, want stopped (must not be crash-marked)", r.Index, r.Status)
			}
			if r.DesiredState != db.ReplicaDesiredWarm {
				t.Errorf("warm replica %d: desired_state = %q, want %q", r.Index, r.DesiredState, db.ReplicaDesiredWarm)
			}
		}
	}

	// App stays running (replica 0 is alive).
	a, err := store.GetAppBySlug("warm-shrunk")
	if err != nil {
		t.Fatalf("GetAppBySlug: %v", err)
	}
	if a.Status != "running" {
		t.Errorf("app status = %q, want running (at least one live replica)", a.Status)
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

func TestWorkerDownMonitor_ExcludesDownedWorkerFromRouting(t *testing.T) {
	store := mustOpenStore(t)
	reg, err := worker.NewRegistry(store)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	node, err := reg.Register(worker.RegisterParams{AdvertiseAddr: "w:8443", Tier: "remote"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	// A worker is routable only after its first heartbeat (Register -> joining).
	if _, _, err := reg.Heartbeat(node.NodeID, "", 0); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if _, ok := reg.WorkerForTier("remote"); !ok {
		t.Fatal("worker not routable after its first heartbeat")
	}
	stale := time.Now().Add(-10 * time.Minute).UTC().Format("2006-01-02 15:04:05")
	if _, err := store.DB().Exec(`UPDATE workers SET last_heartbeat = ? WHERE node_id = ?`, stale, node.NodeID); err != nil {
		t.Fatal(err)
	}

	monitor := lifecycle.NewWorkerDownMonitor(store, time.Minute, time.Hour, reg.MarkDown, func(string, int, string) {}, nil, reg.Forget)
	monitor.Sweep(time.Now())

	if _, ok := reg.WorkerForTier("remote"); ok {
		t.Fatal("downed worker still routable: monitor did not update the in-memory registry index")
	}
}

func TestWorkerDownMonitor_TransitionsReplicasToLostAndDeregisters(t *testing.T) {
	store := mustOpenStore(t)
	prx := proxy.New()
	app := mustCreateApp(t, store, "wd-app")

	if err := store.UpsertWorker(db.Worker{
		NodeID: "node-a", AdvertiseAddr: "w:8443", Tier: "remote", Status: "up",
	}); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-10 * time.Minute).UTC().Format("2006-01-02 15:04:05")
	if _, err := store.DB().Exec(`UPDATE workers SET last_heartbeat = ? WHERE node_id = ?`, old, "node-a"); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: 0, Status: db.ReplicaStatusRunning,
		Provider: "remote_docker", Tier: "remote", WorkerID: "node-a",
		EndpointURL: "https://w:8443/v1/data/tok",
	}); err != nil {
		t.Fatal(err)
	}
	prx.SetPoolSize("wd-app", 1)
	if err := prx.RegisterReplica("wd-app", 0, "https://w:8443/v1/data/tok", nil, 0); err != nil {
		t.Fatal(err)
	}

	deregistered := false
	monitor := lifecycle.NewWorkerDownMonitor(store, time.Minute, time.Hour,
		func(nodeID string) error { return store.SetWorkerStatus(nodeID, "down") },
		func(slug string, index int, expectURL string) {
			deregistered = true
			prx.DeregisterReplicaIfTarget(slug, index, expectURL)
		},
		nil, nil)
	monitor.Sweep(time.Now())

	if w, _ := store.GetWorker("node-a"); w == nil || w.Status != "down" {
		t.Errorf("worker status = %+v, want down", w)
	}
	reps, _ := store.ListReplicas(app.ID)
	if len(reps) != 1 || reps[0].Status != db.ReplicaStatusLost {
		t.Errorf("replica status = %+v, want lost", reps)
	}
	if !deregistered {
		t.Error("lost replica was not deregistered from the proxy")
	}
	if got := prx.ReplicaTargetURL("wd-app", 0); got != "" {
		t.Errorf("replica still routable after deregister: %q", got)
	}
}

// TestLoseWorkerReplicas asserts the loss pass transitions only the running
// replicas genuinely owned by the worker and deregisters exactly those, leaving
// replicas re-placed onto another worker and replicas already in a terminal
// state untouched. (The atomic ownership guard that protects against a concurrent
// re-placement landing mid-pass is covered by the DB-layer
// MarkReplicaLostIfOwnedBy test.)
func TestLoseWorkerReplicas(t *testing.T) {
	store := mustOpenStore(t)
	app := mustCreateApp(t, store, "lose-app")

	seed := func(idx int, status, workerID string) {
		if err := store.UpsertReplica(db.UpsertReplicaParams{
			AppID: app.ID, Index: idx, Status: status,
			Provider: "remote_docker", Tier: "remote", WorkerID: workerID,
		}); err != nil {
			t.Fatalf("seed replica %d: %v", idx, err)
		}
	}
	// idx0: re-placed onto a healthy worker -> not in node-dead's set.
	seed(0, db.ReplicaStatusRunning, "node-healthy")
	// idx1: genuinely owned by the dead worker and running -> lost + deregistered.
	seed(1, db.ReplicaStatusRunning, "node-dead")
	// idx2: owned by the dead worker but already terminal -> skipped.
	seed(2, "stopped", "node-dead")

	deregistered := map[int]bool{}
	if err := lifecycle.LoseWorkerReplicas(store, "node-dead", func(_ string, idx int, _ string) {
		deregistered[idx] = true
	}, nil); err != nil {
		t.Fatalf("LoseWorkerReplicas: %v", err)
	}

	reps, _ := store.ListReplicas(app.ID)
	byIdx := map[int]*db.Replica{}
	for _, r := range reps {
		byIdx[r.Index] = r
	}
	if byIdx[0].Status != db.ReplicaStatusRunning || deregistered[0] {
		t.Errorf("replica on a different worker was touched: status=%q deregistered=%v", byIdx[0].Status, deregistered[0])
	}
	if byIdx[1].Status != db.ReplicaStatusLost || !deregistered[1] {
		t.Errorf("dead worker's running replica not lost/deregistered: status=%q deregistered=%v", byIdx[1].Status, deregistered[1])
	}
	if byIdx[2].Status != "stopped" || deregistered[2] {
		t.Errorf("terminal replica was touched: status=%q deregistered=%v", byIdx[2].Status, deregistered[2])
	}
}

// TestLoseWorkerReplicas_EvictsManagerEntry asserts the loss pass invokes the
// evict callback for exactly the slots it transitions to lost, so the process
// manager drops the dead worker's entry and a re-placement Start at the same
// slug+index is not rejected as already running. Terminal slots are not evicted.
func TestLoseWorkerReplicas_EvictsManagerEntry(t *testing.T) {
	store := mustOpenStore(t)
	app := mustCreateApp(t, store, "evict-app")

	seed := func(idx int, status, workerID string) {
		if err := store.UpsertReplica(db.UpsertReplicaParams{
			AppID: app.ID, Index: idx, Status: status,
			Provider: "remote_docker", Tier: "remote", WorkerID: workerID,
		}); err != nil {
			t.Fatalf("seed replica %d: %v", idx, err)
		}
	}
	seed(0, db.ReplicaStatusRunning, "node-dead") // changed -> evict
	seed(1, "stopped", "node-dead")               // terminal -> no evict

	evicted := map[int]bool{}
	if err := lifecycle.LoseWorkerReplicas(store, "node-dead",
		func(_ string, _ int, _ string) {},
		func(slug string, idx int, workerID string) {
			if slug != "evict-app" {
				t.Errorf("evict slug = %q, want evict-app", slug)
			}
			if workerID != "node-dead" {
				t.Errorf("evict workerID = %q, want node-dead", workerID)
			}
			evicted[idx] = true
		},
	); err != nil {
		t.Fatalf("LoseWorkerReplicas: %v", err)
	}

	if !evicted[0] {
		t.Error("expected evict for the lost replica slot 0")
	}
	if evicted[1] {
		t.Error("terminal slot 1 must not be evicted")
	}
}

// TestWorkerDownMonitor_ReapsLongDeadWorkers asserts the sweep reaps worker rows
// that have been down past the retention window (dropping them from both the
// store and the in-memory registry), while leaving a worker that only just went
// down (stale for the timeout but within retention) in place.
func TestWorkerDownMonitor_ReapsLongDeadWorkers(t *testing.T) {
	store := mustOpenStore(t)
	reg, err := worker.NewRegistry(store)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}

	// ancient: down well past retention -> reaped.
	ancient, err := reg.Register(worker.RegisterParams{AdvertiseAddr: "old:8443", Tier: "remote"})
	if err != nil {
		t.Fatalf("register ancient: %v", err)
	}
	if err := reg.MarkDown(ancient.NodeID); err != nil {
		t.Fatalf("mark ancient down: %v", err)
	}
	longAgo := time.Now().Add(-48 * time.Hour).UTC().Format("2006-01-02 15:04:05")
	if _, err := store.DB().Exec(`UPDATE workers SET last_heartbeat = ? WHERE node_id = ?`, longAgo, ancient.NodeID); err != nil {
		t.Fatal(err)
	}

	// recent: down but within retention -> kept. A distinct tier avoids the
	// single-up-per-tier supersede interfering with the assertion.
	recent, err := reg.Register(worker.RegisterParams{AdvertiseAddr: "new:8443", Tier: "base"})
	if err != nil {
		t.Fatalf("register recent: %v", err)
	}
	if err := reg.MarkDown(recent.NodeID); err != nil {
		t.Fatalf("mark recent down: %v", err)
	}
	withinRetention := time.Now().Add(-30 * time.Minute).UTC().Format("2006-01-02 15:04:05")
	if _, err := store.DB().Exec(`UPDATE workers SET last_heartbeat = ? WHERE node_id = ?`, withinRetention, recent.NodeID); err != nil {
		t.Fatal(err)
	}

	monitor := lifecycle.NewWorkerDownMonitor(store, time.Minute, time.Hour,
		reg.MarkDown, func(string, int, string) {}, nil, reg.Forget)
	monitor.Sweep(time.Now())

	if w, err := store.GetWorker(ancient.NodeID); err != db.ErrNotFound {
		t.Errorf("ancient worker not reaped from store: %+v err=%v", w, err)
	}
	if _, ok := reg.Worker(ancient.NodeID); ok {
		t.Error("ancient worker still in the in-memory registry after reap")
	}
	if _, err := store.GetWorker(recent.NodeID); err != nil {
		t.Errorf("recently-downed worker wrongly reaped: %v", err)
	}
}
