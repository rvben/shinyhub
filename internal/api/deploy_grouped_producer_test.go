package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

// A grouped pool with a deploy-triggered producer deploys like a multiplex
// one on a native tier: the candidate producer runs behind the exclusive
// consumer fence, its run is recorded, and the pool is left to spawn workers
// on demand. Native elastic workers carry durable identities, so the deploy
// leaves no orphan-risk marker behind.
func TestDeploy_GroupedPoolAcceptsProducerSchedule(t *testing.T) {
	srv, store, token, fakeRuntime := newManifestE2EServerWithJobs(t)
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "groupedapp", Name: "groupedapp", OwnerID: 1, Access: "private"}); err != nil {
		t.Fatal(err)
	}
	app, _ := store.GetAppBySlug("groupedapp")

	body, ct := buildMultiFileBundleUpload(t, map[string]string{
		"app.py":        "print('x')",
		"refresh.py":    "print('producer')",
		"shinyhub.toml": groupedProducerManifest,
	})
	req := httptest.NewRequest("POST", "/api/apps/groupedapp/deploy", body)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("deploy status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		ScheduleConvergence []ScheduleConvergenceResult `json:"schedule_convergence"`
		Manifest            ManifestApplied             `json:"manifest"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode deploy response: %v", err)
	}
	if len(resp.ScheduleConvergence) != 1 || !resp.ScheduleConvergence[0].Prestart || resp.ScheduleConvergence[0].RunID == nil {
		t.Fatalf("convergence = %+v, want a prestart run", resp.ScheduleConvergence)
	}
	if len(resp.Manifest.Schedules) != 1 || resp.Manifest.Schedules[0].Action != "created" {
		t.Fatalf("manifest schedule action = %+v, want created", resp.Manifest.Schedules)
	}
	waitForDeployRunCount(t, store, scheduleIDByName(t, store, app.ID, "refresh-data"), 1)

	fakeRuntime.mu.Lock()
	events := append([]string(nil), fakeRuntime.events...)
	fakeRuntime.mu.Unlock()
	producers := 0
	for _, e := range events {
		if strings.HasPrefix(e, "producer:") {
			producers++
		}
	}
	if producers != 1 {
		t.Fatalf("candidate producer runs = %d in events %v, want 1", producers, events)
	}

	app, err := store.GetAppBySlug("groupedapp")
	if err != nil {
		t.Fatal(err)
	}
	if app.WorkerIsolation != "grouped" {
		t.Fatalf("worker_isolation = %q, want grouped kept", app.WorkerIsolation)
	}
	if risk, err := store.AppElasticOrphanRisk(app.ID); err != nil || risk {
		t.Fatalf("grouped producer deploy left an orphan-risk marker: risk=%v err=%v", risk, err)
	}
}
