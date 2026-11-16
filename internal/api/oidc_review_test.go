package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"golang.org/x/oauth2"
)

func TestOIDCLogoutCorruptHintStillRevokesLocally(t *testing.T) {
	srv, store, cfg := oidcMigrationServer(t, map[string]any{"sub": "a"}, config.OIDCConfig{EndSession: true})
	u, err := store.GetUserByUsername("recovery")
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := auth.IssueBrowserSession(u.ContextUser(), cfg.Auth.Secret, time.Now(), time.Hour, 12*time.Hour, "broken-hint")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveOIDCSession(db.OIDCSession{JTI: "broken-hint", UserID: u.ID, Issuer: cfg.OAuth.OIDC.IssuerURL, SID: "sid", IDToken: "not-encrypted", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	req := authedRequest(t, "POST", "/api/auth/logout", nil, token)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "id_token_hint") {
		t.Fatalf("logout failed: %d %s", rec.Code, rec.Body.String())
	}
	revoked, err := store.IsTokenRevoked("broken-hint")
	if err != nil || !revoked {
		t.Fatalf("not revoked: %v %v", revoked, err)
	}
	if _, err := store.GetOIDCSession("broken-hint"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("hint retained: %v", err)
	}
}

func TestOIDCAutomaticLinkRefusesPrivilegedAndConflictingUsers(t *testing.T) {
	for _, kind := range []string{"admin", "manual", "password", "github", "service", "ambiguous"} {
		t.Run(kind, func(t *testing.T) {
			claims := map[string]any{"sub": "incoming-sub", "corp": "target", "groups": []string{"power"}}
			srv, store, _ := oidcMigrationServer(t, claims, config.OIDCConfig{UsernameClaim: "corp", UsernameRequired: true, LinkExisting: "username"})
			var err error
			switch kind {
			case "admin":
				claims["corp"] = "recovery"
			case "password":
				err = store.CreateUser(db.CreateUserParams{Username: "target", PasswordHash: "$2a$local", Role: "viewer"})
			case "service":
				var u *db.User
				u, err = store.UpsertSystemUser(db.SystemUsernameDeploy, "developer")
				if err == nil {
					claims["corp"] = u.Username
				}
			default:
				var u *auth.ContextUser
				u, err = store.CreateForwardAuthUser("target", "viewer")
				if err == nil && kind == "manual" {
					err = store.SetManualRole(u.ID, "developer")
				}
				if err == nil && kind == "github" {
					err = store.CreateOAuthAccount(db.CreateOAuthAccountParams{UserID: u.ID, Provider: "github", ProviderID: "other-sub"})
				}
				if err == nil && kind == "ambiguous" {
					_, err = store.CreateForwardAuthUser("TARGET", "viewer")
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			rec := oidcMigrationLogin(t, srv, claims)
			if rec.Code != 409 {
				t.Fatalf("unsafe link: %d %s", rec.Code, rec.Body.String())
			}
			if n, err := store.CountAuditEvents("login_refused"); err != nil || n != 1 {
				t.Fatalf("refusal audit missing: %d %v", n, err)
			}
			if _, err := store.GetUserByOAuthAccount("oidc", "incoming-sub"); !errors.Is(err, db.ErrNotFound) {
				t.Fatalf("link committed: %v", err)
			}
		})
	}
}

func TestOIDCStrictClaimsRefuseWithoutProvisioning(t *testing.T) {
	for _, tc := range []struct {
		name, policy     string
		groups           any
		present, require bool
		status           int
	}{
		{name: "absent", policy: "reject", status: 403},
		{name: "empty", policy: "empty", groups: []string{}, present: true, require: true, status: 403},
		{name: "malformed reject", policy: "reject", groups: 42, present: true, status: 502},
		{name: "malformed empty", policy: "empty", groups: 42, present: true, status: 502},
	} {
		t.Run(tc.name, func(t *testing.T) {
			claims := map[string]any{"sub": "refused"}
			if tc.present {
				claims["groups"] = tc.groups
			}
			srv, store, _ := oidcMigrationServer(t, claims, config.OIDCConfig{MissingGroups: tc.policy, RequireGroups: tc.require})
			rec := oidcMigrationLogin(t, srv, claims)
			if rec.Code != tc.status {
				t.Fatalf("status=%d %s", rec.Code, rec.Body.String())
			}
			if _, err := store.GetUserByOAuthAccount("oidc", "refused"); !errors.Is(err, db.ErrNotFound) {
				t.Fatal("refused login provisioned")
			}
		})
	}
}

func TestOIDCFrontChannelNegativeAndRateLimit(t *testing.T) {
	claims := map[string]any{"sub": "a", "sid": "actual"}
	srv, store, cfg := oidcMigrationServer(t, claims, config.OIDCConfig{EndSession: true})
	cookie := oidcMigrationCookie(t, oidcMigrationLogin(t, srv, claims))
	parsed, _ := auth.ValidateJWT(cookie.Value, cfg.Auth.Secret, nil)
	for _, tc := range []struct {
		query  string
		status int
	}{
		{"iss=" + url.QueryEscape(cfg.OAuth.OIDC.IssuerURL) + "&sid=other", 200},
		{"iss=https://wrong.example&sid=actual", 400},
		{"iss=" + url.QueryEscape(cfg.OAuth.OIDC.IssuerURL), 400},
	} {
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, httptest.NewRequest("GET", "/api/auth/oidc/frontchannel-logout?"+tc.query, nil))
		if rec.Code != tc.status {
			t.Fatalf("status=%d", rec.Code)
		}
	}
	revoked, err := store.IsTokenRevoked(parsed.ID)
	if err != nil || revoked {
		t.Fatal("unrelated session revoked")
	}
	limited := false
	for range 610 {
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, httptest.NewRequest("GET", "/api/auth/oidc/frontchannel-logout?iss="+url.QueryEscape(cfg.OAuth.OIDC.IssuerURL)+"&sid=unknown", nil))
		if rec.Code == 429 {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("frontchannel endpoint has no rate limit")
	}
	// A logout-notification burst must not consume corporate login/callback capacity.
	for range 25 {
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, httptest.NewRequest("GET", "/api/auth/oidc/login", nil))
		if rec.Code != 302 {
			t.Fatalf("login shares exhausted limiter: %d", rec.Code)
		}
	}
	callback := httptest.NewRecorder()
	srv.Router().ServeHTTP(callback, httptest.NewRequest("GET", "/api/auth/oidc/callback", nil))
	if callback.Code != 400 {
		t.Fatalf("callback shares exhausted limiter: %d", callback.Code)
	}

	cfg.OAuth.OIDC.EndSession = false
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/auth/oidc/frontchannel-logout", nil)
	req.RemoteAddr = "192.0.2.22:1234"
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Fatalf("disabled endpoint: %d", rec.Code)
	}
}

