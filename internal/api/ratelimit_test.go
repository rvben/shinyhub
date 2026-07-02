package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
)

// TestDeployRateLimit verifies that the 11th deploy request in a minute from
// the same user returns 429 Too Many Requests.
func TestDeployRateLimit(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass-" + strings.Repeat("x", 16))
	if err := store.CreateUser(db.CreateUserParams{Username: "alice", PasswordHash: hash, Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	u, err := store.GetUserByUsername("alice")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := auth.IssueJWT(u.ID, u.Username, u.Role, "test-secret")
	if err != nil {
		t.Fatal(err)
	}

	var last int
	for i := 0; i < 11; i++ {
		req := httptest.NewRequest("POST", "/api/apps/missing/deploy", strings.NewReader(""))
		req.Header.Set("Authorization", "Bearer "+tok)
		rr := httptest.NewRecorder()
		srv.Router().ServeHTTP(rr, req)
		last = rr.Code
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("expected 429 on 11th request, got %d", last)
	}
}

// TestRateLimit429IsJSONEnvelope verifies a rate-limited response uses the same
// JSON error envelope ({"error":...}) and application/json content type as the
// rest of the API, rather than plain text - so a strict JSON client (or one
// reading failure_kind) does not break specifically on the 429 a CI pipeline is
// most likely to hit under load.
func TestRateLimit429IsJSONEnvelope(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass-" + strings.Repeat("x", 16))
	if err := store.CreateUser(db.CreateUserParams{Username: "alice", PasswordHash: hash, Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	u, _ := store.GetUserByUsername("alice")
	tok, err := auth.IssueJWT(u.ID, u.Username, u.Role, "test-secret")
	if err != nil {
		t.Fatal(err)
	}

	var rr *httptest.ResponseRecorder
	for i := 0; i < 11; i++ {
		req := httptest.NewRequest("POST", "/api/apps/missing/deploy", strings.NewReader(""))
		req.Header.Set("Authorization", "Bearer "+tok)
		rr = httptest.NewRecorder()
		srv.Router().ServeHTTP(rr, req)
	}
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 on 11th request, got %d", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("429 Content-Type = %q, want application/json", ct)
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("429 body must be a JSON envelope, got %q (%v)", rr.Body.String(), err)
	}
	if body.Error == "" {
		t.Errorf("429 JSON must carry a non-empty error field, got %q", rr.Body.String())
	}
}

// TestActionRateLimit verifies restart is rate limited per-user (the
// actionLimiter is shared by restart/rollback/manual schedule run at 30/min).
func TestActionRateLimit(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass-" + strings.Repeat("x", 16))
	if err := store.CreateUser(db.CreateUserParams{Username: "alice", PasswordHash: hash, Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	u, err := store.GetUserByUsername("alice")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := auth.IssueJWT(u.ID, u.Username, u.Role, "test-secret")
	if err != nil {
		t.Fatal(err)
	}

	var last int
	for i := 0; i < 31; i++ {
		req := httptest.NewRequest("POST", "/api/apps/missing/restart", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		rr := httptest.NewRecorder()
		srv.Router().ServeHTTP(rr, req)
		last = rr.Code
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("expected 429 on 31st restart, got %d", last)
	}
}

// TestOAuthLoginRateLimitByIP verifies the OAuth login-start endpoint is rate
// limited per client IP (20/min) even without an authenticated user.
func TestOAuthLoginRateLimitByIP(t *testing.T) {
	srv, _ := newTestServer(t)

	var last int
	for i := 0; i < 21; i++ {
		req := httptest.NewRequest("GET", "/api/auth/github/login", nil)
		req.RemoteAddr = "203.0.113.7:5555"
		rr := httptest.NewRecorder()
		srv.Router().ServeHTTP(rr, req)
		last = rr.Code
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("expected 429 on 21st login-start from one IP, got %d", last)
	}
}
