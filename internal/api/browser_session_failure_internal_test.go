package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

func TestBrowserSessionRevocationWriteFailureKeepsSession(t *testing.T) {
	for _, endpoint := range []string{"/api/auth/logout", "/api/auth/handoff"} {
		t.Run(endpoint, func(t *testing.T) {
			store := dbtest.New(t)
			if err := store.CreateUser(db.CreateUserParams{Username: "alice", PasswordHash: "unused", Role: "admin"}); err != nil {
				t.Fatal(err)
			}
			user, err := store.GetUserByUsername("alice")
			if err != nil {
				t.Fatal(err)
			}
			cfg := &config.Config{Auth: config.AuthConfig{Secret: "test-secret"}, Storage: config.StorageConfig{AppsDir: t.TempDir()}}
			srv := New(cfg, store, nil, nil)
			token, _, err := auth.IssueBrowserSession(user.ContextUser(), cfg.Auth.Secret, time.Time{}, time.Hour, 12*time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			drop := installDBFailureTrigger(t, store, dbFailureTrigger{name: "fail_session_revocation", table: "revoked_tokens", event: "INSERT", condition: "true"})
			request := func() *httptest.ResponseRecorder {
				req := httptest.NewRequest("POST", endpoint, nil)
				req.Header.Set("Origin", "http://example.com")
				req.Header.Set("Referer", "http://example.com/")
				req.Header.Set("X-CSRF-Token", "csrf")
				req.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: "csrf"})
				req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
				rec := httptest.NewRecorder()
				srv.Router().ServeHTTP(rec, req)
				return rec
			}
			rec := request()
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("revocation failure = %d %s", rec.Code, rec.Body.String())
			}
			for _, c := range rec.Result().Cookies() {
				if c.Name == auth.SessionCookieName {
					t.Fatal("failed revocation discarded the cookie before a retry")
				}
			}
			if _, err := auth.ValidateJWT(token, cfg.Auth.Secret, store.IsTokenRevoked); err != nil {
				t.Fatalf("failed logout invalidated session: %v", err)
			}
			drop()
			rec = request()
			if rec.Code != http.StatusNoContent && rec.Code != http.StatusSeeOther {
				t.Fatalf("retry = %d %s", rec.Code, rec.Body.String())
			}
			if _, err := auth.ValidateJWT(token, cfg.Auth.Secret, store.IsTokenRevoked); err == nil {
				t.Fatal("successful retry did not revoke session")
			}
		})
	}
}