type oidcGraphTransport func(*http.Request) (*http.Response, error)

func (f oidcGraphTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestOIDCGraphOverageCallback(t *testing.T) {
	for _, status := range []int{200, 403} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			claims := map[string]any{"sub": "graph-user", "hasgroups": true}
			srv, store, _ := oidcMigrationServer(t, claims, config.OIDCConfig{GraphGroups: true, MissingGroups: "reject", RequireGroups: true}, func(cfg *config.Config) {
				cfg.Auth.GroupRoleMappings = []config.GroupRoleMapping{{Group: "11111111-1111-1111-1111-111111111111", Role: "operator"}}
			})
			client := &http.Client{Transport: oidcGraphTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != "graph.microsoft.com" {
					return http.DefaultTransport.RoundTrip(r)
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"value":[{"id":"11111111-1111-1111-1111-111111111111","@odata.type":"#microsoft.graph.group"}]}`))}, nil
			})}
			rec := oidcMigrationLogin(t, srv, claims, context.WithValue(context.Background(), oauth2.HTTPClient, client))
			if status == 200 {
				if rec.Code != 302 {
					t.Fatalf("callback=%d %s", rec.Code, rec.Body.String())
				}
				u, err := store.GetUserByOAuthAccount("oidc", "graph-user")
				if err != nil || u.Role != "operator" {
					t.Fatalf("group resolution failed: %v %v", u, err)
				}
			} else {
				if rec.Code != 502 {
					t.Fatalf("failed Graph accepted: %d", rec.Code)
				}
				if _, err := store.GetUserByOAuthAccount("oidc", "graph-user"); !errors.Is(err, db.ErrNotFound) {
					t.Fatal("Graph failure provisioned user")
				}
			}
		})
	}
}

func TestOIDCAuthorityMismatchRefusesCallback(t *testing.T) {
	claims := map[string]any{"sub": "wrong-authority"}
	srv, store, _ := oidcMigrationServer(t, claims, config.OIDCConfig{})
	if err := store.BindOIDCProvider("https://other.example", "other"); err != nil {
		t.Fatal(err)
	}
	rec := oidcMigrationLogin(t, srv, claims)
	if rec.Code != 503 {
		t.Fatalf("authority mismatch accepted: %d", rec.Code)
	}
}

func TestOIDCProvidersAutoRedirectOnlyForSoleNativeLogin(t *testing.T) {
	for _, other := range []string{"none", "local", "github", "google", "forward"} {
		t.Run(other, func(t *testing.T) {
			srv, _, _ := oidcMigrationServer(t, map[string]any{"sub": "a"}, config.OIDCConfig{AutoRedirect: true}, func(cfg *config.Config) {
				local := other == "local"
				cfg.Auth.LocalLogin = &local
				switch other {
				case "github":
					cfg.OAuth.GitHub.ClientID = "github"
					cfg.OAuth.GitHub.ClientSecret = "secret"
				case "google":
					cfg.OAuth.Google.ClientID = "google"
					cfg.OAuth.Google.ClientSecret = "secret"
				case "forward":
					cfg.Auth.ForwardAuth.Enabled = true
				}
			})
			rec := httptest.NewRecorder()
			srv.Router().ServeHTTP(rec, httptest.NewRequest("GET", "/api/auth/providers", nil))
			var out struct {
				OIDC struct {
					AutoRedirect bool `json:"auto_redirect"`
				} `json:"oidc"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			if out.OIDC.AutoRedirect != (other == "none") {
				t.Fatalf("automatic login wrong for %s: %s", other, rec.Body.String())
			}
		})
	}
}

