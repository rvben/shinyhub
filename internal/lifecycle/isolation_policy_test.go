package lifecycle_test

import (
	"os"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/lifecycle"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
)

func TestPrepareColdWorkerIsolationChangesOnlyAfterAllOwnershipIsGone(t *testing.T) {
	for _, blocker := range []string{"ledger", "fixed-pid", "route", "candidate", "live-process", "suspended-process", "unknown-live-process", "none", "default-change"} {
		t.Run(blocker, func(t *testing.T) {
			store := mustOpenStore(t)
			app := mustCreateApp(t, store, "cold-policy")
			dep, err := store.BeginDeployment(app.ID, "v1", t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if blocker != "unknown-live-process" {
				if err := store.RecordDeploymentWorkerIsolation(app.ID, dep.ID, "multiplex"); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.PromoteDeployment(dep.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := store.DB().Exec("UPDATE apps SET status='hibernated',worker_isolation='grouped' WHERE id=?", app.ID); err != nil {
				t.Fatal(err)
			}
			if blocker == "default-change" {
				if _, err := store.DB().Exec("UPDATE apps SET worker_isolation='' WHERE id=?", app.ID); err != nil {
					t.Fatal(err)
				}
			}
			app, err = store.GetAppByID(app.ID)
			if err != nil {
				t.Fatal(err)
			}
			mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
			prx := proxy.New()
			switch blocker {
			case "ledger":
				if err := store.UpsertDeploymentReplica(db.UpsertDeploymentReplicaParams{AppID: app.ID, DeploymentID: dep.ID, Index: 4, Status: "running", Provider: "native"}); err != nil {
					t.Fatal(err)
				}
			case "fixed-pid":
				pid := os.Getpid()
				if err := store.UpsertReplica(db.UpsertReplicaParams{AppID: app.ID, Index: 0, PID: &pid, Status: "running", DeploymentID: &dep.ID}); err != nil {
					t.Fatal(err)
				}
			case "route":
				if err := prx.Register(app.Slug, "http://127.0.0.1:4042"); err != nil {
					t.Fatal(err)
				}
			case "candidate":
				prx.SetPoolSize(app.Slug, 1)
				if err := prx.StageGeneration(app.Slug, dep.ID+1, 1); err != nil {
					t.Fatal(err)
				}
			case "live-process", "suspended-process", "unknown-live-process":
				pid, _ := startNativeProcess(t, dep.BundleDir)
				status := process.StatusRunning
				if blocker == "suspended-process" {
					status = process.StatusSuspended
				}
				mgr.Adopt(app.Slug, process.ProcessInfo{Slug: app.Slug, AppID: app.ID, Index: 0, PID: pid, Status: status, DeploymentID: dep.ID}, process.RunHandle{PID: pid})
				defer mgr.StopAll()
			case "none", "default-change":
				// Old fixed slots with no identity must disappear on a cold grouped boot.
				if err := store.UpsertReplica(db.UpsertReplicaParams{AppID: app.ID, Index: 0, Status: "stopped"}); err != nil {
					t.Fatal(err)
				}
			}
			defaultMode := "multiplex"
			if blocker == "default-change" {
				defaultMode = "grouped"
			}
			mode, err := lifecycle.PrepareColdWorkerIsolation(store, mgr, prx, app, defaultMode)
			if blocker == "none" || blocker == "default-change" {
				if err != nil || mode != "grouped" {
					t.Fatalf("cold change=%q %v", mode, err)
				}
				reps, err := store.ListReplicas(app.ID)
				if err != nil || len(reps) != 0 {
					t.Fatalf("stale fixed slots=%v %v", reps, err)
				}
			} else {
				expected := "multiplex"
				if blocker == "fixed-pid" || blocker == "unknown-live-process" {
					if err == nil {
						t.Fatal("uncertain ownership did not fail closed")
					}
					if blocker == "unknown-live-process" {
						expected = ""
					}
				} else if err != nil || mode != expected {
					t.Fatalf("retained policy=%q %v", mode, err)
				}
				mode, err = store.GetDeploymentWorkerIsolation(dep.ID)
				if err != nil || mode != expected {
					t.Fatalf("policy changed after refusal=%q %v", mode, err)
				}
			}
		})
	}
}
