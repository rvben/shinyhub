package db_test

import (
	"testing"

	"github.com/rvben/shinyhub/internal/schedulespec"
)

func TestDeploymentRepairBatchMatchesAdmission(t *testing.T) {
	for _, scenario := range []string{"healthy", "failed-without-barrier", "failed-barrier", "pending-barrier", "legacy", "later-success"} {
		t.Run(scenario, func(t *testing.T) {
			store := mustOpenDB(t)
			owner := mustCreateUser(t, store, "owner", "developer")
			app := mustCreateApp(t, store, "repair", owner.ID)
			promoteConvergenceDeployment(t, store, app.ID, "good", "sha256:good")
			if scenario != "healthy" {
				dep, err := store.BeginDeployment(app.ID, "attempt", t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				if scenario != "failed-without-barrier" && scenario != "legacy" {
					if err := store.MarkDeploymentProducerBarrierEntered(dep.ID); err != nil {
						t.Fatal(err)
					}
				}
				if scenario == "legacy" {
					if _, err := store.DB().Exec(`UPDATE deployments SET prior_schedule_snapshot_recorded = 0 WHERE id = ?`, dep.ID); err != nil {
						t.Fatal(err)
					}
				}
				if scenario != "pending-barrier" {
					if err := store.FailDeployment(dep.ID); err != nil {
						t.Fatal(err)
					}
				}
				if scenario == "later-success" {
					promoteConvergenceDeployment(t, store, app.ID, "repaired", "sha256:good")
				}
			}
			// Equal timestamps must still respect the deployment-ID fence.
			if _, err := store.DB().Exec(`UPDATE deployments SET created_at = '2026-10-08 08:00:00' WHERE app_id = ?`, app.ID); err != nil {
				t.Fatal(err)
			}
			batch, err := store.DeploymentRepairRequiredForApps([]int64{app.ID})
			if err != nil {
				t.Fatal(err)
			}
			scalar, err := store.AppDeploymentCompatibilityQuarantined(app.ID)
			if err != nil {
				t.Fatal(err)
			}
			want := scenario == "failed-barrier" || scenario == "pending-barrier" || scenario == "legacy"
			if batch[app.ID] != want || scalar != want {
				t.Fatalf("batch=%v scalar=%v want=%v", batch, scalar, want)
			}
			stored, err := store.GetAppBySlug(app.Slug)
			if err != nil || stored.DeploymentRepairRequired != nil {
				t.Fatalf("hot-path read computed API-only repair state: app=%+v err=%v", stored, err)
			}
		})
	}
}

func TestDeploymentRepairBatchExcludesPhysicalWriters(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "writer-only")
	dep := promoteConvergenceDeployment(t, store, appID, "good", "sha256:good")
	id := createConvergenceSchedule(t, store, appID, `["producer"]`, schedulespec.DeployTriggerBundleChange)
	runID := insertProducerRun(t, store, id, dep, `["producer"]`)
	for _, state := range []string{"running", "failed"} {
		if state == "failed" {
			finishProducerRun(t, store, runID, "failed")
		}
		if err := store.EnforceCompatibilityQuarantines(); err != nil {
			t.Fatal(err)
		}
		batch, err := store.DeploymentRepairRequiredForApps([]int64{appID})
		if err != nil || batch[appID] {
			t.Fatalf("writer %s creates deployment repair: %v, %v", state, batch, err)
		}
		quarantined, err := store.AppCompatibilityQuarantined(appID)
		if err != nil || !quarantined {
			t.Fatalf("writer %s lost aggregate fence: %v, %v", state, quarantined, err)
		}
	}
	if empty, err := store.DeploymentRepairRequiredForApps(nil); err != nil || len(empty) != 0 {
		t.Fatalf("empty batch=%v err=%v", empty, err)
	}
}
