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

// TestCreateApp_RateLimited proves POST /api/apps is per-user rate-limited so a
// developer cannot cheaply grow the apps table (SEC-M3). The shared action
// limiter is 30/min; the 31st create from one user must be rejected with 429.
func TestCreateApp_RateLimited(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "bob", PasswordHash: hash, Role: "developer"})
	token, _ := auth.IssueJWT(1, "bob", "developer", "test-secret")

	var last int
	for i := 0; i < 31; i++ {
		body, _ := json.Marshal(map[string]string{"slug": fmt.Sprintf("app-%d", i), "name": "A"})
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, authedRequest(t, "POST", "/api/apps", body, token))
		last = rec.Code
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("31st create attempt: got %d, want 429 (create not rate-limited)", last)
	}
}
