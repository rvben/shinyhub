package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
)

// scheduleListItem mirrors the freshness half of the schedule list payload.
type scheduleListItem struct {
	Name            string  `json:"name"`
	LastRunAt       *string `json:"last_run_at"`
	LastRunStatus   *string `json:"last_run_status"`
	LastSuccessAt   *string `json:"last_success_at"`
	LastSuccessAgeS *int64  `json:"last_success_age_s"`
	Stale           *bool   `json:"stale"`
}

func listSchedules(t *testing.T, srv interface{ Router() http.Handler }, slug, tok string) []scheduleListItem {
	t.Helper()
	req := authedRequest(t, "GET", "/api/apps/"+slug+"/schedules", nil, tok)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list schedules = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var env struct {
		Items []scheduleListItem `json:"items"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return env.Items
}

// The per-app schedule list must carry the same freshness the fleet-wide
// status endpoint computes. Without it a caller with only per-app access has
// to fetch run history per schedule (N+1) and reimplement the cron-aware
// staleness rule client-side, which drifts from the server's definition.
func TestListSchedules_CarriesFreshness(t *testing.T) {
	srv, store := newTestServer(t)
	ownerID, tok := mkUser(t, store, "owner", "developer")
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "billing", Name: "billing", OwnerID: ownerID}); err != nil {
		t.Fatal(err)
	}
	app, _ := store.GetAppBySlug("billing")

	// A daily schedule whose last success was 30h ago is overdue -> stale.
	staleID, err := store.CreateSchedule(db.CreateScheduleParams{
		AppID: app.ID, Name: "overdue", CronExpr: "0 6 * * *",
		CommandJSON: `["echo","hi"]`, Enabled: true, TimeoutSeconds: 3600,
		OverlapPolicy: "skip", MissedPolicy: "skip",
	})
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-30 * time.Hour)
	runID, err := store.InsertScheduleRun(db.InsertScheduleRunParams{
		ScheduleID: staleID, Status: "running", Trigger: "schedule", StartedAt: old, LogPath: "x.log",
	})
	if err != nil {
		t.Fatal(err)
	}
	exit := 0
	if err := store.FinishScheduleRun(db.FinishScheduleRunParams{
		RunID: runID, Status: "succeeded", ExitCode: &exit, FinishedAt: old.Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	// A never-run schedule: its freshness fields must be null, not zero values.
	if _, err := store.CreateSchedule(db.CreateScheduleParams{
		AppID: app.ID, Name: "never", CronExpr: "0 6 * * *",
		CommandJSON: `["echo","hi"]`, Enabled: true, TimeoutSeconds: 3600,
		OverlapPolicy: "skip", MissedPolicy: "skip",
	}); err != nil {
		t.Fatal(err)
	}

	byName := map[string]scheduleListItem{}
	for _, it := range listSchedules(t, srv, "billing", tok) {
		byName[it.Name] = it
	}
	if len(byName) != 2 {
		t.Fatalf("got %d schedules, want 2: %+v", len(byName), byName)
	}

	got := byName["overdue"]
	if got.Stale == nil || !*got.Stale {
		t.Errorf("overdue.stale = %v, want true (daily schedule, last success 30h ago)", got.Stale)
	}
	if got.LastRunStatus == nil || *got.LastRunStatus != "succeeded" {
		t.Errorf("overdue.last_run_status = %v, want succeeded", got.LastRunStatus)
	}
	if got.LastRunAt == nil || got.LastSuccessAt == nil {
		t.Errorf("overdue last_run_at/last_success_at = %v/%v, want both set", got.LastRunAt, got.LastSuccessAt)
	}
	if got.LastSuccessAgeS == nil || *got.LastSuccessAgeS < 100000 {
		t.Errorf("overdue.last_success_age_s = %v, want ~108000 (30h)", got.LastSuccessAgeS)
	}

	// Second bound: a never-run schedule must NOT report a fabricated run.
	// An implementation that zero-fills would pass every assertion above.
	never := byName["never"]
	if never.LastRunAt != nil || never.LastSuccessAt != nil || never.LastSuccessAgeS != nil {
		t.Errorf("never-run schedule must report null run fields, got %+v", never)
	}
	if never.LastRunStatus != nil && *never.LastRunStatus != "" {
		t.Errorf("never-run last_run_status = %v, want null/empty", never.LastRunStatus)
	}
	// stale is still computed (a never-run schedule can be overdue), so it
	// must be present rather than omitted.
	if never.Stale == nil {
		t.Errorf("never-run schedule must still carry a computed stale flag")
	}
}

// Freshness is a property of a stored schedule's run history, so a create
// response - which by definition has none - must omit the fields rather than
// claim last_run_at is null. Otherwise "never run" and "not reported here"
// become the same value on the wire.
func TestCreateSchedule_OmitsFreshness(t *testing.T) {
	srv, store := newTestServer(t)
	ownerID, tok := mkUser(t, store, "owner", "developer")
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "billing", Name: "billing", OwnerID: ownerID}); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"name":"nightly","cron_expr":"0 6 * * *","command":["echo","hi"],` +
		`"timeout_seconds":60,"overlap_policy":"skip","missed_policy":"skip"}`)
	req := authedRequest(t, "POST", "/api/apps/billing/schedules", body, tok)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		t.Fatalf("create = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	var raw map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, k := range []string{"last_run_at", "last_run_status", "last_success_at", "last_success_age_s", "stale"} {
		if _, present := raw[k]; present {
			t.Errorf("create response carries %q; freshness belongs only where it is computed", k)
		}
	}
}
