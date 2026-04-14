package auth_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvben/shinyhost/internal/auth"
)

func TestHashAndVerifyPassword(t *testing.T) {
	hash, err := auth.HashPassword("secret123")
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.VerifyPassword(hash, "secret123"); err != nil {
		t.Errorf("expected password to verify: %v", err)
	}
	if err := auth.VerifyPassword(hash, "wrong"); err == nil {
		t.Error("expected wrong password to fail")
	}
}

func TestIssueAndValidateJWT(t *testing.T) {
	secret := "test-secret"
	token, err := auth.IssueJWT(42, "alice", "admin", secret)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := auth.ValidateJWT(token, secret)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if claims.UserID != 42 || claims.Subject != "alice" || claims.Role != "admin" {
		t.Errorf("unexpected claims: %+v", claims)
	}
}

func TestValidateJWT_WrongSecret(t *testing.T) {
	token, _ := auth.IssueJWT(1, "alice", "admin", "secret-a")
	if _, err := auth.ValidateJWT(token, "secret-b"); err == nil {
		t.Error("expected validation to fail with wrong secret")
	}
}

func TestHashAPIKey(t *testing.T) {
	key := "shk_abc123"
	h1 := auth.HashAPIKey(key)
	h2 := auth.HashAPIKey(key)
	if h1 != h2 {
		t.Error("expected deterministic hash")
	}
	if h1 == key {
		t.Error("expected hash to differ from key")
	}
}

func TestBearerMiddleware(t *testing.T) {
	secret := "test-secret"
	token, _ := auth.IssueJWT(1, "alice", "admin", secret)

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := auth.UserFromContext(r.Context())
		if u == nil {
			http.Error(w, "no user", 500)
			return
		}
		w.Write([]byte(u.Username))
	})
	handler := auth.BearerMiddleware(secret, nil)(next)

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if rec.Body.String() != "alice" {
		t.Errorf("expected alice, got %s", rec.Body.String())
	}
}

func TestBearerMiddleware_TokenScheme(t *testing.T) {
	rawKey := "shk_testkey123"
	keyHash := auth.HashAPIKey(rawKey)

	keyLookup := func(hash string) (*auth.ContextUser, error) {
		if hash == keyHash {
			return &auth.ContextUser{ID: 99, Username: "bot", Role: "developer"}, nil
		}
		return nil, fmt.Errorf("not found")
	}

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := auth.UserFromContext(r.Context())
		if u == nil {
			http.Error(w, "no user", 500)
			return
		}
		w.Write([]byte(u.Username))
	})
	handler := auth.BearerMiddleware("secret", keyLookup)(next)

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Token "+rawKey)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "bot" {
		t.Errorf("expected bot, got %s", rec.Body.String())
	}
}

func TestRequireRole_UnknownUserRole(t *testing.T) {
	handler := auth.RequireRole("viewer")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest("GET", "/", nil)
	ctx := auth.WithUser(req.Context(), &auth.ContextUser{ID: 1, Username: "x", Role: "superadmin"})
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 for unknown role, got %d", rec.Code)
	}
}

func TestRequireRole_NoUser(t *testing.T) {
	handler := auth.RequireRole("viewer")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for missing user, got %d", rec.Code)
	}
}

func TestHashAPIKey_Distinct(t *testing.T) {
	h1 := auth.HashAPIKey("shk_key_one")
	h2 := auth.HashAPIKey("shk_key_two")
	if h1 == h2 {
		t.Error("expected distinct keys to produce distinct hashes")
	}
}
