package lifecycle_test

import (
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/lifecycle"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
	"testing"
	"time"
)

func TestRecoveryIsolationLedgerSurvivesDesiredModeDrift(t *testing.T) {
	store := mustOpenStore(t)
	app := mustCreateApp(t, store, "policy-recovery")
	if _, err := store.DB().Exec("UPDATE apps SET status='running',worker_isolation='multiplex' WHERE id=?", app.ID); err != nil {
		t.Fatal(err)
	}
	bundle := t.TempDir()
	dep, err := store.BeginDeployment(app.ID, "v1", bundle)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordDeploymentWorkerIsolation(app.ID, dep.ID, "grouped"); err != nil {
		t.Fatal(err)
	}
	if err := store.PromoteDeployment(dep.ID); err != nil {
		t.Fatal(err)
	}
	pid, done := startNativeProcess(t, bundle)
	if err := store.UpsertDeploymentReplica(db.UpsertDeploymentReplicaParams{AppID: app.ID, DeploymentID: dep.ID, Index: 7, PID: &pid, Status: "running", Provider: "native"}); err != nil {
		t.Fatal(err)
	}
	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	defer mgr.StopAll()
	lifecycle.RecoverProcesses(store, mgr, proxy.New(), 0, false, "multiplex", nil, mustPrepareRecovery(t, store))
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("grouped generation adopted under desired multiplex")
	}
	rows, err := store.ListDeploymentReplicas(app.ID)
	if err != nil || len(rows) != 0 {
		t.Fatalf("ledger=%v %v", rows, err)
	}
	if mgr.HasRunning(app.Slug) {
		t.Fatal("grouped workers adopted as fixed")
	}
}

func TestRecoveryCleansOrphanLedgerRegardlessDesiredMode(t *testing.T) {
	for _, status := range []string{"running", "hibernated"} {
		t.Run(status, func(t *testing.T) {
			store := mustOpenStore(t)
			app := mustCreateApp(t, store, "orphan-policy")
			if _, err := store.DB().Exec("UPDATE apps SET status=?,worker_isolation='multiplex' WHERE id=?", status, app.ID); err != nil {
				t.Fatal(err)
			}
			dep, err := store.BeginDeployment(app.ID, "candidate", t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err := store.RecordDeploymentWorkerIsolation(app.ID, dep.ID, "grouped"); err != nil {
				t.Fatal(err)
			}
			if err := store.UpsertDeploymentReplica(db.UpsertDeploymentReplicaParams{AppID: app.ID, DeploymentID: dep.ID, Index: 7, Status: "running", Provider: "native"}); err != nil {
				t.Fatal(err)
			}
			mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
			defer mgr.StopAll()
			lifecycle.RecoverProcesses(store, mgr, proxy.New(), 0, false, "multiplex", nil, mustPrepareRecovery(t, store))
			rows, err := store.ListDeploymentReplicas(app.ID)
			if err != nil || len(rows) != 0 {
				t.Fatalf("leftover ledger=%v %v", rows, err)
			}
		})
	}
}

func TestRecoveryBindsLegacyPolicyBeforeAdoptingLiveWorkers(t *testing.T) {
	store := mustOpenStore(t)
	app := mustCreateApp(t, store, "legacy-policy-adoption")
	bundle := t.TempDir()
	dep, err := store.BeginDeployment(app.ID, "v1", bundle)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PromoteDeployment(dep.ID); err != nil {
		t.Fatal(err)
	}
	pid, _ := startNativeProcess(t, bundle)
	port := liveListener(t)
	if err := store.UpsertReplica(db.UpsertReplicaParams{AppID: app.ID, Index: 0, PID: &pid, Port: &port, Status: "running", Provider: "native", DeploymentID: &dep.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec("UPDATE apps SET status='running',replicas=1 WHERE id=?", app.ID); err != nil {
		t.Fatal(err)
	}
	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	prx := proxy.New()
	defer mgr.StopAll()
	lifecycle.RecoverProcesses(store, mgr, prx, 0, false, "multiplex", nil, mustPrepareRecovery(t, store))
	if !mgr.HoldsLiveProcess(app.Slug) {
		t.Fatal("legacy worker was not adopted")
	}
	if mode, err := store.GetDeploymentWorkerIsolation(dep.ID); err != nil || mode != "multiplex" {
		t.Fatalf("legacy snapshot=%q %v", mode, err)
	}
	app, err = store.GetAppByID(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if mode, err := lifecycle.PrepareColdWorkerIsolation(store, mgr, prx, app, "multiplex"); err != nil || mode != "multiplex" {
		t.Fatalf("same legacy policy refused=%q %v", mode, err)
	}
	app.WorkerIsolation = "grouped"
	if mode, err := lifecycle.PrepareColdWorkerIsolation(store, mgr, prx, app, "multiplex"); err != nil || mode != "multiplex" {
		t.Fatalf("living legacy generation policy not preserved=%q %v", mode, err)
	}
	if mode, err := store.GetDeploymentWorkerIsolation(dep.ID); err != nil || mode != "multiplex" {
		t.Fatalf("legacy policy overwritten=%q %v", mode, err)
	}
}
