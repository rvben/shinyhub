package api_test

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
)

func newTestServer(t *testing.T) (*api.Server, *db.Store) {
	t.Helper()
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Auth:    config.AuthConfig{Secret: "test-secret"},
		Storage: config.StorageConfig{AppsDir: t.TempDir()},
	}
	srv := api.New(cfg, store, nil, nil) // no manager/proxy for auth tests
	t.Cleanup(func() { store.Close() })
	return srv, store
}

func TestLogin(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass123")
	store.CreateUser(db.CreateUserParams{Username: "alice", PasswordHash: hash, Role: "admin"})

	body, _ := json.Marshal(map[string]string{"username": "alice", "password": "pass123"})
	req := httptest.NewRequest("POST", "/api/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]string
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["token"] == "" {
		t.Error("expected token in response")
	}
}

func TestLoginWrongPassword(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass123")
	store.CreateUser(db.CreateUserParams{Username: "alice", PasswordHash: hash, Role: "admin"})

	body, _ := json.Marshal(map[string]string{"username": "alice", "password": "wrong"})
	req := httptest.NewRequest("POST", "/api/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != 401 {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}
