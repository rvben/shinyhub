package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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

// driveCallback seeds a server-side oauth state, attaches the matching state
// cookie, and drives the real callback route through the router - the
// production code path (state verification, token exchange, user fetch,
// provisioning, session issuance), not a hand-rolled re-implementation.
func driveCallback(t *testing.T, srv *api.Server, store *db.Store, path, state string) *httptest.ResponseRecorder {
	t.Helper()
	if err := store.CreateOAuthState(state); err != nil {
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

	rec := driveCallback(t, srv, store, "/api/auth/github/callback", "gh-state-happy")
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

	rec := driveCallback(t, srv, store, "/api/auth/github/callback", "gh-state-fault")
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

	rec := driveCallback(t, srv, store, "/api/auth/google/callback", "g-state-happy")
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

	rec := driveCallback(t, srv, store, "/api/auth/google/callback", "g-state-fault")
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
