package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
	"github.com/rvben/shinyhub/internal/oauth"
)

func oidcMigrationServer(t *testing.T, claims map[string]any, o config.OIDCConfig, configure ...func(*config.Config)) (*api.Server, *db.Store, *config.Config) {
	t.Helper()
	idp := newMockIdP(t, "shinyhub", claims)
	store := dbtest.New(t)
	if err := store.CreateUser(db.CreateUserParams{Username: "recovery", PasswordHash: "!disabled", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	o.IssuerURL = idp.URL
	o.ClientID = "shinyhub"
	o.ClientSecret = "secret"
	o.CallbackURL = "https://app.example.test/api/auth/oidc/callback"
	cfg := &config.Config{OAuth: config.OAuthConfig{OIDC: o}, Auth: config.AuthConfig{Secret: "test-secret", OAuthDefaultRole: "viewer", GroupRoleMappings: []config.GroupRoleMapping{{Group: "power", Role: "operator"}}}, Storage: config.StorageConfig{AppsDir: t.TempDir(), AppDataDir: t.TempDir()}}
	for _, f := range configure {
		f(cfg)
	}
	provider, err := oauth.NewOIDCProvider(context.Background(), idp.URL, "shinyhub", "secret", o.CallbackURL, "SSO", o.GroupsClaim, "")
	if err != nil {
		t.Fatal(err)
	}
	srv := api.New(cfg, store, nil, nil)
	srv.SetOIDCProvider(provider)
	return srv, store, cfg
}
func oidcMigrationLogin(t *testing.T, srv *api.Server, claims map[string]any, contexts ...context.Context) *httptest.ResponseRecorder {
	t.Helper()
	start := httptest.NewRecorder()
	srv.Router().ServeHTTP(start, httptest.NewRequest("GET", "/api/auth/oidc/login?next=%2Fapp%2Fdemo%2F", nil))
	loc, err := start.Result().Location()
	if err != nil {
		t.Fatal(err)
	}
	claims["nonce"] = loc.Query().Get("nonce")
	req := httptest.NewRequest("GET", "/api/auth/oidc/callback?code=code&state="+url.QueryEscape(loc.Query().Get("state")), nil)
	for _, c := range start.Result().Cookies() {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	if len(contexts) > 0 {
		req = req.WithContext(contexts[0])
	}
	srv.Router().ServeHTTP(rec, req)
	return rec
}
func oidcMigrationCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.SessionCookieName && c.Value != "" {
			return c
		}
	}
	t.Fatalf("no session cookie: %d %s", rec.Code, rec.Body.String())
	return nil
}
func TestOIDCMigrationLinksExistingAccountAndPreservesDeepLink(t *testing.T) {
	claims := map[string]any{"sub": "sub-a", "corp": "ABC12345", "roles": []string{"power"}}
	srv, store, _ := oidcMigrationServer(t, claims, config.OIDCConfig{UsernameClaim: "corp", UsernameRequired: true, LinkExisting: "username", GroupsClaim: "roles", MissingGroups: "empty", RequireGroups: true})
	old, err := store.CreateForwardAuthUser("abc12345", "viewer")
	if err != nil {
		t.Fatal(err)
	}
	rec := oidcMigrationLogin(t, srv, claims)
	if rec.Code != 302 || rec.Header().Get("Location") != "/app/demo/" {
		t.Fatalf("callback %d %s", rec.Code, rec.Body.String())
	}
	linked, err := store.GetUserByOAuthAccount("oidc", "sub-a")
	if err != nil || linked.ID != old.ID || linked.Role != "operator" {
		t.Fatalf("linked=%v err=%v", linked, err)
	}
	cookie := oidcMigrationCookie(t, rec)
	parsed, err := auth.ValidateJWT(cookie.Value, "test-secret", nil)
	if err != nil || parsed.UserID != old.ID {
		t.Fatalf("session changed identity: %v", err)
	}
	delete(claims, "roles")
	rec = oidcMigrationLogin(t, srv, claims)
	if rec.Code != 403 {
		t.Fatalf("last-role removal accepted: %d", rec.Code)
	}
}
func TestOIDCMissingClaimsClearSnapshotsWhenConfigured(t *testing.T) {
	claims := map[string]any{"sub": "sub-a", "email": "person@example.com", "groups": []string{"power"}}
	srv, store, _ := oidcMigrationServer(t, claims, config.OIDCConfig{MissingGroups: "empty"})
	if rec := oidcMigrationLogin(t, srv, claims); rec.Code != 302 {
		t.Fatal(rec.Body.String())
	}
	delete(claims, "groups")
	if rec := oidcMigrationLogin(t, srv, claims); rec.Code != 302 {
		t.Fatal(rec.Body.String())
	}
	u, err := store.GetUserByOAuthAccount("oidc", "sub-a")
	if err != nil {
		t.Fatal(err)
	}
	groups, err := store.GetUserGroups(u.ID)
	if err != nil || len(groups) != 0 || u.Role != "viewer" {
		t.Fatalf("stale authorization: %v %v %v", u, groups, err)
	}
}
func TestOIDCMigrationRefusesOverageAndMalformedRequiredUsername(t *testing.T) {
	for _, claims := range []map[string]any{
		{"sub": "a", "corp": 42},
		{"sub": "a", "corp": "abc12345", "_claim_names": map[string]string{"groups": "src1"}, "_claim_sources": map[string]any{"src1": map[string]string{"endpoint": "https://graph.windows.net/unused"}}},
		{"sub": "a"},
	} {
		srv, store, _ := oidcMigrationServer(t, claims, config.OIDCConfig{UsernameClaim: "corp", UsernameRequired: true})
		rec := oidcMigrationLogin(t, srv, claims)
		if rec.Code != 403 {
			t.Fatalf("unsafe login accepted: %d %s", rec.Code, rec.Body.String())
		}
		if _, err := store.GetUserByOAuthAccount("oidc", "a"); !errors.Is(err, db.ErrNotFound) {
			t.Fatalf("refused login provisioned user: %v", err)
		}
	}
}
func TestOIDCLogoutCorrelatesAndRevokesSessions(t *testing.T) {
	claims := map[string]any{"sub": "sub-a", "email": "person@example.com", "sid": "idp-session"}
	srv, store, cfg := oidcMigrationServer(t, claims, config.OIDCConfig{EndSession: true})
	rec := oidcMigrationLogin(t, srv, claims)
	cookie := oidcMigrationCookie(t, rec)
	parsed, err := auth.ValidateJWT(cookie.Value, "test-secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := store.GetOIDCSession(parsed.ID)
	if err != nil || strings.Contains(stored.IDToken, "sub-a") {
		t.Fatalf("encrypted hint not stored: %v", err)
	}
	if !stored.ExpiresAt.Equal(parsed.AuthTime.Time.Add(cfg.Auth.BrowserSessionMaxAge()).Truncate(time.Second)) {
		t.Fatal("logout revocation deadline differs from the browser session deadline")
	}
	// Front-channel does not depend on third-party cookies being available.
	request := httptest.NewRequest("GET", "/api/auth/oidc/frontchannel-logout?iss="+url.QueryEscape(cfg.OAuth.OIDC.IssuerURL)+"&sid=idp-session", nil)
	rec = httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, request)
	if rec.Code != 200 {
		t.Fatalf("frontchannel: %d %s", rec.Code, rec.Body.String())
	}
	revoked, err := store.IsTokenRevoked(parsed.ID)
	if err != nil || !revoked {
		t.Fatalf("session survived logout: %v %v", revoked, err)
	}
	// A fresh login can end at the provider through the ordinary POST logout.
	rec = oidcMigrationLogin(t, srv, claims)
	cookie = oidcMigrationCookie(t, rec)
	parsed, _ = auth.ValidateJWT(cookie.Value, "test-secret", nil)
	req := authedRequest(t, "POST", "/api/auth/logout", nil, cookie.Value)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("RP logout: %d %s", rec.Code, rec.Body.String())
	}
	var out map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	loc, err := url.Parse(out["logout_url"])
	if err != nil || loc.Query().Get("id_token_hint") == "" || loc.Query().Get("post_logout_redirect_uri") != "https://app.example.test/?signed_out=1" {
		t.Fatalf("invalid logout redirect: %v %v", loc, err)
	}
	revoked, err = store.IsTokenRevoked(parsed.ID)
	if err != nil || !revoked {
		t.Fatal("local session was not revoked before RP logout")
	}
}

