package api_test

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
)

// TestAuditDateRangeFiltersTheListing drives the real handler, store and SQL.
// The event is stamped by the database clock, so the assertions are anchored to
// today's UTC date rather than to a fixed literal.
func TestAuditDateRangeFiltersTheListing(t *testing.T) {
	srv, store := newTestServer(t)
	if err := store.CreateUser(db.CreateUserParams{
		Username: "alice", PasswordHash: "h", Role: "admin",
	}); err != nil {
		t.Fatal(err)
	}
	u, _ := store.GetUserByUsername("alice")
	store.LogAuditEvent(db.AuditEventParams{
		UserID: &u.ID, Action: "deploy", ResourceType: "app", ResourceID: "myapp",
	})
	token, _ := auth.IssueJWT(u.ID, "alice", "admin", "test-secret")

	today := time.Now().UTC()
	day := func(offset int) string { return today.AddDate(0, 0, offset).Format("2006-01-02") }

	count := func(t *testing.T, query string) int {
		t.Helper()
		req := authedRequest(t, "GET", "/api/audit"+query, nil, token)
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("GET /api/audit%s = %d: %s", query, rec.Code, rec.Body.String())
		}
		var resp struct {
			Events []struct {
				Action string `json:"action"`
			} `json:"events"`
			Total int64 `json:"total"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if int64(len(resp.Events)) != resp.Total {
			t.Errorf("total %d disagrees with %d returned events; pagination would mislead",
				resp.Total, len(resp.Events))
		}
		return len(resp.Events)
	}

	// Positive control first: without a range the event is there, so a zero
	// below cannot be the fixture having failed to log anything.
	if n := count(t, ""); n != 1 {
		t.Fatalf("unfiltered listing returned %d events, want the 1 that was logged", n)
	}

	for _, tc := range []struct {
		name  string
		query string
		want  int
	}{
		// until=today is the assertion that pins the end-of-day bound: the
		// event was stamped during today, so a bound at today's midnight would
		// exclude it.
		{"until today keeps an event logged today", "?until=" + day(0), 1},
		{"since today keeps an event logged today", "?since=" + day(0), 1},
		{"a window around today keeps it", "?since=" + day(-1) + "&until=" + day(1), 1},
		{"a window entirely in the past excludes it", "?since=" + day(-30) + "&until=" + day(-1), 0},
		{"a window entirely in the future excludes it", "?since=" + day(1) + "&until=" + day(30), 0},
		{"an action filter still applies inside a window", "?action=stop&since=" + day(-1), 0},
		{"the matching action inside a window is kept", "?action=deploy&since=" + day(-1), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if n := count(t, tc.query); n != tc.want {
				t.Errorf("GET /api/audit%s returned %d events, want %d", tc.query, n, tc.want)
			}
		})
	}
}

// TestAuditDateRangeRejectsBadInput pins that a malformed or inverted range is
// a 400 rather than a silently unfiltered listing. Returning every event to
// someone who asked for a window is worse than returning an error.
func TestAuditDateRangeRejectsBadInput(t *testing.T) {
	srv, store := newTestServer(t)
	if err := store.CreateUser(db.CreateUserParams{
		Username: "alice", PasswordHash: "h", Role: "admin",
	}); err != nil {
		t.Fatal(err)
	}
	u, _ := store.GetUserByUsername("alice")
	store.LogAuditEvent(db.AuditEventParams{
		UserID: &u.ID, Action: "deploy", ResourceType: "app", ResourceID: "myapp",
	})
	token, _ := auth.IssueJWT(u.ID, "alice", "admin", "test-secret")

	for _, query := range []string{
		"?since=last-tuesday",
		"?until=soon",
		"?since=2026-9-1",
		"?since=2026-09-06&until=2026-09-01",
	} {
		t.Run(query, func(t *testing.T) {
			req := authedRequest(t, "GET", "/api/audit"+query, nil, token)
			rec := httptest.NewRecorder()
			srv.Router().ServeHTTP(rec, req)
			if rec.Code != 400 {
				t.Errorf("GET /api/audit%s = %d, want 400; body: %s", query, rec.Code, rec.Body.String())
			}
		})
	}
}
