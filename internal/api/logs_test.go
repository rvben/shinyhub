package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/process"
)

func newLogsTestServer(t *testing.T) (*api.Server, *db.Store, string) {
	t.Helper()
	appsDir := t.TempDir()
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Auth:    config.AuthConfig{Secret: "test-secret"},
		Storage: config.StorageConfig{AppsDir: appsDir},
	}
	mgr := process.NewManager(appsDir, process.NewNativeRuntime())
	srv := api.New(cfg, store, mgr, nil)
	t.Cleanup(func() { store.Close() })
	return srv, store, appsDir
}

func TestHandleLogs_NoLogFile(t *testing.T) {
	srv, store, _ := newLogsTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	u, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "myapp", Name: "My App", OwnerID: u.ID})

	token, _ := auth.IssueJWT(u.ID, "owner", "developer", "test-secret")
	req := httptest.NewRequest("GET", "/api/apps/myapp/logs", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 when no log file exists, got %d", rec.Code)
	}
}

// TestHandleLogs_TailLimitsInitialBurst verifies that ?tail=N caps the number
// of initial lines emitted. With a 5-line file and ?tail=2, only the last two
// lines should appear.
func TestHandleLogs_TailLimitsInitialBurst(t *testing.T) {
	srv, store, appsDir := newLogsTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	u, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "myapp", Name: "My App", OwnerID: u.ID})

	logPath := filepath.Join(appsDir, "myapp", "app-0.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte("a\nb\nc\nd\ne\n"), 0644); err != nil {
		t.Fatal(err)
	}

	token, _ := auth.IssueJWT(u.ID, "owner", "developer", "test-secret")
	req := httptest.NewRequest("GET", "/api/apps/myapp/logs?tail=2&follow=false", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, absent := range []string{"a\n", "b\n", "c\n"} {
		if strings.Contains(body, absent) {
			t.Errorf("body should not contain %q (tail=2 caps to last 2 lines), got:\n%s", absent, body)
		}
	}
	for _, want := range []string{"d\n", "e\n"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q, got:\n%s", want, body)
		}
	}
}

// TestHandleLogs_NoFollowReturnsPlainText verifies that ?follow=false emits
// plain text/plain output (one line per row, no "data:" SSE prefix) and
// closes the connection immediately. This is the kubectl-style one-shot
// fetch shape that scripts can pipe to tail/grep without parsing SSE frames.
func TestHandleLogs_NoFollowReturnsPlainText(t *testing.T) {
	srv, store, appsDir := newLogsTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	u, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "myapp", Name: "My App", OwnerID: u.ID})

	logPath := filepath.Join(appsDir, "myapp", "app-0.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte("hello\nworld\n"), 0644); err != nil {
		t.Fatal(err)
	}

	token, _ := auth.IssueJWT(u.ID, "owner", "developer", "test-secret")
	req := httptest.NewRequest("GET", "/api/apps/myapp/logs?follow=false", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain; follow=false should not emit SSE", ct)
	}
	body := rec.Body.String()
	if strings.Contains(body, "data:") {
		t.Errorf("body should not contain SSE 'data:' prefix when follow=false, got:\n%s", body)
	}
	if body != "hello\nworld\n" {
		t.Errorf("body = %q, want %q", body, "hello\nworld\n")
	}
}

// TestHandleLogs_TailZeroRejected ensures the handler rejects nonsensical
// tail values rather than silently emitting nothing or the default 200.
func TestHandleLogs_TailZeroRejected(t *testing.T) {
	srv, store, appsDir := newLogsTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	u, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "myapp", Name: "My App", OwnerID: u.ID})

	logPath := filepath.Join(appsDir, "myapp", "app-0.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte("x\n"), 0644); err != nil {
		t.Fatal(err)
	}

	token, _ := auth.IssueJWT(u.ID, "owner", "developer", "test-secret")
	for _, raw := range []string{"0", "-1", "abc", "1000000"} {
		req := httptest.NewRequest("GET", "/api/apps/myapp/logs?tail="+raw, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("tail=%s: status = %d, want 400", raw, rec.Code)
		}
	}
}

func TestHandleLogs_SSEInitialBurst(t *testing.T) {
	srv, store, appsDir := newLogsTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	u, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "myapp", Name: "My App", OwnerID: u.ID})

	// Pre-populate log file for replica 0 (the default).
	logPath := filepath.Join(appsDir, "myapp", "app-0.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte("alpha\nbeta\ngamma\n"), 0644); err != nil {
		t.Fatal(err)
	}

	token, _ := auth.IssueJWT(u.ID, "owner", "developer", "test-secret")

	// Use a context with timeout so the SSE handler returns after the initial burst.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest("GET", "/api/apps/myapp/logs", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	body := rec.Body.String()
	for _, want := range []string{"data: alpha\n", "data: beta\n", "data: gamma\n"} {
		if !strings.Contains(body, want) {
			t.Errorf("expected SSE line %q in body, got:\n%s", want, body)
		}
	}
}
