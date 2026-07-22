package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestLoginRateLimit_BlocksAfterThreshold proves the login endpoint - the
// primary password-guessing target - enforces its per-IP rate limit: after the
// configured number of attempts from one IP, further attempts are rejected with
// 429 instead of continuing to accept guesses (TEST-5). The limiter is checked
// before credential validation, so failed logins still consume the budget.
func TestLoginRateLimit_BlocksAfterThreshold(t *testing.T) {
	srv, _ := newTestServer(t) // login limiter: 10 / minute
	// The limiter is a FIXED window bucketed on floor(now/window). At the
	// production one-minute window this burst can straddle a minute tick, get a
	// fresh count, and legitimately allow the 11th attempt - which made this test
	// flaky on Postgres, where each attempt pays a DB round trip. Widening the
	// window keeps the limit, backend and code path identical while making the
	// straddle impossible for a burst this short.
	srv.SetLoginLimiterWindowForTest(24 * time.Hour)
	h := srv.Router()

	const attempts = 11
	const fromIP = "203.0.113.5:44444"
	body := `{"username":"nobody","password":"wrong"}`

	codes := make([]int, 0, attempts)
	for i := 0; i < attempts; i++ {
		req := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(body))
		req.RemoteAddr = fromIP
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		codes = append(codes, rr.Code)
	}

	// The first attempt must not already be throttled (limiter isn't blocking
	// from the start) and must be a normal auth rejection, not 429.
	if codes[0] == http.StatusTooManyRequests {
		t.Fatalf("first login attempt was already rate-limited (429); limiter over-aggressive")
	}
	// The 11th attempt (over the 10/min budget) must be blocked.
	if last := codes[attempts-1]; last != http.StatusTooManyRequests {
		t.Fatalf("attempt %d: got %d, want 429 (rate limit not enforced): %v", attempts, last, codes)
	}
}
