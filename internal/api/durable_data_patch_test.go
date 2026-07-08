package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
)

// TestPatchApp_SetEphemeralDataAck sets ephemeral_data_ack via PATCH and reads
// it back from the DB, proving the guard's escape hatch is reachable over the
// API (and therefore the `apps set --ephemeral-data-ok` CLI flag).
func TestPatchApp_SetEphemeralDataAck(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := testHashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "bob", PasswordHash: hash, Role: "admin"})
	u, _ := store.GetUserByUsername("bob")
	token, _ := auth.IssueJWT(u.ID, "bob", "admin", "test-secret")
	store.CreateApp(db.CreateAppParams{Slug: "myapp", Name: "My App", OwnerID: u.ID})

	body, _ := json.Marshal(map[string]any{"ephemeral_data_ack": true})
	req := authedRequest(t, "PATCH", "/api/apps/myapp", body, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	app, _ := store.GetAppBySlug("myapp")
	if !app.EphemeralDataAck {
		t.Error("DB not updated: EphemeralDataAck still false after PATCH")
	}
}

// A non-bool ephemeral_data_ack is rejected with 400.
func TestPatchApp_EphemeralDataAckRejectsNonBool(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := testHashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "bob", PasswordHash: hash, Role: "admin"})
	u, _ := store.GetUserByUsername("bob")
	token, _ := auth.IssueJWT(u.ID, "bob", "admin", "test-secret")
	store.CreateApp(db.CreateAppParams{Slug: "myapp", Name: "My App", OwnerID: u.ID})

	req := authedRequest(t, "PATCH", "/api/apps/myapp", []byte(`{"ephemeral_data_ack": "yes"}`), token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for non-bool ephemeral_data_ack, got %d: %s", rec.Code, rec.Body.String())
	}
}
