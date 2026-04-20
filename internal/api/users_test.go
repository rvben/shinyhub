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

func TestCreateUser_Viewer(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"})
	admin, _ := store.GetUserByUsername("admin")
	adminToken, _ := auth.IssueJWT(admin.ID, "admin", "admin", "test-secret")

	body, _ := json.Marshal(map[string]string{
		"username": "guest",
		"password": "secret123",
		"role":     "viewer",
	})
	req := authedRequest(t, "POST", "/api/users", body, adminToken)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["role"] != "viewer" {
		t.Errorf("expected role=viewer, got %v", resp["role"])
	}

	// Round-trip through the store so we know the role persists.
	got, err := store.GetUserByUsername("guest")
	if err != nil {
		t.Fatalf("reload viewer: %v", err)
	}
	if got.Role != "viewer" {
		t.Errorf("stored role = %q, want viewer", got.Role)
	}
}

func TestCreateUser_RejectsInvalidRole(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"})
	admin, _ := store.GetUserByUsername("admin")
	adminToken, _ := auth.IssueJWT(admin.ID, "admin", "admin", "test-secret")

	body, _ := json.Marshal(map[string]string{
		"username": "bad",
		"password": "secret123",
		"role":     "wizard",
	})
	req := authedRequest(t, "POST", "/api/users", body, adminToken)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid role, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestPatchUser_DowngradeToViewer(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"})
	store.CreateUser(db.CreateUserParams{Username: "target", PasswordHash: hash, Role: "developer"})
	admin, _ := store.GetUserByUsername("admin")
	target, _ := store.GetUserByUsername("target")
	adminToken, _ := auth.IssueJWT(admin.ID, "admin", "admin", "test-secret")

	body, _ := json.Marshal(map[string]string{"role": "viewer"})
	path := fmt.Sprintf("/api/users/%d", target.ID)
	req := authedRequest(t, "PATCH", path, body, adminToken)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["role"] != "viewer" {
		t.Errorf("expected role=viewer, got %v", resp["role"])
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

func TestPatchUserPassword_Admin(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"})
	store.CreateUser(db.CreateUserParams{Username: "target", PasswordHash: hash, Role: "developer"})
	admin, _ := store.GetUserByUsername("admin")
	target, _ := store.GetUserByUsername("target")
	adminToken, _ := auth.IssueJWT(admin.ID, "admin", "admin", "test-secret")

	body, _ := json.Marshal(map[string]string{"password": "newsecret123"})
	path := fmt.Sprintf("/api/users/%d/password", target.ID)
	req := authedRequest(t, "PATCH", path, body, adminToken)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}

	updated, err := store.GetUserByUsername("target")
	if err != nil {
		t.Fatalf("reload target: %v", err)
	}
	if err := auth.VerifyPassword(updated.PasswordHash, "newsecret123"); err != nil {
		t.Errorf("new password does not verify: %v", err)
	}
}

func TestPatchUserPassword_TooShort(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"})
	store.CreateUser(db.CreateUserParams{Username: "target", PasswordHash: hash, Role: "developer"})
	admin, _ := store.GetUserByUsername("admin")
	target, _ := store.GetUserByUsername("target")
	adminToken, _ := auth.IssueJWT(admin.ID, "admin", "admin", "test-secret")

	body, _ := json.Marshal(map[string]string{"password": "short"})
	path := fmt.Sprintf("/api/users/%d/password", target.ID)
	req := authedRequest(t, "PATCH", path, body, adminToken)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestPatchUserPassword_NonAdminForbidden(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "dev", PasswordHash: hash, Role: "developer"})
	store.CreateUser(db.CreateUserParams{Username: "target", PasswordHash: hash, Role: "developer"})
	dev, _ := store.GetUserByUsername("dev")
	target, _ := store.GetUserByUsername("target")
	devToken, _ := auth.IssueJWT(dev.ID, "dev", "developer", "test-secret")

	body, _ := json.Marshal(map[string]string{"password": "newsecret123"})
	path := fmt.Sprintf("/api/users/%d/password", target.ID)
	req := authedRequest(t, "PATCH", path, body, devToken)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d: %s", rec.Code, rec.Body.String())
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
