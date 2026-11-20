package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/schedulespec"
)

// A writer's running/failed state must not become a deployment fence simply
// because a concurrent deployment is refused before any candidate runs.
func TestDeployRefusalDoesNotInheritScheduleWriterFence(t *testing.T) {
	for _, writerStatus := range []string{"running", "failed"} {
		t.Run(writerStatus, func(t *testing.T) {
			srv, store, token, _ := newManifestE2EServerWithJobs(t)
			_, err := store.CreateApp(db.CreateAppParams{Slug: "writer-fence", Name: "writer-fence", OwnerID: 1, Access: "private"})
			if err != nil {
				t.Fatal(err)
			}
			app, _ := store.GetAppBySlug("writer-fence")
			memory := 128
			if _, err := store.PatchAppSettings(db.PatchAppSettingsParams{Slug: app.Slug, SetMemoryLimitMB: true, MemoryLimitMB: &memory}); err != nil {
				t.Fatal(err)
			}
			upload := func(allowDowntime bool) *httptest.ResponseRecorder {
				t.Helper()
				body, ct := buildMultiFileBundleUpload(t, map[string]string{"app.py": "print('consumer')", "producer.py": "print('producer')"})
				req := httptest.NewRequest(http.MethodPost, "/api/apps/writer-fence/deploy", body)
				req.Header.Set("Content-Type", ct)
				req.Header.Set("Authorization", "Bearer "+token)
				if allowDowntime {
					req.Header.Set("X-ShinyHub-Allow-Downtime", "1")
				}
				rec := httptest.NewRecorder()
				srv.Router().ServeHTTP(rec, req)
				return rec
			}
			if rec := upload(false); rec.Code != http.StatusOK {
				t.Fatalf("baseline: %d %s", rec.Code, rec.Body.String())
			}
			serving, _ := store.GetServingDeployment(app.ID)
			scheduleID, err := store.CreateSchedule(db.CreateScheduleParams{AppID: app.ID, Name: "refresh", CronExpr: "0 5 * * *", CommandJSON: `["python","producer.py"]`, Enabled: true, TimeoutSeconds: 60, OverlapPolicy: "skip", MissedPolicy: "skip", DeployTrigger: schedulespec.DeployTriggerBundleChange})
			if err != nil {
				t.Fatal(err)
			}
			runID, err := store.InsertScheduleRun(db.InsertScheduleRunParams{ScheduleID: scheduleID, Status: "running", Trigger: "schedule", StartedAt: time.Now(), PublishesData: true, DeploymentID: &serving.ID, AppVersion: serving.Version, ContentDigest: serving.ContentDigest})
			if err != nil {
				t.Fatal(err)
			}
			finish := func(status string) {
				t.Helper()
				exit := 0
				if status == "failed" {
					exit = 1
				}
				if _, err := store.CompleteScheduleRunAndEnqueueActivation(db.CompleteScheduleRunParams{RunID: runID, Status: status, ExitCode: &exit, FinishedAt: time.Now(), DataWriteAttempted: true}); err != nil {
					t.Fatal(err)
				}
			}
			if writerStatus == "failed" {
				finish("failed")
			}
			for attempt := 0; attempt < 2; attempt++ {
				rec := upload(false)
				if rec.Code != http.StatusConflict {
					t.Fatalf("refusal: %d %s", rec.Code, rec.Body.String())
				}
				if !strings.Contains(rec.Body.String(), "its shared data is not known to be safe") || strings.Contains(rec.Body.String(), "in use") {
					t.Fatalf("refusal does not name the schedule writer as the obstacle: %s", rec.Body.String())
				}
				deps, err := store.ListDeploymentsBySlug(app.Slug)
				if err != nil || len(deps) != attempt+2 {
					t.Fatalf("history: %+v %v", deps, err)
				}
				var barrier int
				if err := store.DB().QueryRow(`SELECT producer_barrier_entered FROM deployments WHERE id=?`, deps[0].ID).Scan(&barrier); err != nil {
					t.Fatal(err)
				}
				if barrier != 0 {
					t.Fatal("untouched refusal inherited a physical-writer fence")
				}
				if blocked, err := store.AppDeploymentCompatibilityQuarantined(app.ID); err != nil || blocked {
					t.Fatalf("deployment fence: %v %v", blocked, err)
				}
				if current, _ := store.GetServingDeployment(app.ID); current.ID != serving.ID {
					t.Fatal("refusal changed serving deployment")
				}
			}
			if writerStatus == "running" {
				finish("succeeded")
				if blocked, err := store.AppCompatibilityQuarantined(app.ID); err != nil || blocked {
					t.Fatalf("completed writer left fence: %v %v", blocked, err)
				}
				if err := store.EnforceCompatibilityQuarantines(); err != nil {
					t.Fatal(err)
				}
				fresh, _ := store.GetAppByID(app.ID)
				if fresh.Status != "running" {
					t.Fatalf("restart enforcement changed healthy status: %s", fresh.Status)
				}
			} else {
				if blocked, err := store.AppDataCompatibilityQuarantined(app.ID); err != nil || !blocked {
					t.Fatalf("lost real writer fence: %v %v", blocked, err)
				}
			}
			if rec := upload(true); rec.Code != http.StatusOK {
				t.Fatalf("corrective deploy: %d %s", rec.Code, rec.Body.String())
			}
			if blocked, err := store.AppCompatibilityQuarantined(app.ID); err != nil || blocked {
				t.Fatalf("successful correction left fence: %v %v", blocked, err)
			}
			if rec := upload(false); rec.Code != http.StatusOK {
				t.Fatalf("unchanged no-downtime retry: %d %s", rec.Code, rec.Body.String())
			}

		})
	}
}

