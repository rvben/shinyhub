package auth_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/auth"
)

func TestBrowserSessionStrictDeadline(t *testing.T) {
	original := time.Now().Add(-11*time.Hour - 55*time.Minute).Truncate(time.Second)
	u := &auth.ContextUser{ID: 1, Username: "alice", Role: "admin", TokenEpoch: 3}
	token, info, err := auth.IssueBrowserSession(u, "secret", original, time.Hour, 12*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := auth.ValidateJWT(token, "secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	deadline := original.Add(12 * time.Hour)
	if !claims.ExpiresAt.Time.Equal(deadline) || !info.ExpiresAt.Equal(deadline) {
		t.Fatal("renewal escaped the absolute deadline")
	}
	if !claims.AuthTime.Time.Equal(original) || claims.SessionEpoch != 3 {
		t.Fatal("renewal lost original login or epoch")
	}
	req := httptest.NewRequest("GET", "/api/auth/me", nil)
	req.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: "existing-csrf"})
	rec := httptest.NewRecorder()
	auth.SetSessionCookieUntil(rec, req, token, info.ExpiresAt, nil)
	session := setCookie(t, rec, auth.SessionCookieName)
	csrf := setCookie(t, rec, auth.CSRFCookieName)
	if !session.Expires.Equal(deadline) || session.MaxAge > 300 || session.MaxAge < 298 {
		t.Fatalf("wrong cookie deadline: %+v", session)
	}
	if csrf.Value != "existing-csrf" || !csrf.Expires.Equal(session.Expires) || csrf.MaxAge != session.MaxAge {
		t.Fatal("CSRF was rotated or did not share session lifetime")
	}
}

func TestBrowserSessionCannotRenewExpiredOrSupportSession(t *testing.T) {
	u := &auth.ContextUser{ID: 1}
	_, _, err := auth.IssueBrowserSession(u, "secret", time.Now().Add(-12*time.Hour), time.Hour, 12*time.Hour)
	if !errors.Is(err, auth.ErrSessionExpired) {
		t.Fatalf("expired renewal: %v", err)
	}
	u.SupportSession = &auth.SupportSessionContext{}
	_, _, err = auth.IssueBrowserSession(u, "secret", time.Time{}, time.Hour, 12*time.Hour)
	if !errors.Is(err, auth.ErrSupportSessionScope) {
		t.Fatalf("support renewal: %v", err)
	}
}

func TestBrowserSessionCustomTTLAndLegacyLoginTime(t *testing.T) {
	before := time.Now().Truncate(time.Second)
	_, info, err := auth.IssueBrowserSession(&auth.ContextUser{ID: 1}, "secret", time.Time{}, 10*time.Minute, 4*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if info.AuthTime.Before(before) || info.ExpiresAt.Sub(before) < 10*time.Minute || info.ExpiresAt.Sub(before) > 10*time.Minute+time.Second {
		t.Fatalf("wrong custom expiry: %+v", info)
	}
}
