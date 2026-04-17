package auth_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvben/shinyhub/internal/auth"
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
	claims, err := auth.ValidateJWT(token, secret, nil)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if claims.UserID != 42 || claims.Subject != "alice" || claims.Role != "admin" {
		t.Errorf("unexpected claims: %+v", claims)
	}
	if claims.ID == "" {
		t.Error("expected jti claim to be set on issued token")
	}
}

func TestValidateJWT_WrongSecret(t *testing.T) {
	token, _ := auth.IssueJWT(1, "alice", "admin", "secret-a")
	if _, err := auth.ValidateJWT(token, "secret-b", nil); err == nil {
		t.Error("expected validation to fail with wrong secret")
	}
}

func TestValidateJWT_RejectsRevokedToken(t *testing.T) {
	secret := "test-secret"
	token, err := auth.IssueJWT(1, "alice", "admin", secret)
	if err != nil {
		t.Fatal(err)
	}

	// Extract the jti by parsing once with no checker.
	claims, err := auth.ValidateJWT(token, secret, nil)
	if err != nil {
		t.Fatal(err)
	}

	revoker := func(jti string) (bool, error) { return jti == claims.ID, nil }
	_, err = auth.ValidateJWT(token, secret, revoker)
	if err == nil {
		t.Fatal("expected revoked token to fail validation")
	}
	if !errors.Is(err, auth.ErrTokenRevoked) {
		t.Errorf("expected ErrTokenRevoked, got %v", err)
	}
}

func TestValidateJWT_FailsClosedOnCheckerError(t *testing.T) {
	secret := "test-secret"
	token, _ := auth.IssueJWT(1, "alice", "admin", secret)
	boom := fmt.Errorf("db down")
	revoker := func(string) (bool, error) { return false, boom }
	if _, err := auth.ValidateJWT(token, secret, revoker); err == nil {
		t.Error("expected checker error to fail validation")
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
	handler := auth.BearerMiddleware(secret, nil, nil)(next)

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
	handler := auth.BearerMiddleware("secret", keyLookup, nil)(next)

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

func TestBearerMiddleware_SessionCookie(t *testing.T) {
	secret := "test-secret"
	token, _ := auth.IssueJWT(7, "alice", "developer", secret)

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := auth.UserFromContext(r.Context())
		if u == nil {
			http.Error(w, "no user", 500)
			return
		}
		w.Write([]byte(u.Username))
	})
	handler := auth.BearerMiddleware(secret, nil, nil)(next)

	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "alice" {
		t.Errorf("expected alice, got %s", rec.Body.String())
	}
}

func TestBearerMiddleware_InvalidHeaderDoesNotFallBackToCookie(t *testing.T) {
	secret := "test-secret"
	token, _ := auth.IssueJWT(7, "alice", "developer", secret)

	handler := auth.BearerMiddleware(secret, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer invalid-token")
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestRequireRole_UnknownUserRole(t *testing.T) {
	handler := auth.RequireRole(auth.RoleViewer)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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
	handler := auth.RequireRole(auth.RoleViewer)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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

func TestRequireRole_AllowsSufficientRole(t *testing.T) {
	h := auth.RequireRole(auth.RoleDeveloper)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest("GET", "/", nil)
	req = req.WithContext(auth.WithUser(req.Context(), &auth.ContextUser{Role: "admin"}))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
}

func TestRequireRole_RejectsInsufficientRole(t *testing.T) {
	h := auth.RequireRole(auth.RoleAdmin)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not have been called")
	}))
	req := httptest.NewRequest("GET", "/", nil)
	req = req.WithContext(auth.WithUser(req.Context(), &auth.ContextUser{Role: "developer"}))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", rr.Code)
	}
}
