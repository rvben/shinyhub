package lifecycle_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/lifecycle"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
)

func TestWakeIsolationChangePreservesSuspendedPolicyAndAdoptsColdPolicy(t *testing.T) {
	for _, suspended := range []bool{true, false} {
		for _, inherited := range []bool{true, false} {
			t.Run(fmt.Sprintf("suspended=%t/inherited=%t", suspended, inherited), func(t *testing.T) {
				store := mustOpenStore(t)
				app := mustCreateApp(t, store, "isolation-wake")
				dep := mustCreateDeploymentInDir(t, store, app.ID, mustMinimalBundle(t))
				if err := store.RecordDeploymentWorkerIsolation(app.ID, dep.ID, "multiplex"); err != nil {
					t.Fatal(err)
				}
				rt := &recordingRuntime{exitOnSignal: true, suspendFreed: true}
				t.Cleanup(rt.closeExits)
				mgr := process.NewManager(t.TempDir(), rt)
				prx := proxy.New()
				var frozen *process.ProcessInfo
				if suspended {
					var err error
					frozen, err = mgr.Start(process.StartParams{Slug: app.Slug, AppID: app.ID, Index: 0, Port: 23010, Command: []string{"app"}, DeploymentID: dep.ID})
					if err != nil {
						t.Fatal(err)
					}
					if freed, err := mgr.Suspend(app.Slug); err != nil || !freed {
						t.Fatalf("suspend: %t %v", freed, err)
					}
					if err := store.UpsertReplica(db.UpsertReplicaParams{AppID: app.ID, Index: 0, Status: db.ReplicaStatusSuspended, PID: &frozen.PID, Port: &frozen.Port, DeploymentID: &dep.ID}); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := store.UpsertReplica(db.UpsertReplicaParams{AppID: app.ID, Index: 0, Status: "stopped", DeploymentID: &dep.ID}); err != nil {
						t.Fatal(err)
					}
				}
				desired, defaultMode := "grouped", "multiplex"
				if inherited {
					desired = ""
					defaultMode = "grouped"
				}
				if _, err := store.DB().Exec("UPDATE apps SET status='hibernated',worker_isolation=? WHERE id=?", desired, app.ID); err != nil {
					t.Fatal(err)
				}
				// Mirror a settings PATCH that configured the desired mode while asleep.
				// Wake must restore recorded routing if frozen ownership survives.
				prx.SetPoolMode(app.Slug, config.IsolationGrouped, 4, 4)
				var coldBoots, resumes atomic.Int32
				watcher := lifecycle.New(lifecycle.Config{DefaultWorkerIsolation: defaultMode}, mgr, prx, store, func(context.Context, string, string, int) (*deploy.Result, error) {
					coldBoots.Add(1)
					return nil, fmt.Errorf("unexpected fixed cold boot")
				})
				watcher.SetResume(func(_ context.Context, slug, _ string, index int) (*deploy.Result, error) {
					resumes.Add(1)
					mode, ok := prx.ServingMode(slug)
					if !ok || mode != config.IsolationMultiplex {
						return nil, fmt.Errorf("resume routing mode=%s", mode)
					}
					ep, err := mgr.Resume(slug, index)
					if err != nil {
						return nil, err
					}
					if err := prx.RegisterReplica(slug, index, ep.URL, nil, dep.ID, app.ID); err != nil {
						return nil, err
					}
					return &deploy.Result{Index: index, PID: frozen.PID, Port: frozen.Port, EndpointURL: ep.URL, WorkerID: ep.WorkerID, Provider: ep.Provider}, nil
				})
				watcher.WakeTrigger(context.Background(), app.Slug)
				deadline := time.Now().Add(10 * time.Second)
				for {
					fresh, err := store.GetAppBySlug(app.Slug)
					if err != nil {
						t.Fatal(err)
					}
					if fresh.Status == "running" {
						break
					}
					if time.Now().After(deadline) {
						t.Fatalf("wake status=%s", fresh.Status)
					}
					time.Sleep(10 * time.Millisecond)
				}
				expected := "grouped"
				if suspended {
					expected = "multiplex"
					if resumes.Load() != 1 {
						t.Fatalf("resumes=%d", resumes.Load())
					}
				}
				if coldBoots.Load() != 0 {
					t.Fatalf("fixed cold boots=%d", coldBoots.Load())
				}
				if mode, err := store.GetDeploymentWorkerIsolation(dep.ID); err != nil || mode != expected {
					t.Fatalf("recorded mode=%s err=%v", mode, err)
				}
				if mode, ok := prx.ServingMode(app.Slug); !ok || string(mode) != expected {
					t.Fatalf("routing mode=%s present=%t", mode, ok)
				}
				rows, err := store.ListReplicas(app.ID)
				if err != nil {
					t.Fatal(err)
				}
				if suspended {
					if len(rows) != 1 || rows[0].Status != "running" {
						t.Fatalf("resumed rows=%+v", rows)
					}
				} else if len(rows) != 0 {
					t.Fatalf("cold grouped wake retained fixed rows=%+v", rows)
				}
			})
		}
	}
}
