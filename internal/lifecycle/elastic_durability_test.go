package lifecycle_test

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/lifecycle"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
)

func TestElasticNativeLaunchRequiresDurableIdentity(t *testing.T) {
	for _, scenario := range []string{"recorded", "database_failure", "replacement_controller"} {
		t.Run(scenario, func(t *testing.T) {
			failRecord := scenario == "database_failure"
			store := mustOpenStore(t)
			app := mustCreateElasticApp(t, store, "durable-elastic")
			bundle := t.TempDir()
			manifest := "[app]\ncommand = [\"/bin/sh\", \"-c\", \"touch executed; exec sleep 60\"]\n"
			if err := os.WriteFile(filepath.Join(bundle, "shinyhub.toml"), []byte(manifest), 0600); err != nil {
				t.Fatal(err)
			}
			mustCreateDeploymentInDir(t, store, app.ID, bundle)
			if failRecord {
				if _, err := store.DB().Exec("DROP TABLE deployment_replicas"); err != nil {
					t.Fatal(err)
				}
			}
			mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
			t.Cleanup(func() { _ = mgr.StopAll() })
			prx := proxy.New()
			prx.SetPoolAppID(app.Slug, app.ID)
			prx.SetPoolMode(app.Slug, config.IsolationPerSession, 1, 1)
			checked := false
			s := &lifecycle.ElasticSpawner{Store: store, Manager: mgr, Proxy: prx,
				HealthCheck: func(_ string, _ time.Duration, _ http.RoundTripper) error {
					checked = true
					rows, err := store.ListDeploymentReplicas(app.ID)
					if err != nil || len(rows) != 1 || rows[0].PID == nil {
						t.Fatalf("identity missing before readiness: %v %v", rows, err)
					}
					deadline := time.Now().Add(2 * time.Second)
					for time.Now().Before(deadline) {
						if _, err := os.Stat(filepath.Join(bundle, "executed")); err == nil {
							return nil
						}
						time.Sleep(10 * time.Millisecond)
					}
					t.Fatal("recorded worker never executed")
					return nil
				},
			}
			s.Spawn(app.Slug, 0)
			if failRecord {
				if checked || mgr.HasRunning(app.Slug) || prx.ElasticWorkerCount(app.Slug) != 0 {
					t.Fatal("unrecorded worker became available")
				}
				if _, err := os.Stat(filepath.Join(bundle, "executed")); !os.IsNotExist(err) {
					t.Fatalf("unrecorded app executed: %v", err)
				}
				return
			}
			if !checked {
				t.Fatal("worker did not reach readiness")
			}
			// A worker whose identity is recorded before it executes can never
			// become an untracked survivor, so it leaves no orphan-risk marker.
			if risk, err := store.AppElasticOrphanRisk(app.ID); err != nil || risk {
				t.Fatalf("recorded native worker left an orphan-risk marker: risk=%v err=%v", risk, err)
			}
			if scenario == "replacement_controller" {
				oldWorker, _ := mgr.GetReplica(app.Slug, 0)
				replacement := process.NewManager(t.TempDir(), process.NewNativeRuntime())
				t.Cleanup(func() { _ = replacement.StopAll() })
				lifecycle.RecoverProcesses(store, replacement, proxy.New(), 0, false, "multiplex")
				if !errors.Is(syscall.Kill(oldWorker.PID, 0), syscall.ESRCH) || replacement.HasRunning(app.Slug) {
					t.Fatal("controller replacement left old worker running or adopted it without session bindings")
				}
			} else {
				s.Terminate(app.Slug, 0)
			}
			rows, err := store.ListDeploymentReplicas(app.ID)
			if err != nil || len(rows) != 0 {
				t.Fatalf("confirmed stop retained identity: %v %v", rows, err)
			}
		})
	}
}

func TestElasticRecoveryRetainsUnconfirmedRemoteIdentity(t *testing.T) {
	store := mustOpenStore(t)
	app := mustCreateElasticApp(t, store, "remote-survivor")
	dep := mustCreateDeploymentInDir(t, store, app.ID, t.TempDir())
	pid := os.Getpid() // A remote PID must never be interpreted on this host.
	if err := store.UpsertDeploymentReplica(db.UpsertDeploymentReplicaParams{
		AppID: app.ID, DeploymentID: dep.ID, Index: 0, PID: &pid,
		Status: "running", Provider: "native", WorkerID: "remote-node",
	}); err != nil {
		t.Fatal(err)
	}
	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	prx := proxy.New()
	lifecycle.RecoverProcesses(store, mgr, prx, 0, false, "multiplex")
	rows, err := store.ListDeploymentReplicas(app.ID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("lost unresolved worker identity: %v %v", rows, err)
	}
	app, err = store.GetAppBySlug(app.Slug)
	if err != nil || app.Status != "failed" {
		t.Fatalf("app exposed despite unresolved survivor: %v %v", app, err)
	}
}

func TestElasticIdentityCleanupCannotDeleteReplacement(t *testing.T) {
	store := mustOpenStore(t)
	app := mustCreateElasticApp(t, store, "replacement")
	dep := mustCreateDeploymentInDir(t, store, app.ID, t.TempDir())
	pid := 2222
	if err := store.UpsertDeploymentReplica(db.UpsertDeploymentReplicaParams{
		AppID: app.ID, DeploymentID: dep.ID, Index: 0, PID: &pid, Status: "starting", Provider: "native",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteDeploymentReplicaIdentity(app.ID, dep.ID, 0, 1111); err != nil {
		t.Fatal(err)
	}
	rows, err := store.ListDeploymentReplicas(app.ID)
	if err != nil || len(rows) != 1 || *rows[0].PID != pid {
		t.Fatalf("stale cleanup removed replacement: %v %v", rows, err)
	}
}
