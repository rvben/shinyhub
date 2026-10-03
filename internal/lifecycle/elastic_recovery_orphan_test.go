package lifecycle_test

import (
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/lifecycle"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
)

// These tests cover the elastic-orphan reconciliation path added to
// RecoverProcesses/PrepareRecovery: an app that fell out of the
// running/degraded set (hibernated, crashed, or a mode switch away from
// elastic) while a native worker identity was still recorded for it in
// deployment_replicas. They complement
// TestRecoverProcesses_ElasticGenerationsAreReapedWithoutFixedReplicaAdoption,
// which only covers a currently-running elastic app's leftover generations.

// TestRecoverProcesses_ElasticOrphanLoopSkipsNonElasticApp verifies that a
// non-elastic (fixed-replica) app that still carries deployment_replicas rows
// - left behind by an older rolling deploy, since that table is written by
// more than just elastic apps - is not touched by the elastic-orphan loop:
// its process keeps running and its row survives, because reconciling it is
// the fixed-replica adoption path's job, not this one's.
func TestRecoverProcesses_ElasticOrphanLoopSkipsNonElasticApp(t *testing.T) {
	store := mustOpenStore(t)
	app := mustCreateApp(t, store, "fixed-replica-orphan") // default worker_isolation: multiplex
	bundleDir := t.TempDir()
	dep, err := store.BeginDeployment(app.ID, "v1", bundleDir)
	if err != nil {
		t.Fatal(err)
	}
	pid, done := startNativeProcess(t, bundleDir)
	if err := store.UpsertDeploymentReplica(db.UpsertDeploymentReplicaParams{
		AppID: app.ID, DeploymentID: dep.ID, Index: 0, PID: &pid,
		Status: "running", Provider: "native",
	}); err != nil {
		t.Fatal(err)
	}
	// Hibernated (not running/degraded) so PrepareRecovery surfaces it as an
	// elastic-orphan candidate; it must be filtered back out by the
	// isElasticIsolation guard before any reconciliation runs.
	if _, err := store.DB().Exec(`UPDATE apps SET status='hibernated' WHERE id=?`, app.ID); err != nil {
		t.Fatal(err)
	}

	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	prx := proxy.New()
	lifecycle.RecoverProcesses(store, mgr, prx, 0, false, "", nil, mustPrepareRecovery(t, store))

	select {
	case <-done:
		t.Fatal("fixed-replica orphan's process was stopped; the elastic-orphan loop must not touch it")
	case <-time.After(200 * time.Millisecond):
	}
	rows, err := store.ListDeploymentReplicas(app.ID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("expected the leftover replica row to survive untouched, got %+v err=%v", rows, err)
	}
}

