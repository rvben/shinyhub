package api_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
	"github.com/rvben/shinyhub/internal/oauth"
)

// newMockIdP starts an httptest server that speaks enough OpenID Connect for
// ShinyHub's real login flow: discovery, a JWKS endpoint, and a token endpoint
// that returns an RS256-signed ID token carrying idClaims (plus iss/aud/iat/exp).
// This lets the e2e test exercise the production authorization-code path -
// AuthURL -> Exchange -> VerifyIDToken - against a real (mock) provider.
func newMockIdP(t *testing.T, clientID string, idClaims map[string]any) *httptest.Server {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	const kid = "test-key-1"
	b64 := base64.RawURLEncoding.EncodeToString

	mux := http.NewServeMux()
	var srv *httptest.Server // set below; handlers read srv.URL at request time

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                srv.URL,
			"authorization_endpoint":                srv.URL + "/authorize",
			"token_endpoint":                        srv.URL + "/token",
			"jwks_uri":                              srv.URL + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		pub := key.Public().(*rsa.PublicKey)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]any{{
			"kty": "RSA", "alg": "RS256", "use": "sig", "kid": kid,
			"n": b64(pub.N.Bytes()),
			"e": b64(big.NewInt(int64(pub.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		claims := jwt.MapClaims{
			"iss": srv.URL,
			"aud": clientID,
			"iat": time.Now().Unix(),
			"exp": time.Now().Add(time.Hour).Unix(),
		}
		for k, v := range idClaims {
			claims[k] = v
		}
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
		tok.Header["kid"] = kid
		signed, err := tok.SignedString(key)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "mock-access-token",
			"token_type":   "Bearer",
			"id_token":     signed,
		})
	})

	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestOIDC_EndToEnd_LoginCallbackProvisionsAndReconciles drives the full native
// OIDC login as an operator would with no external auth proxy: it hits the real
// /api/auth/oidc/login and /api/auth/oidc/callback handlers against a mock IdP
// and asserts the outcomes a broker-replacement stands on:
//   - a session cookie is issued (authenticated session, no forward-auth),
//   - the user is JIT-provisioned,
//   - the platform role is reconciled from the IdP groups claim via
//     group_role_mappings (not the default role),
//   - the IdP display name and email are persisted.
func TestOIDC_EndToEnd_LoginCallbackProvisionsAndReconciles(t *testing.T) {
	const clientID = "shinyhub"

	// idClaims is mutated below (after the login redirect) to echo back the
	// nonce our own login handler generated, mirroring what a real IdP does:
	// it embeds whatever nonce it received on the authorization request into
	// the ID token it later returns from the token endpoint.
	idClaims := map[string]any{
		"sub":    "idp-subject-123",
		"email":  "alice@corp.example",
		"name":   "Alice Liddell",
		"groups": []string{"eng-admins"},
	}
	idp := newMockIdP(t, clientID, idClaims)

	store := dbtest.New(t)
	cfg := &config.Config{
		Auth: config.AuthConfig{
			Secret:            "test-secret-000000000000000000000000",
			OAuthDefaultRole:  "viewer",
			GroupRoleMappings: []config.GroupRoleMapping{{Group: "eng-admins", Role: "admin"}},
		},
		Storage: config.StorageConfig{AppsDir: t.TempDir(), AppDataDir: t.TempDir()},
	}
	srv := api.New(cfg, store, nil, nil)

	provider, err := oauth.NewOIDCProvider(
		context.Background(), idp.URL, clientID, "client-secret",
		"http://app.example.test/api/auth/oidc/callback", "Company SSO", "groups", "",
	)
	if err != nil {
		t.Fatalf("NewOIDCProvider: %v", err)
	}
	srv.SetOIDCProvider(provider)

	// 1. Login: expect a 302 to the IdP authorize endpoint with a state param
	//    and a state binding cookie.
	loginRec := httptest.NewRecorder()
	srv.Router().ServeHTTP(loginRec, httptest.NewRequest(http.MethodGet, "/api/auth/oidc/login", nil))
	if loginRec.Code != http.StatusFound {
		t.Fatalf("login: expected 302, got %d (%s)", loginRec.Code, loginRec.Body.String())
	}
	loc, err := loginRec.Result().Location()
	if err != nil {
		t.Fatalf("login redirect location: %v", err)
	}
	state := loc.Query().Get("state")
	if state == "" {
		t.Fatalf("login redirect %q carries no state", loc.String())
	}
	nonce := loc.Query().Get("nonce")
	if nonce == "" {
		t.Fatalf("login redirect %q carries no nonce", loc.String())
	}
	// The mock IdP has no real authorize endpoint to observe the nonce we just
	// sent, so echo it into the claims it will sign - exactly what a real IdP
	// does with the nonce it received on the authorization request.
	idClaims["nonce"] = nonce
	var stateCookie *http.Cookie
	for _, c := range loginRec.Result().Cookies() {
		if c.Value != "" {
			stateCookie = c
		}
	}
	if stateCookie == nil {
		t.Fatal("login set no state binding cookie")
	}

	// 2. Callback: the IdP redirected the browser back with our state and a code.
	cbReq := httptest.NewRequest(http.MethodGet, "/api/auth/oidc/callback?state="+url.QueryEscape(state)+"&code=mock-auth-code", nil)
	cbReq.AddCookie(stateCookie)
	cbRec := httptest.NewRecorder()
	srv.Router().ServeHTTP(cbRec, cbReq)

	if cbRec.Code != http.StatusFound {
		t.Fatalf("callback: expected 302, got %d (%s)", cbRec.Code, cbRec.Body.String())
	}
	if cbLoc, _ := cbRec.Result().Location(); cbLoc == nil || cbLoc.Path != "/" {
		t.Fatalf("callback should redirect to /, got %v", cbLoc)
	}

	// Session cookie issued => authenticated session established with no proxy.
	var session *http.Cookie
	for _, c := range cbRec.Result().Cookies() {
		if c.Name == auth.SessionCookieName && c.Value != "" {
			session = c
		}
	}
	if session == nil {
		t.Fatal("callback issued no session cookie")
	}
	if !session.HttpOnly {
		t.Error("session cookie must be HttpOnly")
	}

	// 3. The user was JIT-provisioned from the email local-part.
	user, err := store.GetUserByUsername("alice")
	if err != nil {
		t.Fatalf("expected JIT-provisioned user 'alice': %v", err)
	}
	// 4. Role reconciled from the groups claim, NOT the default 'viewer'.
	if user.Role != "admin" {
		t.Errorf("role = %q, want admin (reconciled from group eng-admins via group_role_mappings)", user.Role)
	}
	// 5. IdP display name and email persisted.
	if user.DisplayName != "Alice Liddell" {
		t.Errorf("display name = %q, want %q", user.DisplayName, "Alice Liddell")
	}
	if user.Email != "alice@corp.example" {
		t.Errorf("email = %q, want %q", user.Email, "alice@corp.example")
	}
}

// TestOIDC_EndToEnd_AbsentGroupsClaimDoesNotDemote proves that an IdP response
// without a groups claim leaves an existing user's role intact (an absent claim
// must not be read as "no groups" and demote a promoted user). This is the
// safety property the callback's GroupsClaimPresent guard provides.
func TestOIDC_EndToEnd_AbsentGroupsClaimDoesNotDemote(t *testing.T) {
	const clientID = "shinyhub"
	// No "groups" key at all in the ID token.
	idClaims := map[string]any{
		"sub":   "idp-subject-777",
		"email": "bob@corp.example",
		"name":  "Bob Builder",
	}
	idp := newMockIdP(t, clientID, idClaims)

	store := dbtest.New(t)
	cfg := &config.Config{
		Auth: config.AuthConfig{
			Secret:            "test-secret-000000000000000000000000",
			OAuthDefaultRole:  "viewer",
			GroupRoleMappings: []config.GroupRoleMapping{{Group: "eng-admins", Role: "admin"}},
		},
		Storage: config.StorageConfig{AppsDir: t.TempDir(), AppDataDir: t.TempDir()},
	}
	srv := api.New(cfg, store, nil, nil)
	provider, err := oauth.NewOIDCProvider(
		context.Background(), idp.URL, clientID, "client-secret",
		"http://app.example.test/api/auth/oidc/callback", "Company SSO", "groups", "",
	)
	if err != nil {
		t.Fatalf("NewOIDCProvider: %v", err)
	}
	srv.SetOIDCProvider(provider)

	// Pre-provision bob as an operator (as if promoted earlier).
	if err := store.CreateUser(db.CreateUserParams{Username: "bob", PasswordHash: "", Role: "operator"}); err != nil {
		t.Fatalf("seed bob: %v", err)
	}

	loginRec := httptest.NewRecorder()
	srv.Router().ServeHTTP(loginRec, httptest.NewRequest(http.MethodGet, "/api/auth/oidc/login", nil))
	loc, _ := loginRec.Result().Location()
	state := loc.Query().Get("state")
	// Echo the nonce back into the claims the mock IdP will sign, as a real
	// IdP would (see the sibling provisioning test for the full rationale).
	idClaims["nonce"] = loc.Query().Get("nonce")
	var stateCookie *http.Cookie
	for _, c := range loginRec.Result().Cookies() {
		if c.Value != "" {
			stateCookie = c
		}
	}

	cbReq := httptest.NewRequest(http.MethodGet, "/api/auth/oidc/callback?state="+url.QueryEscape(state)+"&code=x", nil)
	cbReq.AddCookie(stateCookie)
	cbRec := httptest.NewRecorder()
	srv.Router().ServeHTTP(cbRec, cbReq)
	if cbRec.Code != http.StatusFound {
		t.Fatalf("callback: expected 302, got %d (%s)", cbRec.Code, cbRec.Body.String())
	}

	user, err := store.GetUserByUsername("bob")
	if err != nil {
		t.Fatalf("get bob: %v", err)
	}
	if user.Role != "operator" {
		t.Errorf("role = %q, want operator preserved (absent groups claim must not demote)", user.Role)
	}
}

// TestOIDC_EndToEnd_MalformedGroupsClaimRefusedWhenRequireValid drives the real
// callback route (not a unit test of decodeGroupsClaim in isolation) against an
// IdP that sends a "groups" claim of the wrong JSON type (a number, not an
// array/string). With oauth.oidc.require_valid_groups=true this must fail
// closed: 502, and - critically - no session cookie, since a claim we can't
// parse must never be silently treated as "no groups" and let the login
// through with a default role. This exercises the refusal in
// handleOIDCCallback (oidc_handler.go's GroupsClaimMalformed + RequireValidGroups
// gate), which previously had no test coverage at all.
func TestOIDC_EndToEnd_MalformedGroupsClaimRefusedWhenRequireValid(t *testing.T) {
	const clientID = "shinyhub"
	idClaims := map[string]any{
		"sub":    "idp-subject-42",
		"email":  "carol@corp.example",
		"name":   "Carol Danvers",
		"groups": 42, // malformed: neither a JSON array of strings nor a single string
	}
	idp := newMockIdP(t, clientID, idClaims)

	store := dbtest.New(t)
	cfg := &config.Config{
		Auth: config.AuthConfig{
			Secret:           "test-secret-000000000000000000000000",
			OAuthDefaultRole: "viewer",
		},
		OAuth: config.OAuthConfig{
			OIDC: config.OIDCConfig{RequireValidGroups: true},
		},
		Storage: config.StorageConfig{AppsDir: t.TempDir(), AppDataDir: t.TempDir()},
	}
	srv := api.New(cfg, store, nil, nil)
	provider, err := oauth.NewOIDCProvider(
		context.Background(), idp.URL, clientID, "client-secret",
		"http://app.example.test/api/auth/oidc/callback", "Company SSO", "groups", "",
	)
	if err != nil {
		t.Fatalf("NewOIDCProvider: %v", err)
	}
	srv.SetOIDCProvider(provider)

	loginRec := httptest.NewRecorder()
	srv.Router().ServeHTTP(loginRec, httptest.NewRequest(http.MethodGet, "/api/auth/oidc/login", nil))
	loc, err := loginRec.Result().Location()
	if err != nil {
		t.Fatalf("login redirect location: %v", err)
	}
	state := loc.Query().Get("state")
	idClaims["nonce"] = loc.Query().Get("nonce")
	var stateCookie *http.Cookie
	for _, c := range loginRec.Result().Cookies() {
		if c.Value != "" {
			stateCookie = c
		}
	}
	if stateCookie == nil {
		t.Fatal("login set no state binding cookie")
	}

	cbReq := httptest.NewRequest(http.MethodGet, "/api/auth/oidc/callback?state="+url.QueryEscape(state)+"&code=mock-auth-code", nil)
	cbReq.AddCookie(stateCookie)
	cbRec := httptest.NewRecorder()
	srv.Router().ServeHTTP(cbRec, cbReq)

	if cbRec.Code != http.StatusBadGateway {
		t.Fatalf("callback: expected 502 for malformed groups claim with require_valid_groups=true, got %d (%s)",
			cbRec.Code, cbRec.Body.String())
	}
	for _, c := range cbRec.Result().Cookies() {
		if c.Name == auth.SessionCookieName && c.Value != "" {
			t.Errorf("callback set a session cookie despite refusing the malformed groups claim")
		}
	}
	if _, err := store.GetUserByUsername("carol"); err == nil {
		t.Errorf("user should not be provisioned when the malformed groups claim is refused")
	}
}
