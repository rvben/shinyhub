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

func TestHandleLogs_SSEInitialBurst(t *testing.T) {
	srv, store, appsDir := newLogsTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	u, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "myapp", Name: "My App", OwnerID: u.ID})

	// Pre-populate log file
	logPath := filepath.Join(appsDir, "myapp", "app.log")
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
