package api_test

import (
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
