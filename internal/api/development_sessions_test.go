package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
)

func TestCreateEphemeralDevelopmentAppRecordsLifecycleAndReapsIt(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := testHashPassword("pass")
	if err := store.CreateUser(db.CreateUserParams{Username: "dev", PasswordHash: hash, Role: "developer"}); err != nil {
		t.Fatal(err)
	}
	u, _ := store.GetUserByUsername("dev")
	token, _ := auth.IssueJWT(u.ID, u.Username, u.Role, "test-secret")
	sessionID := "0123456789abcdef0123456789abcdef"
	// Exercise the documented lower boundary: request transit must not make an
	// exact --ttl 15m value fail server-side validation.
	expires := time.Now().UTC().Add(15 * time.Minute)
	body, _ := json.Marshal(map[string]any{
		"slug": "scratch-dev", "name": "Scratch dev", "access": "private",
		"development_session_id": sessionID,
		"development_target":     db.DevelopmentTargetEphemeral,
		"expires_at":             expires,
	})
	req := authedRequest(t, http.MethodPost, "/api/apps", body, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body.String())
	}
	app, err := store.GetAppBySlug("scratch-dev")
	if err != nil || app.Access != "private" {
		t.Fatalf("created app = %+v, err=%v", app, err)
	}
	session, err := store.GetDevelopmentSession(app.ID, sessionID)
	if err != nil || session.TargetKind != db.DevelopmentTargetEphemeral || session.ExpiresAt == nil {
		t.Fatalf("session = %+v, err=%v", session, err)
	}
	if err := store.MarkEphemeralApp(app.ID, sessionID, time.Now().UTC().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go srv.RunDevelopmentAppReaper(ctx, 5*time.Millisecond)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := store.GetAppBySlug("scratch-dev"); errors.Is(err, db.ErrNotFound) {
			count, countErr := store.CountAuditEvents("development_app_expired")
			if countErr != nil {
				t.Fatal(countErr)
			}
			if count == 1 {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("expired app still exists")
}

func TestCreateEphemeralDevelopmentAppRejectsPublicAccess(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := testHashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "dev", PasswordHash: hash, Role: "developer"})
	token, _ := auth.IssueJWT(1, "dev", "developer", "test-secret")
	body, _ := json.Marshal(map[string]any{
		"slug": "public-scratch", "name": "Public scratch", "access": "public",
		"development_session_id": "0123456789abcdef0123456789abcdef",
		"development_target":     db.DevelopmentTargetEphemeral,
		"expires_at":             time.Now().UTC().Add(time.Hour),
	})
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, authedRequest(t, http.MethodPost, "/api/apps", body, token))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("public ephemeral create = %d: %s", rec.Code, rec.Body.String())
	}
}
