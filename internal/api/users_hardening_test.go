package api_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

func adminReq(t *testing.T, method, path string, body any, token string) *http.Request {
	t.Helper()
	var r *http.Request
	if body != nil {
		b, _ := json.Marshal(body)
		r = httptest.NewRequest(method, path, bytes.NewReader(b))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	r.Header.Set("Authorization", "Bearer "+token)
	return r
}

func TestCreateUser_RejectsPasswordUnder8Chars(t *testing.T) {
	srv, store := newTestServer(t)
	token, _ := seedUserAndJWT(t, store, "admin", "admin")

	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, adminReq(t, http.MethodPost, "/api/users",
		map[string]string{"username": "bob", "password": "short", "role": "developer"}, token))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("short password: want 400, got %d: %s", rec.Code, rec.Body.String())
	}

	// A compliant password still creates the user (guard against over-rejection).
	rec = httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, adminReq(t, http.MethodPost, "/api/users",
		map[string]string{"username": "carol", "password": "longenough", "role": "developer"}, token))
	if rec.Code != http.StatusCreated {
		t.Fatalf("valid password: want 201, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestPatchUser_RejectsSelfRoleChange(t *testing.T) {
	srv, store := newTestServer(t)
	token, adminID := seedUserAndJWT(t, store, "admin", "admin")

	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, adminReq(t, http.MethodPatch, fmt.Sprintf("/api/users/%d", adminID),
		map[string]string{"role": "viewer"}, token))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("self role-change: want 403, got %d: %s", rec.Code, rec.Body.String())
	}

	// Changing a different user's role still works.
	_ = store.CreateUser(db.CreateUserParams{Username: "other", PasswordHash: "x", Role: "developer"})
	other, _ := store.GetUserByUsername("other")
	rec = httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, adminReq(t, http.MethodPatch, fmt.Sprintf("/api/users/%d", other.ID),
		map[string]string{"role": "operator"}, token))
	if rec.Code != http.StatusOK {
		t.Fatalf("other-user role-change: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestDeleteUser_RejectsSelfDelete(t *testing.T) {
	srv, store := newTestServer(t)
	token, adminID := seedUserAndJWT(t, store, "admin", "admin")

	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, adminReq(t, http.MethodDelete, fmt.Sprintf("/api/users/%d", adminID), nil, token))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("self delete: want 403, got %d: %s", rec.Code, rec.Body.String())
	}
}