func TestDeploymentRepairGuidanceNamesOriginalFence(t *testing.T) {
	srv, store, _, _ := newManifestE2EServerWithJobs(t)
	_, err := store.CreateApp(db.CreateAppParams{Slug: "repair-guidance", Name: "repair-guidance", OwnerID: 1, Access: "private"})
	if err != nil {
		t.Fatal(err)
	}
	app, _ := store.GetAppBySlug("repair-guidance")
	first, _ := store.BeginDeployment(app.ID, "original", t.TempDir())
	if err := store.MarkDeploymentProducerBarrierEntered(first.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.FailDeploymentWithReason(first.ID, "producer replaced data; consumer crashed"); err != nil {
		t.Fatal(err)
	}
	later, _ := store.BeginDeployment(app.ID, "later-refusal", t.TempDir())
	if err := store.MarkDeploymentProducerBarrierEntered(later.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.FailDeploymentWithReason(later.ID, "no-downtime deployment deferred"); err != nil {
		t.Fatal(err)
	}
	blocker, err := store.AppDeploymentRepairBlocker(app.ID)
	if err != nil || blocker == nil || blocker.ID != first.ID {
		t.Fatalf("blocker=%+v err=%v", blocker, err)
	}
	guardErr := srv.guardCompatibilityQuarantine(app.ID, "start")
	if guardErr == nil {
		t.Fatal("activation admitted an unresolved deployment fence")
	}
	plan, err := srv.planPrestartSchedules(app, nil, "sha256:target")
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range []string{guardErr.Error(), plan.downtimeReason(nil, app.Slug)} {
		for _, want := range []string{"original", "producer replaced data; consumer crashed", blocker.CreatedAt.UTC().Format(time.RFC3339), "--allow-downtime", "successful producer runs alone"} {
			if !strings.Contains(message, want) {
				t.Errorf("missing %q: %s", want, message)
			}
		}
		if strings.Contains(message, "later-refusal") {
			t.Errorf("named latest attempt instead of cause: %s", message)
		}
	}
}

func TestRollbackValidationDoesNotInheritRunningWriterFence(t *testing.T) {
	srv, store, token, _ := newManifestE2EServerWithJobs(t)
	_, err := store.CreateApp(db.CreateAppParams{Slug: "rollback-writer", Name: "rollback-writer", OwnerID: 1, Access: "private"})
	if err != nil {
		t.Fatal(err)
	}
	app, _ := store.GetAppBySlug("rollback-writer")
	var targetID int64
	for i := 0; i < 2; i++ {
		body, ct := buildMultiFileBundleUpload(t, map[string]string{"app.py": fmt.Sprintf("print(%d)", i)})
		req := httptest.NewRequest(http.MethodPost, "/api/apps/rollback-writer/deploy", body)
		req.Header.Set("Content-Type", ct)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-ShinyHub-Allow-Downtime", "1")
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("baseline: %d %s", rec.Code, rec.Body.String())
		}
		if i == 0 {
			dep, _ := store.GetServingDeployment(app.ID)
			targetID = dep.ID
		}
	}
	scheduleID, err := store.CreateSchedule(db.CreateScheduleParams{AppID: app.ID, Name: "refresh", CronExpr: "0 5 * * *", CommandJSON: `["producer"]`, Enabled: true, TimeoutSeconds: 60, OverlapPolicy: "skip", MissedPolicy: "skip"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.InsertScheduleRun(db.InsertScheduleRunParams{ScheduleID: scheduleID, Status: "running", Trigger: "schedule", StartedAt: time.Now(), PublishesData: true}); err != nil {
		t.Fatal(err)
	}
	// The target's immutable schedule snapshot is required before any consumers
	// are stopped. Make it unavailable to exercise an untouched rollback refusal.
	if _, err := store.DB().Exec(`DELETE FROM deployment_schedule_snapshots WHERE deployment_id=?`, targetID); err != nil {
		t.Fatal(err)
	}
	// A prepared target can have zero schedules, so explicitly mark the snapshot
	// absent rather than relying on the deleted rows to imply absence.
	if _, err := store.DB().Exec(`UPDATE deployments SET schedule_snapshot_recorded=0 WHERE id=?`, targetID); err != nil {
		t.Fatal(err)
	}
	srv.SetDeployRunForTest(func(deploy.Params) (*deploy.PoolResult, error) {
		t.Fatal("refused rollback reached runtime")
		return nil, nil
	})
	req := httptest.NewRequest(http.MethodPost, "/api/apps/rollback-writer/rollback", strings.NewReader(fmt.Sprintf(`{"deployment_id":%d}`, targetID)))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("rollback: %d %s", rec.Code, rec.Body.String())
	}
	var barrier int
	if err := store.DB().QueryRow(`SELECT producer_barrier_entered FROM deployments WHERE app_id=? ORDER BY id DESC LIMIT 1`, app.ID).Scan(&barrier); err != nil {
		t.Fatal(err)
	}
	if barrier != 0 {
		t.Fatal("rollback validation created a deployment fence from a running writer")
	}
	if blocked, err := store.AppDeploymentCompatibilityQuarantined(app.ID); err != nil || blocked {
		t.Fatalf("rollback deployment fence: %v %v", blocked, err)
	}
}
