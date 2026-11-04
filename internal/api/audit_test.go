package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
)

func TestAuditListIncludesUsername(t *testing.T) {
	srv, store := newTestServer(t)

	// Create a user and log an event attributed to them.
	if err := store.CreateUser(db.CreateUserParams{
		Username: "alice", PasswordHash: "h", Role: "admin",
	}); err != nil {
		t.Fatal(err)
	}
	u, _ := store.GetUserByUsername("alice")
	store.LogAuditEvent(db.AuditEventParams{
		UserID:       &u.ID,
		Action:       "deploy",
		ResourceType: "app",
		ResourceID:   "myapp",
	})

	token, _ := auth.IssueJWT(u.ID, "alice", "admin", "test-secret")
	req := authedRequest(t, "GET", "/api/audit", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Events []struct {
			Action   string  `json:"action"`
			Username *string `json:"username"`
		} `json:"events"`
		Total   int64 `json:"total"`
		HasMore bool  `json:"has_more"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(resp.Events))
	}
	if resp.Events[0].Username == nil || *resp.Events[0].Username != "alice" {
		t.Errorf("expected username=alice in response, got %v", resp.Events[0].Username)
	}
	if resp.Total != 1 {
		t.Errorf("expected total=1, got %d", resp.Total)
	}
	if resp.HasMore {
		t.Errorf("expected has_more=false for single-page result, got true")
	}
}

func TestAuditListAnonymousEventHasNoUsername(t *testing.T) {
	srv, store := newTestServer(t)

	// Admin user for authentication — not the actor of the event.
	if err := store.CreateUser(db.CreateUserParams{
		Username: "admin", PasswordHash: "h", Role: "admin",
	}); err != nil {
		t.Fatal(err)
	}
	admin, _ := store.GetUserByUsername("admin")

	// Log an anonymous event (no UserID).
	store.LogAuditEvent(db.AuditEventParams{
		Action:       "login_failed",
		ResourceType: "user",
		ResourceID:   "unknown",
	})

	token, _ := auth.IssueJWT(admin.ID, "admin", "admin", "test-secret")
	req := authedRequest(t, "GET", "/api/audit", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Events []struct {
			Username *string `json:"username"`
		} `json:"events"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(resp.Events))
	}
	if resp.Events[0].Username != nil {
		t.Errorf("expected no username field for anonymous event, got %v", *resp.Events[0].Username)
	}
}

// SCH-4: docs/schedules.md advertises GET /api/audit?action=<value>. The filter
// must restrict results to the named action (and the total/has_more envelope
// must reflect the filtered count, not the table total).
func TestListAuditEvents_ActionFilter(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := testHashPassword("pass")
	if err := store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	u, _ := store.GetUserByUsername("admin")
	token, _ := auth.IssueJWT(u.ID, "admin", "admin", "test-secret")

	store.LogAuditEvent(db.AuditEventParams{UserID: &u.ID, Action: "deploy", ResourceType: "app", ResourceID: "a"})
	store.LogAuditEvent(db.AuditEventParams{UserID: &u.ID, Action: "schedule_run_failed", ResourceType: "schedule", ResourceID: "b"})
	store.LogAuditEvent(db.AuditEventParams{UserID: &u.ID, Action: "deploy", ResourceType: "app", ResourceID: "c"})

	req := authedRequest(t, "GET", "/api/audit?action=schedule_run_failed", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Events  []map[string]any `json:"events"`
		Total   int64            `json:"total"`
		HasMore bool             `json:"has_more"`
	}
	json.NewDecoder(rec.Body).Decode(&resp)
	if len(resp.Events) != 1 {
		t.Fatalf("expected 1 filtered event, got %d", len(resp.Events))
	}
	if resp.Events[0]["action"] != "schedule_run_failed" {
		t.Errorf("expected action=schedule_run_failed, got %v", resp.Events[0]["action"])
	}
	if resp.Total != 1 {
		t.Errorf("filtered total must count only matching rows, got %d", resp.Total)
	}
	if resp.HasMore {
		t.Errorf("has_more must be false when the filtered set fits in one page")
	}
}

