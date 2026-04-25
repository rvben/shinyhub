package cli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestVerifyToken_Unauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/auth/me" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	t.Cleanup(srv.Close)

	err := verifyToken(srv.URL, "bad_token")
	if err == nil {
		t.Fatal("expected error for 401, got nil")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error should contain status code, got: %v", err)
	}
}

func TestVerifyToken_Forbidden(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"forbidden"}`))
	}))
	t.Cleanup(srv.Close)

	if err := verifyToken(srv.URL, "shk_forbidden"); err == nil {
		t.Fatal("expected error for 403, got nil")
	}
}

func TestVerifyToken_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/auth/me" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"user":{"id":1,"username":"admin","role":"admin"}}`))
	}))
	t.Cleanup(srv.Close)

	if err := verifyToken(srv.URL, "shk_good"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestVerifyToken_ServerDown(t *testing.T) {
	// Use a port unlikely to be listening.
	err := verifyToken("http://127.0.0.1:1", "shk_test")
	if err == nil {
		t.Fatal("expected error when server is unreachable, got nil")
	}
	if !strings.Contains(err.Error(), "connect to server") {
		t.Errorf("error should mention connection failure, got: %v", err)
	}
}
