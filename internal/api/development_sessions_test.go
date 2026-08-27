package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestDevelopmentSessionHeartbeatStartsRenewsAndCannotReopenLease(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := testHashPassword("pass")
	if err := store.CreateUser(db.CreateUserParams{Username: "lease-dev", PasswordHash: hash, Role: "developer"}); err != nil {
		t.Fatal(err)
	}
	u, _ := store.GetUserByUsername("lease-dev")
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "lease-app", Name: "Lease app", OwnerID: u.ID, Access: "private"}); err != nil {
		t.Fatal(err)
	}
	token, _ := auth.IssueJWT(u.ID, u.Username, u.Role, "test-secret")
	id := "33333333333333333333333333333333"
	forgedID := "66666666666666666666666666666666"
	forged := authedRequest(t, http.MethodPost, "/api/apps/lease-app/development-sessions/"+forgedID+"/heartbeat", nil, token)
	forged.Header.Set("X-Shinyhub-Deploy-Channel", "watch")
	forged.Header.Set("X-Shinyhub-Development-Session", forgedID)
	forged.Header.Set("X-Shinyhub-Development-Target", db.DevelopmentTargetEphemeral)
	forgedRec := httptest.NewRecorder()
	srv.Router().ServeHTTP(forgedRec, forged)
	if forgedRec.Code != http.StatusConflict {
		t.Fatalf("forged ephemeral heartbeat = %d: %s", forgedRec.Code, forgedRec.Body.String())
	}
	heartbeat := func() *httptest.ResponseRecorder {
		req := authedRequest(t, http.MethodPost, "/api/apps/lease-app/development-sessions/"+id+"/heartbeat", nil, token)
		req.Header.Set("X-Shinyhub-Deploy-Channel", "watch")
		req.Header.Set("X-Shinyhub-Development-Session", id)
		req.Header.Set("X-Shinyhub-Development-Target", db.DevelopmentTargetExisting)
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, req)
		return rec
	}
	if rec := heartbeat(); rec.Code != http.StatusNoContent {
		t.Fatalf("start heartbeat = %d: %s", rec.Code, rec.Body.String())
	}
	app, _ := store.GetAppBySlug("lease-app")
	if session, err := store.GetDevelopmentSession(app.ID, id); err != nil || session.Status != db.DevelopmentSessionActive {
		t.Fatalf("active session = %+v, err=%v", session, err)
	}
	end := authedRequest(t, http.MethodPost, "/api/apps/lease-app/development-sessions/"+id+"/end", nil, token)
	endRec := httptest.NewRecorder()
	srv.Router().ServeHTTP(endRec, end)
	if endRec.Code != http.StatusNoContent {
		t.Fatalf("end = %d: %s", endRec.Code, endRec.Body.String())
	}
	if rec := heartbeat(); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "start a new session") {
		t.Fatalf("reopen heartbeat = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestDevelopmentAppCreationConflictIsAtomicAndSanitized(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := testHashPassword("pass")
	if err := store.CreateUser(db.CreateUserParams{Username: "atomic-dev", PasswordHash: hash, Role: "developer"}); err != nil {
		t.Fatal(err)
	}
	u, _ := store.GetUserByUsername("atomic-dev")
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "session-owner", Name: "Session owner", OwnerID: u.ID, Access: "private"}); err != nil {
		t.Fatal(err)
	}
	existing, _ := store.GetAppBySlug("session-owner")
	id := "55555555555555555555555555555555"
	if err := store.UpsertDevelopmentSession(db.UpsertDevelopmentSessionParams{
		ID: id, AppID: existing.ID, TargetKind: db.DevelopmentTargetExisting,
	}); err != nil {
		t.Fatal(err)
	}
	token, _ := auth.IssueJWT(u.ID, u.Username, u.Role, "test-secret")
	body, _ := json.Marshal(map[string]any{
		"slug": "must-roll-back", "name": "Must roll back", "access": "private", "project_slug": "must-not-leak",
		"development_session_id": id, "development_target": db.DevelopmentTargetCreated,
	})
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, authedRequest(t, http.MethodPost, "/api/apps", body, token))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "start a new session") {
		t.Fatalf("conflicting create = %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(strings.ToLower(rec.Body.String()), "constraint") || strings.Contains(strings.ToLower(rec.Body.String()), "database") {
		t.Fatalf("database detail leaked: %s", rec.Body.String())
	}
	if _, err := store.GetAppBySlug("must-roll-back"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("app survived failed request: %v", err)
	}
	if _, err := store.GetProject("must-not-leak"); !errors.Is(err, db.ErrProjectNotFound) {
		t.Fatalf("project survived failed request: %v", err)
	}
}
