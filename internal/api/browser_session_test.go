package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/dbtest"
)

func TestBrowserSessionConfiguredRenewalAndDeadline(t *testing.T) {
	store := dbtest.New(t)
	ttl, maxAge := 10*time.Minute, 4*time.Hour
	cfg := &config.Config{Auth: config.AuthConfig{Secret: "test-secret", SessionTTL: &ttl, SessionMaxAge: &maxAge}, Storage: config.StorageConfig{AppsDir: t.TempDir()}}
	srv := api.New(cfg, store, nil, nil)
	_, uid := seedUserAndJWT(t, store, "alice", "admin")
	original := time.Now().Add(-3*time.Hour - 55*time.Minute).Truncate(time.Second)
	token, err := auth.IssueJWTAt(uid, "alice", "admin", "test-secret", original)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/api/auth/me", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	req.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: "known-csrf"})
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("renew: %d %s", rec.Code, rec.Body.String())
	}
	// Decode explicitly named JSON times to verify the browser contract.
	var body map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	var timing struct {
		ExpiresAt    time.Time `json:"expires_at"`
		Deadline     time.Time `json:"absolute_expires_at"`
		RefreshAfter float64   `json:"refresh_after_seconds"`
	}
	if err := json.Unmarshal(body["session"], &timing); err != nil {
		t.Fatal(err)
	}
	deadline := original.Add(maxAge)
	if !timing.ExpiresAt.Equal(deadline) || !timing.Deadline.Equal(deadline) || timing.RefreshAfter != 200 {
		t.Fatalf("wrong timing: %+v", timing)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("session response may be cached")
	}
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == auth.SessionCookieName {
			claims, err := auth.ValidateJWT(cookie.Value, cfg.Auth.Secret, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !claims.ExpiresAt.Time.Equal(deadline) || !claims.AuthTime.Time.Equal(original) || !cookie.Expires.Equal(deadline) {
				t.Fatal("JWT, cookie, and metadata diverged")
			}
		}
		if cookie.Name == auth.CSRFCookieName && cookie.Value != "known-csrf" {
			t.Fatal("renewal rotated CSRF token")
		}
	}
	// Bearer credentials never turn into a renewable browser session.
	req = httptest.NewRequest("GET", "/api/auth/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("bearer /me: %d", rec.Code)
	}
	if bytes.Contains(rec.Body.Bytes(), []byte(`"session":`)) || hasCookie(rec.Result().Cookies(), auth.SessionCookieName) {
		t.Fatal("bearer was renewed")
	}
}

func TestBrowserSessionLoginReturnsTimingAndAlignedCookies(t *testing.T) {
	srv, store := newTestServer(t)
	seedUserAndJWT(t, store, "alice", "admin")
	req := httptest.NewRequest("POST", "/api/auth/session", bytes.NewBufferString(`{"username":"alice","password":"seed-password"}`))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("login: %d %s", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"session":`)) {
		t.Fatal("login missing renewal timing")
	}
	if !hasCookie(rec.Result().Cookies(), auth.CSRFCookieName) {
		t.Fatal("login missing CSRF cookie for immediate mutations")
	}
}

// A renewal can reach the browser after its logout request was sent with an
// older cookie. Every version must stay revoked, including after that older
// token's expiry would otherwise allow the revocation row to be pruned.
func TestBrowserSessionLogoutRevokesConcurrentRenewal(t *testing.T) {
	for _, scenario := range []struct {
		name, endpoint string
		bearer         bool
	}{
		{"browser logout", "/api/auth/logout", false},
		{"browser handoff", "/api/auth/handoff", false},
		{"browser JWT presented as bearer", "/api/auth/logout", true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			srv, store := newTestServer(t)
			original, _ := seedUserAndJWT(t, store, "alice", "admin")
			old, err := auth.ValidateJWT(original, "test-secret", nil)
			if err != nil {
				t.Fatal(err)
			}
			// Make the outgoing cookie older than its renewal, retaining the
			// random session ID from the original login.
			issued := time.Now().Add(-30 * time.Minute)
			old.AuthTime = jwt.NewNumericDate(issued)
			old.IssuedAt = jwt.NewNumericDate(issued)
			old.ExpiresAt = jwt.NewNumericDate(issued.Add(time.Hour))
			original, err = jwt.NewWithClaims(jwt.SigningMethodHS256, old).SignedString([]byte("test-secret"))
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest("GET", "/api/auth/me", nil)
			req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: original})
			rec := httptest.NewRecorder()
			srv.Router().ServeHTTP(rec, req)
			if rec.Code != 200 {
				t.Fatalf("renew: %d", rec.Code)
			}
			var renewed string
			for _, c := range rec.Result().Cookies() {
				if c.Name == auth.SessionCookieName {
					renewed = c.Value
				}
			}
			claims, err := auth.ValidateJWT(renewed, "test-secret", nil)
			if err != nil {
				t.Fatal(err)
			}
			if claims.ID != old.ID {
				t.Fatal("renewal changed session identity")
			}
			if renewed == original || !claims.ExpiresAt.After(old.ExpiresAt.Time) {
				t.Fatal("renewal did not extend the older token")
			}
			req = httptest.NewRequest("POST", scenario.endpoint, nil)
			req.Header.Set("Origin", "http://example.com")
			req.Header.Set("Referer", "http://example.com/")
			req.Header.Set("X-CSRF-Token", "csrf")
			req.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: "csrf"})
			if scenario.bearer {
				req.Header.Set("Authorization", "Bearer "+original)
			} else {
				req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: original})
			}
			rec = httptest.NewRecorder()
			srv.Router().ServeHTTP(rec, req)
			if rec.Code != 204 && rec.Code != 303 {
				t.Fatalf("logout: %d %s", rec.Code, rec.Body.String())
			}
			// Emulate expiration of the old token's revocation retention window by
			// checking the stored deadline, rather than sleeping for an hour.
			var until int64
			if err := store.DB().QueryRow(`SELECT expires_at FROM revoked_tokens WHERE jti = ?`, old.ID).Scan(&until); err != nil {
				t.Fatal(err)
			}
			if until < old.AuthTime.Time.Add(auth.AbsoluteSessionMaxAge).Unix() {
				t.Fatal("browser revocation ends before the absolute deadline")
			}
			if _, err := auth.ValidateJWT(renewed, "test-secret", store.IsTokenRevoked); err == nil {
				t.Fatal("renewal survived logout")
			}
			req = httptest.NewRequest("GET", "/api/auth/me", nil)
			req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: renewed})
			rec = httptest.NewRecorder()
			srv.Router().ServeHTTP(rec, req)
			if rec.Code != 401 {
				t.Fatalf("revoked renewal /me = %d", rec.Code)
			}
		})
	}
}

func TestBrowserSessionHandoffClearsDeletedAccountCookie(t *testing.T) {
	srv, store := newTestServer(t)
	token, uid := seedUserAndJWT(t, store, "deleted", "viewer")
	if err := store.DeleteUser(uid); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/auth/handoff", nil)
	req.Header.Set("Origin", "http://example.com")
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("deleted-account handoff = %d %s", rec.Code, rec.Body.String())
	}
	if !hasCookie(rec.Result().Cookies(), auth.SessionCookieName) {
		t.Fatal("deleted-account cookie was not cleared")
	}
}