func TestOIDCMalformedClaimDoesNotChangeStoredAuthorization(t *testing.T) {
	for _, policy := range []string{"reject", "empty"} {
		t.Run(policy, func(t *testing.T) {
			claims := map[string]any{"sub": "existing", "groups": []string{"power"}}
			srv, store, _ := oidcMigrationServer(t, claims, config.OIDCConfig{MissingGroups: policy})
			if rec := oidcMigrationLogin(t, srv, claims); rec.Code != 302 {
				t.Fatal(rec.Body.String())
			}
			claims["groups"] = 42
			if rec := oidcMigrationLogin(t, srv, claims); rec.Code != 502 {
				t.Fatalf("malformed status=%d", rec.Code)
			}
			u, err := store.GetUserByOAuthAccount("oidc", "existing")
			if err != nil {
				t.Fatal(err)
			}
			groups, err := store.GetUserGroups(u.ID)
			if err != nil || u.Role != "operator" || len(groups) != 1 || groups[0] != "power" {
				t.Fatalf("snapshot changed: %v %v %v", u, groups, err)
			}
		})
	}
}

func TestOIDCGraphRequiresObjectIDGroupFormat(t *testing.T) {
	claims := map[string]any{"sub": "named-groups", "groups": []string{"DOMAIN\\power"}}
	srv, store, _ := oidcMigrationServer(t, claims, config.OIDCConfig{GraphGroups: true})
	rec := oidcMigrationLogin(t, srv, claims)
	if rec.Code != 502 {
		t.Fatalf("named groups accepted: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := store.GetUserByOAuthAccount("oidc", "named-groups"); !errors.Is(err, db.ErrNotFound) {
		t.Fatal("misconfigured login provisioned user")
	}
}
