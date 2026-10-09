package proxy_test

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
	"github.com/rvben/shinyhub/internal/identity"
	"github.com/rvben/shinyhub/internal/proxy"
)

func TestForwardAuth_UTF8HTTPRoundTrip(t *testing.T) {
	store := dbtest.New(t)
	backend, captured := startChainBackend(t)
	const secret = "identity-secret"
	const username = "ćirić"
	const name = "Ana Ćirić 李 伟"
	const email = "ćirić@example.com"
	const group = "gg-équipe"
	const appID = 42
	p := proxy.New()
	p.SetPoolSize("demo", 1)
	p.SetPoolAppID("demo", appID)
	p.SetPoolIdentityHeaders("demo", true)
	provider := identity.NewProvider(secret, store)
	p.SetIdentityProvider(provider.PayloadFor)
	if err := p.RegisterReplica("demo", 0, backend.URL, nil, 1); err != nil {
		t.Fatal(err)
	}
	_, loopback, err := net.ParseCIDR("127.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	cfg := auth.ForwardAuthConfig{
		Enabled:           true,
		UserHeader:        "Remote-User",
		NameHeader:        "Remote-Name",
		EmailHeader:       "Remote-Email",
		GroupsHeader:      "Remote-Groups",
		SharedSecret:      "proxy-secret",
		SecretHeader:      "Proxy-Secret",
		DefaultRole:       "viewer",
		GroupRoleMappings: []auth.GroupRoleMapping{{Group: group, Role: "developer"}},
	}
	hub := httptest.NewServer(auth.ForwardAuthMiddleware(store, cfg, []*net.IPNet{loopback})(p))
	t.Cleanup(hub.Close)
	request := func(user, friendlyName, address string, groups []string) int {
		t.Helper()
		r, err := http.NewRequest(http.MethodGet, hub.URL+"/app/demo/", nil)
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set(cfg.UserHeader, user)
		r.Header.Set(cfg.NameHeader, friendlyName)
		r.Header.Set(cfg.EmailHeader, address)
		r.Header.Set(cfg.SecretHeader, cfg.SharedSecret)
		for _, g := range groups {
			r.Header.Add(cfg.GroupsHeader, g)
		}
		response, err := hub.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusBadRequest {
			t.Fatalf("status=%d body=%s", response.StatusCode, body)
		}
		return response.StatusCode
	}
	if status := request(username, name, email, []string{group}); status != http.StatusOK {
		t.Fatalf("valid UTF-8: status=%d", status)
	}
	user, err := store.GetUserByUsername(username)
	if err != nil {
		t.Fatal(err)
	}
	if user.DisplayName != name || user.Role != "developer" {
		t.Fatalf("stored user=%+v", user)
	}
	verify := func(wantEmail string) {
		t.Helper()
		headers := captured()
		for header, want := range map[string]string{identity.HeaderUser: username, identity.HeaderName: name, identity.HeaderEmail: wantEmail, identity.HeaderGroups: group, identity.HeaderRole: "developer"} {
			if got := headers.Get(header); got != want {
				t.Errorf("%s=%q, want %q", header, got, want)
			}
		}
		for _, header := range []string{cfg.UserHeader, cfg.NameHeader, cfg.EmailHeader, cfg.GroupsHeader, cfg.SecretHeader} {
			if headers.Get(header) != "" {
				t.Errorf("ingress header %s reached app", header)
			}
		}
		claims := &identity.TokenClaims{}
		token, err := jwt.ParseWithClaims(headers.Get(identity.HeaderToken), claims, func(*jwt.Token) (any, error) { return identity.DeriveKey(secret, appID), nil }, jwt.WithValidMethods([]string{"HS256"}), jwt.WithIssuer(identity.Issuer), jwt.WithAudience("demo"))
		if err != nil || !token.Valid {
			t.Fatalf("verify identity token: %v", err)
		}
		if claims.Subject != strconv.FormatInt(user.ID, 10) || claims.PreferredUsername != username || claims.Name != name || claims.Email != wantEmail || claims.Role != "developer" || !reflect.DeepEqual(claims.Groups, []string{group}) {
			t.Fatalf("token claims=%+v", claims)
		}
	}
	verify(email)
	// An unreadable group assertion must neither revoke existing membership nor
	// allow the request using the old role; a new user must not be provisioned.
	before := captured()
	for _, assertedUser := range []string{username, "new-user"} {
		if status := request(assertedUser, "Replacement name", email, []string{group, "bad\xe9"}); status != http.StatusBadRequest {
			t.Fatalf("invalid groups: status=%d", status)
		}
	}
	if status := request("bad\xe9", name, email, []string{group}); status != http.StatusBadRequest {
		t.Fatalf("invalid username: status=%d", status)
	}
	if !reflect.DeepEqual(before, captured()) {
		t.Fatal("rejected identity reached backend")
	}
	if _, err := store.GetUserByUsername("new-user"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("invalid group assertion provisioned user: %v", err)
	}
	stored, err := store.GetUserByUsername(username)
	if err != nil {
		t.Fatal(err)
	}
	groups, err := store.GetUserGroups(user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.DisplayName != name || stored.Role != "developer" || !reflect.DeepEqual(groups, []string{group}) {
		t.Fatalf("rejection mutated identity: user=%+v groups=%v", stored, groups)
	}
	// An invalid email must not expose a previously stored email in headers or
	// claims. The request still succeeds and keeps the stored display name.
	if err := store.SetEmailFromIdP(user.ID, "old@example.com"); err != nil {
		t.Fatal(err)
	}
	if status := request(username, "private\xe9", "private\xe9@example.com", []string{group}); status != http.StatusOK {
		t.Fatalf("invalid optional fields: status=%d", status)
	}
	verify("")
	stored, err = store.GetUserByUsername(username)
	if err != nil {
		t.Fatal(err)
	}
	if stored.DisplayName != name || stored.Email != "old@example.com" {
		t.Fatalf("invalid optional fields mutated persisted data: %+v", stored)
	}
	// Raw mode does not infer encodings or decode percent sequences.
	if status := request("literal%20user", "Literal", "", []string{}); status != http.StatusOK {
		t.Fatalf("literal percent username: status=%d", status)
	}
	if _, err := store.GetUserByUsername("literal%20user"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetUserByUsername("literal user"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("raw username was decoded: %v", err)
	}
}
