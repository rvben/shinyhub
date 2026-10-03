package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
	"github.com/rvben/shinyhub/internal/oauth"
)

// newFakeGitHub starts a fake GitHub token+API server and returns a GitHub
// provider pointed at it via the test-only SetTestEndpoints seam, so the real
// Exchange/FetchUser code paths run against a controlled backend instead of
// github.com. A nil tokenHandler serves a fixed successful token response.
func newFakeGitHub(t *testing.T, tokenHandler http.HandlerFunc, userBody, emailsBody string) *oauth.GitHub {
	t.Helper()
	if tokenHandler == nil {
		tokenHandler = func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"access_token":"gh-mock-token","token_type":"Bearer"}`)
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/login/oauth/access_token", tokenHandler)
	mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, userBody)
	})
	mux.HandleFunc("/user/emails", func(w http.ResponseWriter, r *http.Request) {
		if emailsBody == "" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, emailsBody)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	gh := oauth.NewGitHub("gh-client-id", "gh-client-secret", "http://app.example.test/api/auth/github/callback")
	gh.SetTestEndpoints(srv.URL+"/login/oauth/access_token", srv.URL)
	return gh
}

// newFakeGoogle starts a fake Google token+userinfo server and returns a
// Google provider pointed at it via the test-only SetTestEndpoints seam. A nil
// tokenHandler serves a fixed successful token response.
func newFakeGoogle(t *testing.T, tokenHandler http.HandlerFunc, userinfoBody string) *oauth.Google {
	t.Helper()
	if tokenHandler == nil {
		tokenHandler = func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"access_token":"g-mock-token","token_type":"Bearer"}`)
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/token", tokenHandler)
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, userinfoBody)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	g := oauth.NewGoogle("g-client-id", "g-client-secret", "http://app.example.test/api/auth/google/callback")
	g.SetTestEndpoints(srv.URL+"/token", srv.URL+"/userinfo")
	return g
}

// e2eTestServer builds a minimal api.Server for oauth e2e tests: an auth
// secret and isolated storage dirs, nothing else configured.
func e2eTestServer(t *testing.T) (*api.Server, *db.Store) {
	t.Helper()
	store := dbtest.New(t)
	cfg := &config.Config{
		Auth:    config.AuthConfig{Secret: "test-secret-000000000000000000000000", OAuthDefaultRole: "viewer"},
		Storage: config.StorageConfig{AppsDir: t.TempDir(), AppDataDir: t.TempDir()},
	}
	return api.New(cfg, store, nil, nil), store
}

// driveCallback seeds a server-side oauth state bound to provider, attaches
// the matching state cookie, and drives the real callback route through the
// router - the production code path (state verification, token exchange,
// user fetch, provisioning, session issuance), not a hand-rolled
// re-implementation.
func driveCallback(t *testing.T, srv *api.Server, store *db.Store, path, state, provider string) *httptest.ResponseRecorder {
	t.Helper()
	if err := store.CreateOAuthState(state, provider); err != nil {
		t.Fatalf("seed oauth state: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, path+"?state="+state+"&code=mock-code", nil)
	req.AddCookie(&http.Cookie{Name: auth.OAuthStateCookieName, Value: state})
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	return rec
}

func sessionCookie(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.SessionCookieName && c.Value != "" {
			return c
		}
	}
	return nil
}

// TestGitHubCallback_EndToEnd_ProvisionsUserAndSession drives the real
// handleGitHubCallback (not store.CreateUser called directly, as the older
// vacuous coverage did) against a fake GitHub token+API server: it must
// exchange the code, fetch the user, JIT-provision an account, and issue a
// session cookie.
func TestGitHubCallback_EndToEnd_ProvisionsUserAndSession(t *testing.T) {
	gh := newFakeGitHub(t, nil,
		`{"id":501,"login":"octocat","name":"Octo Cat","email":"octocat@corp.example"}`, "")

	srv, store := e2eTestServer(t)
	srv.SetGitHubProvider(gh)

	rec := driveCallback(t, srv, store, "/api/auth/github/callback", "gh-state-happy", "github")
	if rec.Code != http.StatusFound {
		t.Fatalf("callback: expected 302, got %d (%s)", rec.Code, rec.Body.String())
	}
	if sessionCookie(rec) == nil {
		t.Fatal("callback issued no session cookie")
	}

	user, err := store.GetUserByUsername("octocat")
	if err != nil {
		t.Fatalf("expected JIT-provisioned user 'octocat': %v", err)
	}
	if user.Email != "octocat@corp.example" {
		t.Errorf("email = %q, want %q", user.Email, "octocat@corp.example")
	}
}

// TestGitHubCallback_EndToEnd_TokenExchangeFault proves a broken token
// endpoint (500 with a non-JSON body, as a misbehaving or compromised
// endpoint might return) maps to a generic 502 through the real handler, with
// no internal detail leaked to the client and no session/user created.
func TestGitHubCallback_EndToEnd_TokenExchangeFault(t *testing.T) {
	gh := newFakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, "<html>internal proxy error, upstream unreachable</html>")
	}, `{"id":501,"login":"octocat","name":"Octo Cat","email":"octocat@corp.example"}`, "")

	srv, store := e2eTestServer(t)
	srv.SetGitHubProvider(gh)

	rec := driveCallback(t, srv, store, "/api/auth/github/callback", "gh-state-fault", "github")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 on token-exchange fault, got %d (%s)", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if strings.Contains(strings.ToLower(body["error"]), "internal proxy error") || strings.Contains(body["error"], "<html>") {
		t.Errorf("error response leaked upstream detail: %q", body["error"])
	}
	if sessionCookie(rec) != nil {
		t.Error("session cookie set despite token-exchange fault")
	}
	if _, err := store.GetUserByUsername("octocat"); err == nil {
		t.Error("user should not be provisioned when token exchange fails")
	}
}

