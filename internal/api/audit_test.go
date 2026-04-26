package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	hash, _ := auth.HashPassword("pass")
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