func TestListAuditEvents_ExactEventAndRunFilters(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := testHashPassword("pass")
	if err := store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	u, _ := store.GetUserByUsername("admin")
	token, _ := auth.IssueJWT(u.ID, "admin", "admin", "test-secret")
	for _, runID := range []string{"run-one", "run-two"} {
		if _, created, err := store.CreateFleetRun(db.CreateFleetRunParams{
			ID: runID, FleetID: "audit-filter-test", Kind: "fleet_apply", UserID: &u.ID,
		}); err != nil || !created {
			t.Fatalf("create fleet run %s: created=%v err=%v", runID, created, err)
		}
	}

	store.LogAuditEvent(db.AuditEventParams{UserID: &u.ID, Action: "deploy", ResourceType: "app", ResourceID: "a", RunID: "run-one"})
	store.LogAuditEvent(db.AuditEventParams{UserID: &u.ID, Action: "env.set", ResourceType: "app", ResourceID: "b", RunID: "run-two"})
	store.LogAuditEvent(db.AuditEventParams{UserID: &u.ID, Action: "rollback", ResourceType: "app", ResourceID: "c", RunID: "run-one"})

	events, err := store.ListAuditEvents("", 10, 0)
	if err != nil || len(events) != 3 {
		t.Fatalf("seed audit events: n=%d err=%v", len(events), err)
	}
	targetID := events[1].ID

	for _, tc := range []struct {
		name      string
		query     string
		wantCount int
		wantID    int64
		wantRun   string
	}{
		{name: "event", query: "/api/audit?event=" + fmt.Sprint(targetID), wantCount: 1, wantID: targetID},
		{name: "run", query: "/api/audit?run=run-one", wantCount: 2, wantRun: "run-one"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := authedRequest(t, http.MethodGet, tc.query, nil, token)
			rec := httptest.NewRecorder()
			srv.Router().ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
			}
			var resp struct {
				Events []db.AuditEvent `json:"events"`
				Total  int64           `json:"total"`
			}
			if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
				t.Fatal(err)
			}
			if len(resp.Events) != tc.wantCount || resp.Total != int64(tc.wantCount) {
				t.Fatalf("events=%d total=%d, want %d", len(resp.Events), resp.Total, tc.wantCount)
			}
			for _, event := range resp.Events {
				if tc.wantID != 0 && event.ID != tc.wantID {
					t.Errorf("event id=%d, want %d", event.ID, tc.wantID)
				}
				if tc.wantRun != "" && event.RunID != tc.wantRun {
					t.Errorf("run=%q, want %q", event.RunID, tc.wantRun)
				}
			}
		})
	}

	req := authedRequest(t, http.MethodGet, "/api/audit?event=not-a-number", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid event filter status=%d, want 400", rec.Code)
	}
}

func TestListAuditEvents_AdminOnly(t *testing.T) {
	srv, store := newTestServer(t)
	token, _ := seedUserAndJWT(t, store, "dev", "developer")
	req := authedRequest(t, "GET", "/api/audit", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 for non-admin, got %d", rec.Code)
	}
}

func TestListAuditEvents_Admin(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := testHashPassword("pass")
	if err := store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	u, _ := store.GetUserByUsername("admin")
	token, _ := auth.IssueJWT(u.ID, "admin", "admin", "test-secret")

	store.LogAuditEvent(db.AuditEventParams{
		UserID: &u.ID, Action: "deploy", ResourceType: "app", ResourceID: "test-app",
	})

	req := authedRequest(t, "GET", "/api/audit", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Events []map[string]any `json:"events"`
	}
	json.NewDecoder(rec.Body).Decode(&resp)
	if len(resp.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(resp.Events))
	}
	if resp.Events[0]["action"] != "deploy" {
		t.Errorf("expected action=deploy, got %v", resp.Events[0]["action"])
	}
}