// TestGoogleCallback_EndToEnd_ProvisionsUserAndSession is the Google
// equivalent of TestGitHubCallback_EndToEnd_ProvisionsUserAndSession: drives
// the real handleGoogleCallback against a fake Google token+userinfo server.
func TestGoogleCallback_EndToEnd_ProvisionsUserAndSession(t *testing.T) {
	g := newFakeGoogle(t, nil, `{"id":"9001","email":"dana@corp.example","name":"Dana Scully"}`)

	srv, store := e2eTestServer(t)
	srv.SetGoogleProvider(g)

	rec := driveCallback(t, srv, store, "/api/auth/google/callback", "g-state-happy", "google")
	if rec.Code != http.StatusFound {
		t.Fatalf("callback: expected 302, got %d (%s)", rec.Code, rec.Body.String())
	}
	if sessionCookie(rec) == nil {
		t.Fatal("callback issued no session cookie")
	}

	user, err := store.GetUserByUsername("dana")
	if err != nil {
		t.Fatalf("expected JIT-provisioned user 'dana': %v", err)
	}
	if user.Email != "dana@corp.example" {
		t.Errorf("email = %q, want %q", user.Email, "dana@corp.example")
	}
}

// TestGoogleCallback_EndToEnd_TokenExchangeFault_InvalidGrant proves a
// standards-shaped OAuth2 error response (400 invalid_grant, as a real IdP
// returns for a reused/expired code) maps to a generic 502 through the real
// handler, without leaking the provider's error code/description to the
// client and without creating a session or user.
func TestGoogleCallback_EndToEnd_TokenExchangeFault_InvalidGrant(t *testing.T) {
	g := newFakeGoogle(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"invalid_grant","error_description":"Bad Request"}`)
	}, `{"id":"9001","email":"dana@corp.example","name":"Dana Scully"}`)

	srv, store := e2eTestServer(t)
	srv.SetGoogleProvider(g)

	rec := driveCallback(t, srv, store, "/api/auth/google/callback", "g-state-fault", "google")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 on invalid_grant, got %d (%s)", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if strings.Contains(body["error"], "invalid_grant") {
		t.Errorf("error response leaked provider error code: %q", body["error"])
	}
	if sessionCookie(rec) != nil {
		t.Error("session cookie set despite invalid_grant")
	}
	if _, err := store.GetUserByUsername("dana"); err == nil {
		t.Error("user should not be provisioned when token exchange fails")
	}
}

// TestOAuthState_RejectedAcrossProviders is the regression test for the OAuth
// state binding fix: a state nonce minted for one provider's login flow must
// not be accepted by a different provider's callback. Before the fix,
// ConsumeOAuthState matched on the state string alone, so a state minted for
// GitHub was consumed by the Google callback and only then failed the token
// exchange - after a real request had already reached Google's token
// endpoint. The correct behavior is the same invalid-state response the
// callback already returns for a missing or expired state, returned before
// any token exchange is attempted, and the original state must remain usable
// afterward by the provider it was actually minted for.
func TestOAuthState_RejectedAcrossProviders(t *testing.T) {
	var googleTokenHits atomic.Int32
	gh := newFakeGitHub(t, nil,
		`{"id":801,"login":"octocat","name":"Octo Cat","email":"octocat@corp.example"}`, "")
	g := newFakeGoogle(t, func(w http.ResponseWriter, r *http.Request) {
		googleTokenHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"g-mock-token","token_type":"Bearer"}`)
	}, `{"id":"9201","email":"dana2@corp.example","name":"Dana Scully"}`)

	srv, store := e2eTestServer(t)
	srv.SetGitHubProvider(gh)
	srv.SetGoogleProvider(g)

	const state = "cross-provider-state"
	if err := store.CreateOAuthState(state, "github"); err != nil {
		t.Fatalf("seed oauth state: %v", err)
	}

	// Present the github-minted state to the google callback.
	req := httptest.NewRequest(http.MethodGet, "/api/auth/google/callback?state="+state+"&code=mock-code", nil)
	req.AddCookie(&http.Cookie{Name: auth.OAuthStateCookieName, Value: state})
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("cross-provider state on google callback: want 400, got %d (%s)", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if !strings.Contains(body["error"], "invalid or expired state") {
		t.Errorf("cross-provider state on google callback: want invalid-state error, got %q", body["error"])
	}
	if sessionCookie(rec) != nil {
		t.Error("session cookie set despite cross-provider state")
	}
	if n := googleTokenHits.Load(); n != 0 {
		t.Errorf("google token endpoint hit %d times; a cross-provider state must be rejected before any token exchange", n)
	}
	if _, err := store.GetUserByUsername("dana2"); err == nil {
		t.Error("user should not be provisioned from a cross-provider state")
	}

	// The state must still be usable by the provider it was actually minted for.
	req2 := httptest.NewRequest(http.MethodGet, "/api/auth/github/callback?state="+state+"&code=mock-code", nil)
	req2.AddCookie(&http.Cookie{Name: auth.OAuthStateCookieName, Value: state})
	rec2 := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusFound {
		t.Fatalf("github callback with its own state: want 302, got %d (%s)", rec2.Code, rec2.Body.String())
	}
	if sessionCookie(rec2) == nil {
		t.Fatal("github callback with its own state issued no session cookie")
	}
}

