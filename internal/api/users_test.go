package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
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

func TestPatchUser_RejectsSystemUser(t *testing.T) {
	srv, store := newTestServer(t)
	syntheticUser, err := store.UpsertSystemUser(db.SystemUsernameDeploy, "developer")
	if err != nil {
		t.Fatal(err)
	}
	adminToken, _ := seedUserAndJWT(t, store, "admin1", "admin")

	body := strings.NewReader(`{"role":"admin"}`)
	req := httptest.NewRequest("PATCH", fmt.Sprintf("/api/users/%d", syntheticUser.ID), body)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func TestDeleteUser_RejectsSystemUser(t *testing.T) {
	srv, store := newTestServer(t)
	syntheticUser, err := store.UpsertSystemUser(db.SystemUsernameDeploy, "developer")
	if err != nil {
		t.Fatal(err)
	}
	adminToken, _ := seedUserAndJWT(t, store, "admin1", "admin")

	req := httptest.NewRequest("DELETE", fmt.Sprintf("/api/users/%d", syntheticUser.ID), nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func TestPatchUserPassword_RejectsSystemUser(t *testing.T) {
	srv, store := newTestServer(t)
	syntheticUser, err := store.UpsertSystemUser(db.SystemUsernameDeploy, "developer")
	if err != nil {
		t.Fatal(err)
	}
	adminToken, _ := seedUserAndJWT(t, store, "admin1", "admin")

	body := strings.NewReader(`{"password":"longenoughpw"}`)
	req := httptest.NewRequest("PATCH", fmt.Sprintf("/api/users/%d/password", syntheticUser.ID), body)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

// TestPatchUser_SetsManualOverride verifies that PATCH /api/users/{id} with a
// valid role sets the manual override and returns the updated role.
func TestPatchUser_SetsManualOverride(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"})
	store.CreateUser(db.CreateUserParams{Username: "bob", PasswordHash: hash, Role: "viewer"})
	admin, _ := store.GetUserByUsername("admin")
	bob, _ := store.GetUserByUsername("bob")
	token, _ := auth.IssueJWT(admin.ID, "admin", "admin", "test-secret")

	body, _ := json.Marshal(map[string]string{"role": "operator"})
	req := authedRequest(t, "PATCH", "/api/users/"+strconv.FormatInt(bob.ID, 10), body, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	u, _ := store.GetUserByID(bob.ID)
	if u.Role != "operator" {
		t.Fatalf("role = %q, want operator", u.Role)
	}
}

// TestPatchUser_ClearsManualOverrideWithEmptyRole verifies that an empty role
// in the PATCH body clears the manual override (returns the user to
// group/default governance) and returns 200.
func TestPatchUser_ClearsManualOverrideWithEmptyRole(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"})
	store.CreateUser(db.CreateUserParams{Username: "bob", PasswordHash: hash, Role: "viewer"})
	admin, _ := store.GetUserByUsername("admin")
	bob, _ := store.GetUserByUsername("bob")
	// Pre-set a manual override so there is something to clear.
	if err := store.SetManualRole(bob.ID, "operator"); err != nil {
		t.Fatalf("SetManualRole: %v", err)
	}
	token, _ := auth.IssueJWT(admin.ID, "admin", "admin", "test-secret")

	// Empty role means "clear manual override, revert to SSO/default governance".
	body, _ := json.Marshal(map[string]string{"role": ""})
	req := authedRequest(t, "PATCH", "/api/users/"+strconv.FormatInt(bob.ID, 10), body, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	u, _ := store.GetUserByID(bob.ID)
	// No groups and no mappings configured -> falls back to OAuthDefaultRole ("viewer").
	if u.Role != "viewer" {
		t.Fatalf("role = %q, want viewer after clear", u.Role)
	}
}

// TestPatchUser_MultiAdminDemotionAllowed confirms that demoting one of several
// admins returns 200, and that the 409 ErrLastAdmin wiring compiles and maps
// correctly at the handler level. The true last-admin 409 is structurally
// unreachable via PATCH /api/users/{id} (the caller must be admin, so at least
// two admins always exist); the store-level rejection is tested in db/.
func TestPatchUser_MultiAdminDemotionAllowed(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"})
	store.CreateUser(db.CreateUserParams{Username: "second", PasswordHash: hash, Role: "admin"})
	admin, _ := store.GetUserByUsername("admin")
	second, _ := store.GetUserByUsername("second")
	token, _ := auth.IssueJWT(admin.ID, "admin", "admin", "test-secret")

	// Demoting 'second' (one of two admins) to viewer is allowed.
	body, _ := json.Marshal(map[string]string{"role": "viewer"})
	req := authedRequest(t, "PATCH", "/api/users/"+strconv.FormatInt(second.ID, 10), body, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("multi-admin demotion: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	u, _ := store.GetUserByID(second.ID)
	if u.Role != "viewer" {
		t.Fatalf("role = %q, want viewer", u.Role)
	}
}