// TestListAuditEvents_LimitIsClamped proves the clamp reaches the real HTTP
// path, not just the parsing helper in isolation: an admin asking for an
// enormous page size still gets back at most the server's maximum page.
func TestListAuditEvents_LimitIsClamped(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := testHashPassword("pass")
	if err := store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	u, _ := store.GetUserByUsername("admin")
	token, _ := auth.IssueJWT(u.ID, "admin", "admin", "test-secret")

	const seeded = 505
	for i := 0; i < seeded; i++ {
		store.LogAuditEvent(db.AuditEventParams{
			UserID: &u.ID, Action: "deploy", ResourceType: "app", ResourceID: "test-app",
		})
	}

	req := authedRequest(t, "GET", "/api/audit?limit=999999999", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Events []map[string]any `json:"events"`
		Total  int64            `json:"total"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Total != seeded {
		t.Fatalf("total = %d, want %d (the clamp must not affect the reported total)", resp.Total, seeded)
	}
	if len(resp.Events) >= seeded {
		t.Fatalf("an excessive ?limit= returned %d of %d rows in one response, want it capped well below the full set", len(resp.Events), seeded)
	}
}

// Search and counts share the same predicate, including escaped SQL wildcard
// characters. Filtering must cover the history, not only the first UI page.
func TestAuditSearchAcrossActorsResourcesAndCredentials(t *testing.T) {
	srv, store := newTestServer(t)
	if err := store.CreateUser(db.CreateUserParams{Username: "audit-operator", PasswordHash: "h", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	actor, _ := store.GetUserByUsername("audit-operator")
	for _, p := range []db.AuditEventParams{
		{UserID: &actor.ID, Action: "update_app", ResourceType: "app", ResourceID: "forecast", Detail: `{"memory":{"old":"256 MiB","new":"512 MiB"}}`},
		{Action: "deploy", ResourceType: "app", ResourceID: "forecast"},
		{Action: "deploy", ResourceType: "app", ResourceID: "literal_%!"},
		{Action: "deploy", ResourceType: "app", ResourceID: "literal-abc", CredentialName: "Release pipeline"},
		{Action: "login_failed", ResourceType: "user", ResourceID: "visitor"},
	} {
		store.LogAuditEvent(p)
	}
	token, _ := auth.IssueJWT(actor.ID, actor.Username, "admin", "test-secret")
	for _, tc := range []struct {
		query  string
		want   int
		action string
	}{
		{"forecast", 2, ""}, {"update app", 1, ""}, {"AUDIT-OPERATOR", 1, ""}, {"release pipeline", 1, ""}, {"_%!", 1, ""},
		{"not-found", 0, ""}, {"forecast", 1, "deploy"}, {"' OR 1=1 --", 0, ""},
	} {
		t.Run(tc.query+tc.action, func(t *testing.T) {
			path := "/api/audit?limit=1&q=" + url.QueryEscape(tc.query) + "&action=" + url.QueryEscape(tc.action)
			rec := httptest.NewRecorder()
			srv.Router().ServeHTTP(rec, authedRequest(t, "GET", path, nil, token))
			if rec.Code != 200 {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			if rec.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("audit responses must not be cached")
			}
			var result struct {
				Events  []db.AuditEvent `json:"events"`
				Total   int             `json:"total"`
				HasMore bool            `json:"has_more"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.Total != tc.want || result.HasMore != (tc.want > 1) {
				t.Fatalf("total/has_more = %d/%v, want %d/%v", result.Total, result.HasMore, tc.want, tc.want > 1)
			}
			if tc.want > 1 {
				next := httptest.NewRecorder()
				srv.Router().ServeHTTP(next, authedRequest(t, "GET", path+"&offset=1", nil, token))
				var second struct {
					Events []db.AuditEvent `json:"events"`
				}
				_ = json.Unmarshal(next.Body.Bytes(), &second)
				if len(second.Events) != 1 || second.Events[0].ID == result.Events[0].ID {
					t.Fatal("search pagination duplicated or omitted an event")
				}
			}
		})
	}
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, authedRequest(t, "GET", "/api/audit?q="+strings.Repeat("x", 201), nil, token))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("long query status = %d", rec.Code)
	}
}

func TestAuditRecordsAppMetadataBeforeAndAfter(t *testing.T) {
	srv, store := newTestServer(t)
	if err := store.CreateUser(db.CreateUserParams{Username: "metadata-admin", PasswordHash: "h", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	actor, _ := store.GetUserByUsername("metadata-admin")
	token, _ := auth.IssueJWT(actor.ID, actor.Username, "admin", "test-secret")
	for _, request := range []struct {
		method, path, body string
		status             int
	}{
		{"POST", "/api/apps", `{"slug":"audit-metadata","name":"Original name"}`, 201},
		{"PATCH", "/api/apps/audit-metadata", `{"description":"Original description"}`, 200},
		{"PATCH", "/api/apps/audit-metadata", `{"name":"Updated name","description":"Updated description"}`, 200},
	} {
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, authedRequest(t, request.method, request.path, []byte(request.body), token))
		if rec.Code != request.status {
			t.Fatalf("%s status %d: %s", request.method, rec.Code, rec.Body.String())
		}
	}
	rows, err := store.ListAuditEventsFiltered(db.AuditEventFilter{Action: "update_app"}, 1, 0)
	if err != nil || len(rows) != 1 {
		t.Fatalf("audit rows %d, %v", len(rows), err)
	}
	var detail map[string]struct {
		Old string `json:"old"`
		New string `json:"new"`
	}
	if err := json.Unmarshal([]byte(rows[0].Detail), &detail); err != nil {
		t.Fatal(err)
	}
	if detail["name"].Old != "Original name" || detail["name"].New != "Updated name" || detail["description"].Old != "Original description" || detail["description"].New != "Updated description" {
		t.Fatalf("incorrect metadata attribution: %#v", detail)
	}
}
