package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
)

// mkScheduleWithSuccess creates an app with one daily schedule that last
// succeeded `ago` before now, so the row is present in the freshness view.
func mkScheduleWithSuccess(t *testing.T, store *db.Store, slug string, ownerID int64, ago time.Duration) {
	t.Helper()
	if _, err := store.CreateApp(db.CreateAppParams{Slug: slug, Name: slug, OwnerID: ownerID}); err != nil {
		t.Fatalf("CreateApp %s: %v", slug, err)
	}
	app, err := store.GetAppBySlug(slug)
	if err != nil {
		t.Fatalf("GetAppBySlug %s: %v", slug, err)
	}
	schedID, err := store.CreateSchedule(db.CreateScheduleParams{
		AppID: app.ID, Name: "refresh", CronExpr: "0 6 * * *",
		CommandJSON: `["echo","hi"]`, Enabled: true, TimeoutSeconds: 3600,
		OverlapPolicy: "skip", MissedPolicy: "skip",
	})
	if err != nil {
		t.Fatalf("CreateSchedule %s: %v", slug, err)
	}
	at := time.Now().Add(-ago)
	runID, err := store.InsertScheduleRun(db.InsertScheduleRunParams{
		ScheduleID: schedID, Status: "running", Trigger: "schedule", StartedAt: at, LogPath: "x.log",
	})
	if err != nil {
		t.Fatalf("InsertScheduleRun %s: %v", slug, err)
	}
	exit := 0
	if err := store.FinishScheduleRun(db.FinishScheduleRunParams{
		RunID: runID, Status: "succeeded", ExitCode: &exit, FinishedAt: at.Add(time.Minute),
	}); err != nil {
		t.Fatalf("FinishScheduleRun %s: %v", slug, err)
	}
}

// statusSlugs performs the request and returns the slugs of the returned rows.
func statusSlugs(t *testing.T, rec *httptest.ResponseRecorder) []string {
	t.Helper()
	var env struct {
		Items []struct {
			Slug string `json:"slug"`
		} `json:"items"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	out := make([]string, 0, len(env.Items))
	for _, it := range env.Items {
		out = append(out, it.Slug)
	}
	return out
}

// An operator can already read every input this endpoint aggregates (schedule
// definitions and run metadata, via isPrivilegedAppOperator in canViewApp), so
// withholding the server-side freshness computation from them protects nothing
// and forces an N+1 client-side reimplementation of the staleness rule.
func TestFleetScheduleStatus_OperatorCanRead(t *testing.T) {
	srv, store := newFleetHealthServer(t)
	hash, _ := testHashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "ops", PasswordHash: hash, Role: "operator"})
	ops, _ := store.GetUserByUsername("ops")
	opsTok, _ := auth.IssueJWT(ops.ID, "ops", "operator", "test-secret")
	mkScheduleWithSuccess(t, store, "billing", ops.ID, 30*time.Hour)

	req := authedRequest(t, "GET", "/api/fleet/schedules/status", nil, opsTok)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("operator status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	// Two bounds: reaching the handler is not enough, it must return the row.
	if got := statusSlugs(t, rec); len(got) != 1 || got[0] != "billing" {
		t.Fatalf("operator rows = %v, want [billing]", got)
	}
}

// Negative control for the widening above: developer and viewer must stay out.
// Without this, "gate on any authenticated user" would pass the operator test.
func TestFleetScheduleStatus_BelowOperatorForbidden(t *testing.T) {
	srv, store := newFleetHealthServer(t)
	hash, _ := testHashPassword("pass")
	for _, role := range []string{"developer", "viewer"} {
		store.CreateUser(db.CreateUserParams{Username: role, PasswordHash: hash, Role: role})
		u, _ := store.GetUserByUsername(role)
		tok, _ := auth.IssueJWT(u.ID, role, role, "test-secret")

		req := authedRequest(t, "GET", "/api/fleet/schedules/status", nil, tok)
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s status = %d, want 403", role, rec.Code)
		}
	}
}

// A scoped deploy token must not see schedules for apps outside its allowlist.
// The scope check is independent of role: it beats admin everywhere else
// (canViewApp checks AppInScope before role), and this fleet-wide endpoint is
// no exception. requireAdmin never consulted the scope, so an admin-role scoped
// token could enumerate every app's schedule names through this endpoint.
func TestFleetScheduleStatus_ScopedTokenSeesOnlyItsApps(t *testing.T) {
	for _, role := range []string{"admin", "operator"} {
		t.Run(role, func(t *testing.T) {
			srv, store := newFleetHealthServer(t)
			ownerID, _ := mkUser(t, store, "owner", "developer")
			mkScheduleWithSuccess(t, store, "inscope", ownerID, 30*time.Hour)
			mkScheduleWithSuccess(t, store, "outscope", ownerID, 30*time.Hour)
			tok := scopedDeployToken(t, srv, store, role, []string{"inscope"})

			rec := doToken(t, srv, "GET", "/api/fleet/schedules/status", tok, nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
			}
			// Two bounds: the in-scope row must be present AND the
			// out-of-scope row absent. Presence alone passes on a handler
			// that returns everything.
			got := statusSlugs(t, rec)
			if len(got) != 1 || got[0] != "inscope" {
				t.Fatalf("scoped %s rows = %v, want exactly [inscope]", role, got)
			}
		})
	}
}
