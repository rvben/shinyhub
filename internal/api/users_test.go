package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
)

func TestListUsers_AdminOnly(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "dev", PasswordHash: hash, Role: "developer"})
	devToken, _ := auth.IssueJWT(1, "dev", "developer", "test-secret")

	req := authedRequest(t, "GET", "/api/users", nil, devToken)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("non-admin: expected 403, got %d", rec.Code)
	}
}

func TestListUsers_Admin(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"})
	admin, _ := store.GetUserByUsername("admin")
	adminToken, _ := auth.IssueJWT(admin.ID, "admin", "admin", "test-secret")

	req := authedRequest(t, "GET", "/api/users", nil, adminToken)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var users []map[string]any
	json.NewDecoder(rec.Body).Decode(&users)
	if len(users) == 0 {
		t.Error("expected at least one user in list")
	}
}

func TestCreateUser_Admin(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"})
	admin, _ := store.GetUserByUsername("admin")
	adminToken, _ := auth.IssueJWT(admin.ID, "admin", "admin", "test-secret")

	body, _ := json.Marshal(map[string]string{
		"username": "newdev",
		"password": "secret123",
		"role":     "developer",
	})
	req := authedRequest(t, "POST", "/api/users", body, adminToken)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["username"] != "newdev" {
		t.Errorf("expected username=newdev, got %v", resp["username"])
	}
}

func TestPatchUser_UpdateRole(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"})
	store.CreateUser(db.CreateUserParams{Username: "target", PasswordHash: hash, Role: "developer"})
	admin, _ := store.GetUserByUsername("admin")
	target, _ := store.GetUserByUsername("target")
	adminToken, _ := auth.IssueJWT(admin.ID, "admin", "admin", "test-secret")

	body, _ := json.Marshal(map[string]string{"role": "operator"})
	path := fmt.Sprintf("/api/users/%d", target.ID)
	req := authedRequest(t, "PATCH", path, body, adminToken)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["role"] != "operator" {
		t.Errorf("expected role=operator, got %v", resp["role"])
	}
}

func TestDeleteUser_Admin(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"})
	store.CreateUser(db.CreateUserParams{Username: "todelete", PasswordHash: hash, Role: "developer"})
	admin, _ := store.GetUserByUsername("admin")
	target, _ := store.GetUserByUsername("todelete")
	adminToken, _ := auth.IssueJWT(admin.ID, "admin", "admin", "test-secret")

	path := fmt.Sprintf("/api/users/%d", target.ID)
	req := authedRequest(t, "DELETE", path, nil, adminToken)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestGetUserByUsername_PathParam(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "alice", PasswordHash: hash, Role: "developer"})
	store.CreateUser(db.CreateUserParams{Username: "bob", PasswordHash: hash, Role: "developer"})
	alice, _ := store.GetUserByUsername("alice")
	token, _ := auth.IssueJWT(alice.ID, "alice", "developer", "test-secret")

	req := authedRequest(t, "GET", "/api/users/alice", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["username"] != "alice" {
		t.Errorf("expected username=alice, got %v", resp["username"])
	}
}