// TestRecoverProcesses_ElasticOrphanNativeIdentity_StopOutcome verifies that
// a hibernated elastic app with a leftover native worker identity is
// reconciled on its own: a stop that can be confirmed deletes the identity
// row and actually stops the process, while a stop that cannot be confirmed
// (bundle identity unprovable) leaves the row in place so nothing loses the
// only record of a possibly still-live process. Either way the app's own
// status is left untouched by this pass.
func TestRecoverProcesses_ElasticOrphanNativeIdentity_StopOutcome(t *testing.T) {
	for _, confirmed := range []bool{true, false} {
		name := "unconfirmed"
		if confirmed {
			name = "confirmed"
		}
		t.Run(name, func(t *testing.T) {
			store := mustOpenStore(t)
			app := mustCreateElasticApp(t, store, "elastic-orphan-"+name)
			bundleDir := t.TempDir()
			dep, err := store.BeginDeployment(app.ID, "v1", bundleDir)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.PromoteDeployment(dep.ID); err != nil {
				t.Fatal(err)
			}

			var pid int
			var done <-chan error
			if confirmed {
				pid, done = startNativeProcess(t, bundleDir)
			} else {
				// Alive (the test binary itself) but its cwd cannot match
				// bundleDir, so the bundle-identity check fails fast without
				// ever signalling the process.
				pid = os.Getpid()
			}
			if err := store.UpsertDeploymentReplica(db.UpsertDeploymentReplicaParams{
				AppID: app.ID, DeploymentID: dep.ID, Index: 2, PID: &pid,
				Status: "running", Provider: "native",
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := store.DB().Exec(`UPDATE apps SET status='hibernated' WHERE id=?`, app.ID); err != nil {
				t.Fatal(err)
			}

			mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
			prx := proxy.New()
			lifecycle.RecoverProcesses(store, mgr, prx, 0, false, "", nil, mustPrepareRecovery(t, store))

			rows, err := store.ListDeploymentReplicas(app.ID)
			if err != nil {
				t.Fatal(err)
			}
			if confirmed {
				if len(rows) != 0 {
					t.Errorf("expected the confirmed identity row to be deleted, got %+v", rows)
				}
				select {
				case <-done:
				case <-time.After(2 * time.Second):
					t.Fatal("live process was not stopped")
				}
			} else if len(rows) != 1 {
				t.Errorf("expected the unconfirmed identity row to survive, got %+v", rows)
			}

			got, err := store.GetAppBySlug(app.Slug)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != "hibernated" {
				t.Errorf("orphan reconciliation touched app status: got %q, want hibernated", got.Status)
			}
		})
	}
}

// TestRecoverProcesses_ElasticOrphanPerWorkerGranularity verifies that
// cleanupElasticDeploymentGenerations reconciles one deployment's recorded
// workers individually rather than as a unit: with two recorded slots on the
// same deployment, the one whose stop confirms is deleted while the other -
// whose stop cannot be confirmed - survives.
func TestRecoverProcesses_ElasticOrphanPerWorkerGranularity(t *testing.T) {
	store := mustOpenStore(t)
	app := mustCreateElasticApp(t, store, "elastic-per-worker")
	bundleDir := t.TempDir()
	dep, err := store.BeginDeployment(app.ID, "v1", bundleDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PromoteDeployment(dep.ID); err != nil {
		t.Fatal(err)
	}

	confirmedPID, confirmedDone := startNativeProcess(t, bundleDir)
	unconfirmedPID := os.Getpid()

	if err := store.UpsertDeploymentReplica(db.UpsertDeploymentReplicaParams{
		AppID: app.ID, DeploymentID: dep.ID, Index: 0, PID: &confirmedPID,
		Status: "running", Provider: "native",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertDeploymentReplica(db.UpsertDeploymentReplicaParams{
		AppID: app.ID, DeploymentID: dep.ID, Index: 1, PID: &unconfirmedPID,
		Status: "running", Provider: "native",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`UPDATE apps SET status='hibernated' WHERE id=?`, app.ID); err != nil {
		t.Fatal(err)
	}

	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	prx := proxy.New()
	lifecycle.RecoverProcesses(store, mgr, prx, 0, false, "", nil, mustPrepareRecovery(t, store))

	select {
	case <-confirmedDone:
	case <-time.After(2 * time.Second):
		t.Fatal("confirmed worker's process was not stopped")
	}

	rows, err := store.ListDeploymentReplicas(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Index != 1 || rows[0].PID == nil || *rows[0].PID != unconfirmedPID {
		t.Fatalf("expected only the unconfirmed worker's row (index 1) to survive, got %+v", rows)
	}
}

// TestRecoverProcesses_ElasticOrphanPIDlessRowIsDeleted verifies that a
// native elastic identity row that never recorded a PID is cleared by the
// orphan pass. There is no process to stop for it, so leaving it in place
// would only bring the app back into the orphan scan on every restart.
func TestRecoverProcesses_ElasticOrphanPIDlessRowIsDeleted(t *testing.T) {
	store := mustOpenStore(t)
	app := mustCreateElasticApp(t, store, "elastic-pidless")
	dep, err := store.BeginDeployment(app.ID, "v1", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PromoteDeployment(dep.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertDeploymentReplica(db.UpsertDeploymentReplicaParams{
		AppID: app.ID, DeploymentID: dep.ID, Index: 0,
		Status: "starting", Provider: "native",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`UPDATE apps SET status='hibernated' WHERE id=?`, app.ID); err != nil {
		t.Fatal(err)
	}

	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	lifecycle.RecoverProcesses(store, mgr, proxy.New(), 0, false, "", nil, mustPrepareRecovery(t, store))

	rows, err := store.ListDeploymentReplicas(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("expected the pid-less identity row to be deleted, got %+v", rows)
	}
}

// TestRecoverProcesses_ElasticOrphanSeedsSlotSequenceForNextWake verifies
// that reconciling a hibernated elastic app's leftover identity actually
// prevents slot ID reuse end to end: a leftover identity recorded at index 3
// must raise the app's slot sequence so that the very first worker spawned
// after the app next wakes gets a fresh slot (>= 4), never slot 3 or lower -
// which would let a still-terminating old worker and a brand-new one collide
// on the same routing key.
func TestRecoverProcesses_ElasticOrphanSeedsSlotSequenceForNextWake(t *testing.T) {
	store := mustOpenStore(t)
	app := mustCreateElasticApp(t, store, "elastic-seed-slot")
	bundleDir := t.TempDir()
	dep, err := store.BeginDeployment(app.ID, "v1", bundleDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PromoteDeployment(dep.ID); err != nil {
		t.Fatal(err)
	}

	deadPID := 99999999 // guaranteed not to exist; the confirmed-stop path is not this test's concern.
	if err := store.UpsertDeploymentReplica(db.UpsertDeploymentReplicaParams{
		AppID: app.ID, DeploymentID: dep.ID, Index: 3, PID: &deadPID,
		Status: "running", Provider: "native",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`UPDATE apps SET status='hibernated' WHERE id=?`, app.ID); err != nil {
		t.Fatal(err)
	}

	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	prx := proxy.New()
	lifecycle.RecoverProcesses(store, mgr, prx, 0, false, "", nil, mustPrepareRecovery(t, store))

	// Simulate the wake path: a fresh, empty pool is created for the app only
	// after recovery has already seeded the slot sequence.
	prx.SetPoolAppID(app.Slug, app.ID)
	prx.SetPoolMode(app.Slug, config.IsolationPerSession, 0, 5)

	spawnCh := make(chan int, 1)
	prx.SetSpawnFunc(func(_ string, slotID int) { spawnCh <- slotID })

	req := httptest.NewRequest("GET", "/app/"+app.Slug+"/", nil)
	rec := httptest.NewRecorder()
	prx.ServeHTTP(rec, req)

	select {
	case slotID := <-spawnCh:
		if slotID < 4 {
			t.Errorf("first slot allocated after recovery = %d, want >= 4 (leftover identity occupied slot 3)", slotID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("spawn callback not invoked")
	}
}

// TestSweepOrphanContainers_RemovesLeftoverElasticDockerWorker verifies that
// a docker-backed elastic worker's container, left behind by an unconfirmed
// stop across a restart, is reconciled by the startup container sweep.
// cleanupElasticDeploymentGenerations deliberately skips docker/remote rows
// (it can only confirm a native-tier stop itself), so this is the mechanism
// that actually reclaims them: RecoverProcesses must not adopt the container
// into the Manager, and the sweep must then remove it as unowned.
func TestSweepOrphanContainers_RemovesLeftoverElasticDockerWorker(t *testing.T) {
	store := mustOpenStore(t)
	app := mustCreateElasticApp(t, store, "elastic-docker-orphan")
	bundleDir := t.TempDir()
	dep, err := store.BeginDeployment(app.ID, "v1", bundleDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PromoteDeployment(dep.ID); err != nil {
		t.Fatal(err)
	}

	pid := 55001
	if err := store.UpsertDeploymentReplica(db.UpsertDeploymentReplicaParams{
		AppID: app.ID, DeploymentID: dep.ID, Index: 0, PID: &pid,
		Status: "running", Provider: "docker", Tier: "default", WorkerID: "elastic-cont-1",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`UPDATE apps SET status='hibernated' WHERE id=?`, app.ID); err != nil {
		t.Fatal(err)
	}

	rt := &fakeDockerRuntime{
		containers: []process.ContainerInfo{{
			ID: "elastic-cont-1", State: "running", Labels: map[string]string{
				process.LabelManaged: "true", process.LabelSlug: app.Slug, process.LabelReplicaIndex: "0",
			},
		}},
		pids: map[string]int{"elastic-cont-1": pid},
	}
	mgr := process.NewManager(t.TempDir(), rt)
	prx := proxy.New()

	lifecycle.RecoverProcesses(store, mgr, prx, 0, false, "", nil, mustPrepareRecovery(t, store))

	if mgr.HasRunning(app.Slug) {
		t.Fatal("elastic docker worker was adopted into the Manager; the sweep would then wrongly skip it")
	}
	// The identity row must survive recovery untouched: the docker worker was
	// never confirmed stopped, so deleting its record here would erase the
	// only trace of it while the container itself is still running.
	if rows, err := store.ListDeploymentReplicas(app.ID); err != nil || len(rows) != 1 {
		t.Fatalf("expected the unconfirmed docker identity row to survive recovery, got %+v err=%v", rows, err)
	}

	if err := lifecycle.SweepOrphanContainers(mgr, rt); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(rt.removed) != 1 || rt.removed[0] != "elastic-cont-1" {
		t.Fatalf("sweep removed=%v, want [elastic-cont-1]", rt.removed)
	}
}