// TestOAuthState_OIDCStateRejectedByGitHubCallback extends the cross-provider
// binding proof to the third provider path (OIDC), without needing a full
// mock identity provider: rejection happens in ConsumeOAuthState, before the
// callback touches the configured provider at all, so seeding a state bound
// to "oidc" and presenting it to the GitHub callback exercises the same
// shared binding check that guards GitHub, Google, and OIDC alike.
func TestOAuthState_OIDCStateRejectedByGitHubCallback(t *testing.T) {
	gh := newFakeGitHub(t, nil,
		`{"id":803,"login":"octocat3","name":"Octo Cat Three","email":"octocat3@corp.example"}`, "")
	srv, store := e2eTestServer(t)
	srv.SetGitHubProvider(gh)

	const state = "oidc-minted-state"
	if err := store.CreateOAuthState(state, "oidc"); err != nil {
		t.Fatalf("seed oauth state: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/auth/github/callback?state="+state+"&code=mock-code", nil)
	req.AddCookie(&http.Cookie{Name: auth.OAuthStateCookieName, Value: state})
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("oidc-minted state on github callback: want 400, got %d (%s)", rec.Code, rec.Body.String())
	}
	if sessionCookie(rec) != nil {
		t.Error("session cookie set despite an oidc-minted state presented to github")
	}
	if _, err := store.GetUserByUsername("octocat3"); err == nil {
		t.Error("user should not be provisioned from a cross-provider state")
	}
}
