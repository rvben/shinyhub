package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/schedulespec"
)

func TestDeployPreflightRepairProducerProjectionIsReadOnly(t *testing.T) {
	for _, tc := range []struct {
		name     string
		existing bool
		manifest string
		valid    bool
	}{
		{"no producer", false, "", false},
		{"inherit omitted producer", true, "[app]\nname = 'target'\n", true},
		{"disabled override", true, "[[schedule]]\nname='refresh'\ncron='0 5 * * *'\ncmd='python producer.py'\ndisabled=true\ndeploy_trigger='bundle_change'\n", false},
		{"never override", true, "[[schedule]]\nname='refresh'\ncron='0 5 * * *'\ncmd='python producer.py'\ndeploy_trigger='never'\n", false},
		{"new enabled producer", false, "[[schedule]]\nname='refresh'\ncron='0 5 * * *'\ncmd='python producer.py'\ndeploy_trigger='bundle_change'\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, store, token, _ := newManifestE2EServerWithJobs(t)
			if _, err := store.CreateApp(db.CreateAppParams{Slug: "repair", Name: "repair", OwnerID: 1, Access: "private"}); err != nil {
				t.Fatal(err)
			}
			app, err := store.GetAppBySlug("repair")
			if err != nil {
				t.Fatal(err)
			}
			dep, err := store.BeginDeployment(app.ID, "failed", t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err := store.MarkDeploymentProducerBarrierEntered(dep.ID); err != nil {
				t.Fatal(err)
			}
			if err := store.QuarantineAndFailDeployment(dep.ID, "consumer failed"); err != nil {
				t.Fatal(err)
			}
			if tc.existing {
				if _, err := store.CreateSchedule(db.CreateScheduleParams{AppID: app.ID, Name: "refresh", CronExpr: "0 5 * * *", CommandJSON: `["python","producer.py"]`, Enabled: true, TimeoutSeconds: 60, OverlapPolicy: "skip", MissedPolicy: "skip", DeployTrigger: schedulespec.DeployTriggerBundleChange}); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := store.ListSchedulesByApp(app.ID)
			body, _ := json.Marshal(deployPreflightRequest{Manifest: tc.manifest, AppType: "python"})
			req := httptest.NewRequest(http.MethodPost, "/api/apps/repair/deploy-preflight", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+token)
			rec := httptest.NewRecorder()
			srv.Router().ServeHTTP(rec, req)
			var reply deployPreflightResponse
			if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &reply) != nil || reply.Valid != tc.valid {
				t.Fatalf("preflight=%d %s", rec.Code, rec.Body.String())
			}
			if !tc.valid && !strings.Contains(rec.Body.String(), "repair-blocked-no-producer") {
				t.Fatalf("missing recovery guidance: %s", rec.Body.String())
			}
			after, _ := store.ListSchedulesByApp(app.ID)
			deps, _ := store.ListDeploymentsBySlug("repair")
			if len(after) != len(before) || len(deps) != 1 || deps[0].Status != db.DeploymentFailed {
				t.Fatalf("preflight mutated schedules or deployments: before=%v after=%v deps=%v", before, after, deps)
			}
			for _, path := range []string{"/api/apps", "/api/apps/repair"} {
				req := httptest.NewRequest(http.MethodGet, path, nil)
				req.Header.Set("Authorization", "Bearer "+token)
				rec := httptest.NewRecorder()
				srv.Router().ServeHTTP(rec, req)
				if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"deployment_repair_required":true`) {
					t.Fatalf("%s omitted repair state: %d %s", path, rec.Code, rec.Body.String())
				}
			}
		})
	}
}