func TestOIDCAdminLinkPreviewAndConflict(t *testing.T) {
	srv, store, cfg := oidcMigrationServer(t, map[string]any{"sub": "a"}, config.OIDCConfig{})
	target, err := store.CreateForwardAuthUser("existing", "developer")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := store.GetUserByUsername("recovery")
	if err != nil {
		t.Fatal(err)
	}
	token, err := auth.IssueJWT(admin.ID, admin.Username, "admin", cfg.Auth.Secret)
	if err != nil {
		t.Fatal(err)
	}
	for _, preview := range []bool{true, false, false} {
		body, _ := json.Marshal(map[string]any{"sub": "verified-sub", "issuer": cfg.OAuth.OIDC.IssuerURL, "client_id": "shinyhub", "dry_run": preview})
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, authedRequest(t, "POST", fmt.Sprintf("/api/users/%d/link-oidc", target.ID), body, token))
		if rec.Code != 200 {
			t.Fatalf("preview=%v status=%d body=%s", preview, rec.Code, rec.Body.String())
		}
		linked, err := store.GetUserByOAuthAccount("oidc", "verified-sub")
		if preview {
			if !errors.Is(err, db.ErrNotFound) {
				t.Fatal("preview wrote identity")
			}
		} else if err != nil || linked.ID != target.ID {
			t.Fatalf("wrong link %v %v", linked, err)
		}
	}
	body, _ := json.Marshal(map[string]any{"sub": "other-sub", "issuer": cfg.OAuth.OIDC.IssuerURL, "client_id": "shinyhub"})
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, authedRequest(t, "POST", fmt.Sprintf("/api/users/%d/link-oidc", target.ID), body, token))
	if rec.Code != 409 {
		t.Fatalf("relink status %d", rec.Code)
	}
}

func TestOIDCStrictPolicyRefusesRetainedLastAdminRole(t *testing.T) {
	claims := map[string]any{"sub": "sub-a", "corp": "recovery", "groups": []string{}}
	srv, store, _ := oidcMigrationServer(t, claims, config.OIDCConfig{UsernameClaim: "corp", UsernameRequired: true, MissingGroups: "empty"})
	if _, _, err := store.LinkOIDCUser("recovery", "sub-a", false); err != nil {
		t.Fatal(err)
	}
	rec := oidcMigrationLogin(t, srv, claims)
	if rec.Code != 403 {
		t.Fatalf("last admin guard allowed stale admin login: %d %s", rec.Code, rec.Body.String())
	}
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == auth.SessionCookieName && cookie.Value != "" {
			t.Fatal("denied login issued cookie")
		}
	}
}
