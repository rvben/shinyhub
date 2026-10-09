package api

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/activation"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/lifecycle"
)

func TestGroupedRollingRestartUsesDistinctServingIdentity(t *testing.T) {
	srv, store, token, _, _, rt := buildManifestE2EServer(t, config.RuntimeConfig{})
	defer srv.Close()
	rt.serveTraffic = true
	srv.cfg.Server.DrainTimeout = 20 * time.Millisecond
	srv.SetAvailableMemoryForTest(func() (int, error) { return 4096, nil })
	app := createGenerationTestApp(t, store, "grouped-roll", 1, 16)
	if _, err := store.DB().Exec("UPDATE apps SET worker_isolation='grouped',worker_grouped_size=4,worker_max_workers=2 WHERE id=?", app.ID); err != nil {
		t.Fatal(err)
	}
	if rec := deployBareGeneration(t, srv, token, app.Slug, "print('v1')", false); rec.Code != 200 {
		t.Fatalf("deploy: %d %s", rec.Code, rec.Body.String())
	}
	srv.SetDeployRunForTest(func(p deploy.Params) (*deploy.PoolResult, error) {
		if p.Preparation != deploy.PrepareSkip {
			t.Fatal("same-bundle roll attempted preparation")
		}
		p.HealthCheck = func(string, time.Duration, http.RoundTripper) error { return nil }
		// Ordinary serving workers need the operation lock during candidate
		// readiness. A rolling activation must not hold it across this wait.
		p.HealthCheck = func(string, time.Duration, http.RoundTripper) error {
			acquired := make(chan struct{})
			go func() { release := srv.acquireDeployLock(app.Slug); release(); close(acquired) }()
			select {
			case <-acquired:
				waitCtx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
				defer cancel()
				if err := srv.WaitForAppOperations(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
					return errors.New("ownership shutdown lost readiness operation")
				}
				return nil
			case <-time.After(time.Second):
				return errors.New("serving spawn blocked behind activation readiness")
			}
		}
		return deploy.Run(p)
	})
	first, err := store.GetServingDeployment(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	previous := first.ID
	for i := 0; i < 2; i++ {
		app, err = store.GetAppByID(app.ID)
		if err != nil {
			t.Fatal(err)
		}
		current, err := store.GetServingDeployment(app.ID)
		if err != nil {
			t.Fatal(err)
		}
		a, err := store.EnqueueRollingRestart(app, current)
		if err != nil {
			t.Fatal(err)
		}
		a, err = store.ClaimNextScheduleActivation(time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if err = srv.Roll(context.Background(), a); err != nil {
			t.Fatal(err)
		}
		after, err := store.GetServingDeployment(app.ID)
		if err != nil {
			t.Fatal(err)
		}
		if after.ID == previous || after.Version != first.Version || after.BundleDir != first.BundleDir || after.ActivationToken == current.ActivationToken {
			t.Fatalf("invalid new serving identity: %+v", after)
		}
		// Retry after publication must complete the same execution, never roll again.
		if err = srv.Roll(context.Background(), a); err != nil {
			t.Fatalf("retry: %v", err)
		}
		retry, _ := store.GetServingDeployment(app.ID)
		if retry.ID != after.ID {
			t.Fatal("retry allocated another generation")
		}
		if err = store.FinishScheduleActivation(a.ID, "succeeded", "", time.Now(), false); err != nil {
			t.Fatal(err)
		}
		if err = srv.waitForPreviousGeneration(context.Background(), app, after.ID, true); err != nil {
			t.Fatal(err)
		}
		previous = after.ID
		history, err := store.ListRecentDeployments(app.ID, 2)
		if err != nil {
			t.Fatal(err)
		}
		updatedApp, err := store.GetAppByID(app.ID)
		if err != nil || updatedApp.ReleaseNumber != 1 {
			t.Fatalf("roll changed release number: %+v, %v", updatedApp, err)
		}
		if len(history) != 1 || history[0].ID != first.ID {
			t.Fatalf("roll changed release history: %+v", history)
		}
	}
}

func TestGroupedRestartFallbackKeepsAppLockDuringReadiness(t *testing.T) {
	srv, store, token, _, _, _ := buildManifestE2EServer(t, config.RuntimeConfig{})
	defer srv.Close()
	app := createGenerationTestApp(t, store, "grouped-restart-fallback", 1, 16)
	if _, err := store.DB().Exec("UPDATE apps SET worker_isolation='grouped',worker_grouped_size=4,worker_max_workers=2 WHERE id=?", app.ID); err != nil {
		t.Fatal(err)
	}
	if rec := deployBareGeneration(t, srv, token, app.Slug, "print('v1')", false); rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	app, _ = store.GetAppByID(app.ID)
	source, _ := store.GetServingDeployment(app.ID)
	a, err := store.EnqueueRollingRestart(app, source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec("UPDATE schedule_activations SET roll_fallback='restart' WHERE id=?", a.ID); err != nil {
		t.Fatal(err)
	}
	a, err = store.ClaimNextScheduleActivation(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	srv.SetAvailableMemoryForTest(func() (int, error) { return 0, nil })
	checked := false
	srv.SetDeployRunForTest(func(p deploy.Params) (*deploy.PoolResult, error) {
		p.HealthCheck = func(string, time.Duration, http.RoundTripper) error {
			checked = true
			if release, ok := srv.TryAcquireAppOperation(app.Slug); ok {
				release()
				return errors.New("fallback released app lock while serving pool was stopped")
			}
			return nil
		}
		return deploy.Run(p)
	})
	if err := srv.Roll(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if !checked {
		t.Fatal("candidate readiness was not checked")
	}
}

func TestGroupedActivationTerminalOutcomeClearsCandidate(t *testing.T) {
	for _, status := range []string{db.DeploymentPending, db.DeploymentFailed} {
		t.Run(status, func(t *testing.T) {
			srv, store, token, _, _, _ := buildManifestE2EServer(t, config.RuntimeConfig{})
			defer srv.Close()
			app := createGenerationTestApp(t, store, "abandoned-roll", 1, 16)
			_, err := store.DB().Exec("UPDATE apps SET worker_isolation='grouped',worker_grouped_size=4,worker_max_workers=2 WHERE id=?", app.ID)
			if err != nil {
				t.Fatal(err)
			}
			if rec := deployBareGeneration(t, srv, token, app.Slug, "print('v1')", false); rec.Code != 200 {
				t.Fatal(rec.Body.String())
			}
			app, _ = store.GetAppByID(app.ID)
			source, _ := store.GetServingDeployment(app.ID)
			a, err := store.EnqueueRollingRestart(app, source)
			if err != nil {
				t.Fatal(err)
			}
			a, err = store.ClaimNextScheduleActivation(time.Now())
			if err != nil {
				t.Fatal(err)
			}
			candidate, err := store.ActivationDeployment(a.ID, source)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.UpsertDeploymentReplica(db.UpsertDeploymentReplicaParams{AppID: app.ID, DeploymentID: candidate.ID, Index: 99, Status: "starting", Provider: "native"}); err != nil {
				t.Fatal(err)
			}
			if status == db.DeploymentFailed {
				if err := store.FailDeploymentWithReason(candidate.ID, "interrupted candidate"); err != nil {
					t.Fatal(err)
				}
			}
			if blocked, err := store.HasPendingDeployment(app.ID); err != nil || blocked {
				t.Fatalf("same-bundle candidate blocks serving admissions: %v %v", blocked, err)
			}
			if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: app.Slug, Status: "hibernated"}); err != nil {
				t.Fatal(err)
			}
			if err := srv.Roll(context.Background(), a); !errors.Is(err, activation.ErrNotNeeded) {
				t.Fatalf("outcome=%v", err)
			}
			after, err := store.GetDeploymentByID(candidate.ID)
			if err != nil || after.Status != db.DeploymentFailed {
				t.Fatalf("candidate=%+v err=%v", after, err)
			}

			rows, err := store.ListDeploymentReplicas(app.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range rows {
				if row.DeploymentID == candidate.ID {
					t.Fatal("terminal candidate runtime ledger survived cleanup")
				}
			}

		})
	}
}

func TestInterruptedServingActivationPreservesScheduleEdits(t *testing.T) {
	srv, store, token, _, _, _ := buildManifestE2EServer(t, config.RuntimeConfig{})
	defer srv.Close()
	app := createGenerationTestApp(t, store, "roll-recovery", 1, 16)
	if rec := deployBareGeneration(t, srv, token, app.Slug, "print('v1')", false); rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	id, err := store.CreateSchedule(db.CreateScheduleParams{AppID: app.ID, Name: "refresh", CronExpr: "0 5 * * *", CommandJSON: `["true"]`, Enabled: true, TimeoutSeconds: 60, OverlapPolicy: "skip", MissedPolicy: "skip", DeployTrigger: "never", OnSuccess: "none", RollFallback: "defer"})
	if err != nil {
		t.Fatal(err)
	}
	source, _ := store.GetServingDeployment(app.ID)
	a, err := store.EnqueueRollingRestart(app, source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ActivationDeployment(a.ID, source); err != nil {
		t.Fatal(err)
	}
	cron := "0 6 * * *"
	if err := store.UpdateSchedule(id, db.UpdateScheduleParams{CronExpr: &cron}); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.ReconcileInflightDeployments(store); err != nil {
		t.Fatal(err)
	}
	sc, err := store.GetSchedule(id)
	if err != nil || sc.CronExpr != cron {
		t.Fatalf("schedule edit lost: %+v %v", sc, err)
	}
}
