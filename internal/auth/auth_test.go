package auth_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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

// TestIssueJWT_StampsAuthTime verifies a freshly issued token carries an
// auth_time claim (the original login time) so the absolute session lifetime
// can be bounded across sliding renewals.
func TestIssueJWT_StampsAuthTime(t *testing.T) {
	secret := "test-secret"
	token, err := auth.IssueJWT(1, "alice", "admin", secret)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := auth.ValidateJWT(token, secret, nil)
	if err != nil {
		t.Fatal(err)
	}
	if claims.AuthTime == nil {
		t.Fatal("issued token must carry an auth_time claim")
	}
	if d := time.Since(claims.AuthTime.Time); d < 0 || d > time.Minute {
		t.Errorf("auth_time = %v, want ~now", claims.AuthTime.Time)
	}
}

// TestIssueJWTAt_PreservesAuthTime verifies a sliding renewal keeps the original
// login time rather than resetting it, so repeated renewals cannot extend a
// session past its absolute lifetime.
func TestIssueJWTAt_PreservesAuthTime(t *testing.T) {
	secret := "test-secret"
	orig := time.Now().Add(-3 * time.Hour)
	token, err := auth.IssueJWTAt(1, "alice", "admin", secret, orig)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := auth.ValidateJWT(token, secret, nil)
	if err != nil {
		t.Fatal(err)
	}
	if claims.AuthTime == nil || claims.AuthTime.Time.Sub(orig).Abs() > time.Second {
		t.Errorf("auth_time = %v, want the preserved original %v", claims.AuthTime, orig)
	}
}

// TestCanSlideSession bounds the absolute session lifetime: a session younger
// than AbsoluteSessionMaxAge may be renewed; an older one may not (forcing a
// fresh login, which re-runs SSO group reconciliation). A zero auth_time
// (legacy token) is renewable so a deploy does not log everyone out.
func TestCanSlideSession(t *testing.T) {
	if !auth.CanSlideSession(time.Now().Add(-time.Minute)) {
		t.Error("a young session must be renewable")
	}
	if auth.CanSlideSession(time.Now().Add(-auth.AbsoluteSessionMaxAge - time.Minute)) {
		t.Error("a session past the absolute max age must not be renewable")
	}
	if !auth.CanSlideSession(time.Time{}) {
		t.Error("a zero (legacy) auth_time must be renewable")
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
	handler := auth.BearerMiddleware(secret, nil, nil, nil)(next)

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
	handler := auth.BearerMiddleware("secret", keyLookup, nil, nil)(next)

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
	handler := auth.BearerMiddleware(secret, nil, nil, nil)(next)

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

	handler := auth.BearerMiddleware(secret, nil, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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

// TestBearerMiddleware_RevalidatesUserViaLookup proves that the DB-backed
// userLookup wins over claims baked into the JWT. A user demoted from admin
// to viewer must lose admin privileges on the next request, even if the
// previously-issued admin token has not yet expired.
func TestBearerMiddleware_RevalidatesUserViaLookup(t *testing.T) {
	secret := "test-secret"
	token, _ := auth.IssueJWT(7, "alice", "admin", secret)

	userLookup := func(id int64) (*auth.ContextUser, error) {
		if id != 7 {
			t.Fatalf("expected userLookup for ID 7, got %d", id)
		}
		return &auth.ContextUser{ID: 7, Username: "alice", Role: "viewer"}, nil
	}

	var seen *auth.ContextUser
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = auth.UserFromContext(r.Context())
	})
	handler := auth.BearerMiddleware(secret, nil, userLookup, nil)(next)

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if seen == nil {
		t.Fatal("expected user in context")
	}
	if seen.Role != "viewer" {
		t.Fatalf("DB role must win over JWT claim: got role=%q, want viewer", seen.Role)
	}
}

// TestBearerMiddleware_LookupErrorRejects proves that a userLookup error
// (e.g. user was deleted) rejects the request without invoking the handler.
// This is the path that makes account deletions take effect without waiting
// for the JWT to expire.
func TestBearerMiddleware_LookupErrorRejects(t *testing.T) {
	secret := "test-secret"
	token, _ := auth.IssueJWT(7, "alice", "admin", secret)

	userLookup := func(int64) (*auth.ContextUser, error) {
		return nil, errors.New("user not found")
	}

	called := false
	next := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		called = true
	})
	handler := auth.BearerMiddleware(secret, nil, userLookup, nil)(next)

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	if called {
		t.Fatal("handler must not be called when lookup rejects")
	}
}

// TestBearerMiddleware_SessionCookieRevalidates mirrors the bearer-header path
// for session cookie auth: a stale session cookie issued before a role
// demotion must yield the demoted role on subsequent requests.
func TestBearerMiddleware_SessionCookieRevalidates(t *testing.T) {
	secret := "test-secret"
	token, _ := auth.IssueJWT(7, "alice", "admin", secret)

	userLookup := func(id int64) (*auth.ContextUser, error) {
		return &auth.ContextUser{ID: id, Username: "alice", Role: "viewer"}, nil
	}

	var seen *auth.ContextUser
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = auth.UserFromContext(r.Context())
	})
	handler := auth.BearerMiddleware(secret, nil, userLookup, nil)(next)

	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if seen == nil || seen.Role != "viewer" {
		t.Fatalf("session cookie path must also re-resolve via lookup: got %+v", seen)
	}
}

func TestHashAPIKey_Distinct(t *testing.T) {
	h1 := auth.HashAPIKey("shk_key_one")
	h2 := auth.HashAPIKey("shk_key_two")
	if h1 == h2 {
		t.Error("expected distinct keys to produce distinct hashes")
	}
}
