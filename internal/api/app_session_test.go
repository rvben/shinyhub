package api_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/access"
	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

func TestHostedBrowserRenewalChecksIdentityAndPermissions(t *testing.T) {
	store := dbtest.New(t)
	ttl, maxAge := 10*time.Minute, 2*time.Hour
	cfg := &config.Config{Auth: config.AuthConfig{Secret: "test-secret", SessionTTL: &ttl, SessionMaxAge: &maxAge}, Storage: config.StorageConfig{AppsDir: t.TempDir()}}
	srv := api.New(cfg, store, nil, nil)
	_, owner := seedUserAndJWT(t, store, "owner", "admin")
	_, viewer := seedUserAndJWT(t, store, "viewer", "viewer")
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "session-app", Name: "Session", OwnerID: owner}); err != nil {
		t.Fatal(err)
	}
	if err := store.GrantAppAccess("session-app", viewer); err != nil {
		t.Fatal(err)
	}
	u, err := store.LookupContextUser(viewer)
	if err != nil {
		t.Fatal(err)
	}
	original := time.Now().Add(-110 * time.Minute).Truncate(time.Second)
	token, info, err := auth.IssueBrowserSession(u, cfg.Auth.Secret, original, ttl, maxAge)
	if err != nil {
		t.Fatal(err)
	}
	handler := access.Middleware(store, cfg.Auth.Secret, store.IsTokenRevoked, store.LookupContextUser)(http.HandlerFunc(srv.HandleAppSession))
	request := func(cookie string, forward *auth.ContextUser) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("GET", "/app/session-app/.shinyhub/session.json", nil)
		// Apps may forward their own Authorization header. It must not select
		// dashboard/API bearer authentication or override the browser cookie.
		r.Header.Set("Authorization", "Bearer app-owned-credential")
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: cookie})
		}
		if forward != nil {
			r = r.WithContext(auth.WithUser(r.Context(), forward))
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, r)
		return rec
	}
	rec := request(token, nil)
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("renewal: %d %s", rec.Code, rec.Body.String())
	}
	if bytes.Contains(rec.Body.Bytes(), []byte("username")) || bytes.Contains(rec.Body.Bytes(), []byte("admin")) || bytes.Contains(rec.Body.Bytes(), []byte("token")) {
		t.Fatal("app endpoint exposed dashboard account details")
	}
	var renewed string
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.SessionCookieName {
			renewed = c.Value
		}
	}
	claims, err := auth.ValidateJWT(renewed, cfg.Auth.Secret, nil)
	if err != nil || claims.ID != info.JTI || !claims.AuthTime.Time.Equal(original) || !claims.ExpiresAt.Time.Equal(original.Add(maxAge)) {
		t.Fatalf("changed session: %+v %v", claims, err)
	}
	if rec := request("", u); rec.Code != 200 || hasCookie(rec.Result().Cookies(), auth.SessionCookieName) {
		t.Fatal("forward auth was minted a native session")
	}
	if rec := request("", nil); rec.Code != 401 {
		t.Fatalf("anonymous renewal: %d", rec.Code)
	}
	if err := store.RevokeAppAccess("session-app", viewer); err != nil {
		t.Fatal(err)
	}
	if rec := request(token, nil); rec.Code != 403 || hasCookie(rec.Result().Cookies(), auth.SessionCookieName) {
		t.Fatalf("renewed revoked app access: %d", rec.Code)
	}
}
