package api_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/dbtest"
	"github.com/rvben/shinyhub/internal/oauth"
)

// dexImage pins the real IdP used by TestOIDC_Dex_EndToEnd_RealProviderDrivesLoginAndGroupReconciliation
// to a specific release tag (not "latest") so the test's behavior does not
// shift under an unpinned image.
const dexImage = "ghcr.io/dexidp/dex:v2.45.1"

// dexInstance is a running Dex (github.com/dexidp/dex) container: a real,
// standards-compliant OpenID Connect provider. It exposes a static test client
// and Dex's built-in "mockCallback" connector, which returns a fixed identity
// (sub, email "kilgore@kilgore.trout", name "Kilgore Trout", groups
// ["authors"]) with NO login form or user interaction - the authorization
// request redirects straight back to the redirect_uri with a code, which is
// exactly what lets this test drive the real authorization-code flow
// headlessly.
type dexInstance struct {
	IssuerURL    string
	ClientID     string
	ClientSecret string
	RedirectURI  string
}

// startDex launches a real Dex OIDC provider in Docker, configured with a
// static client for ShinyHub and the mock connector, and waits for its
// discovery document to be servable. It skips (never fails) the test when
// Docker is unavailable, the image cannot be pulled, or Dex does not become
// ready in time - all environmental preconditions, not behavior the code
// under test is responsible for. Mirrors the skip-on-environment pattern used
// by internal/process/docker_test.go's dockerRuntimeWithImage.
func startDex(t *testing.T) dexInstance {
	t.Helper()

	dockerPath, err := exec.LookPath("docker")
	if err != nil {
		t.Skip("docker not found in PATH; skipping real-Dex OIDC integration test")
	}
	{
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if out, err := exec.CommandContext(ctx, dockerPath, "info").CombinedOutput(); err != nil {
			t.Skipf("docker daemon unreachable; skipping real-Dex OIDC integration test: %v\n%s", err, out)
		}
	}

	port, err := freeTCPPort()
	if err != nil {
		t.Skipf("could not reserve a local port for Dex: %v", err)
	}
	issuer := fmt.Sprintf("http://127.0.0.1:%d", port)

	const clientID = "shinyhub-test"
	const clientSecret = "shinyhub-test-secret" //nolint:gosec // test-only static secret for a throwaway local Dex instance
	const redirectURI = "http://shinyhub.test/api/auth/oidc/callback"

	configYAML := fmt.Sprintf(`issuer: %s
storage:
  type: memory
web:
  http: 0.0.0.0:5556
oauth2:
  skipApprovalScreen: true
staticClients:
- id: %s
  secret: %s
  redirectURIs:
  - %q
  name: 'ShinyHub Test'
connectors:
- type: mockCallback
  id: mock
  name: Mock
enablePasswordDB: false
`, issuer, clientID, clientSecret, redirectURI)

	configPath := filepath.Join(t.TempDir(), "dex-config.yaml")
	if err := os.WriteFile(configPath, []byte(configYAML), 0o644); err != nil {
		t.Fatalf("write dex config: %v", err)
	}

	containerName := "shinyhub-oidc-dex-it-" + randHex(t, 6)
	runArgs := []string{
		"run", "-d", "--rm",
		"--name", containerName,
		"-p", fmt.Sprintf("127.0.0.1:%d:5556", port),
		"-v", configPath + ":/etc/dex/config.docker.yaml:ro",
		dexImage,
	}
	{
		// Bounded generously: this also covers a cold image pull on first run.
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		if out, err := exec.CommandContext(ctx, dockerPath, runArgs...).CombinedOutput(); err != nil {
			t.Skipf("could not start Dex (%s); skipping real-Dex OIDC integration test: %v\n%s", dexImage, err, out)
		}
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = exec.CommandContext(ctx, dockerPath, "rm", "-f", containerName).Run()
	})

	waitForDexReady(t, issuer)

	return dexInstance{
		IssuerURL:    issuer,
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURI:  redirectURI,
	}
}

// waitForDexReady polls Dex's real discovery endpoint until it serves a valid
// document or the timeout elapses, in which case the test is skipped (a slow
// or failed container start is environmental, not a defect in ShinyHub).
func waitForDexReady(t *testing.T, issuer string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest(http.MethodGet, issuer+"/.well-known/openid-configuration", nil)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				var doc map[string]any
				if json.NewDecoder(resp.Body).Decode(&doc) == nil {
					if iss, _ := doc["issuer"].(string); iss == issuer {
						return
					}
				}
			}
		} else {
			lastErr = err
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Skipf("Dex did not become ready at %s within timeout; skipping real-Dex OIDC integration test: %v", issuer, lastErr)
}

func freeTCPPort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

func randHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return hex.EncodeToString(b)
}

// TestOIDC_Dex_EndToEnd_RealProviderDrivesLoginAndGroupReconciliation drives
// ShinyHub's production OIDC handlers - the same handleOIDCLogin and
// handleOIDCCallback exercised in oidc_e2e_test.go - against a real,
// standards-compliant OpenID Connect provider (Dex) instead of the hand-rolled
// mock IdP. Unlike the mock, this proves oauth.NewOIDCProvider's real
// discovery (GET {issuer}/.well-known/openid-configuration), a real JWKS fetch
// and RS256 signature check, and a real IdP's authorization-code redirect
// chain (/auth -> /auth/mock -> /callback -> our redirect_uri) - all driven
// headlessly via Dex's built-in mockCallback connector, which returns a fixed
// identity with no login form.
//
// Only the two hops between the browser and ShinyHub (GET .../oidc/login and
// GET .../oidc/callback) are driven in-process against srv.Router(), exactly
// as the mock-IdP e2e tests do; every hop between ShinyHub and the IdP (the
// AuthURL redirect, the code exchange, the ID token fetch and verification)
// talks to the real Dex container over the network.
func TestOIDC_Dex_EndToEnd_RealProviderDrivesLoginAndGroupReconciliation(t *testing.T) {
	dex := startDex(t)

	store := dbtest.New(t)
	cfg := &config.Config{
		Auth: config.AuthConfig{
			Secret:            "test-secret-000000000000000000000000",
			OAuthDefaultRole:  "viewer",
			GroupRoleMappings: []config.GroupRoleMapping{{Group: "authors", Role: "admin"}},
		},
		Storage: config.StorageConfig{AppsDir: t.TempDir(), AppDataDir: t.TempDir()},
	}
	srv := api.New(cfg, store, nil, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	provider, err := oauth.NewOIDCProvider(
		ctx, dex.IssuerURL, dex.ClientID, dex.ClientSecret, dex.RedirectURI,
		"Dex SSO", "groups", "groups",
	)
	if err != nil {
		t.Fatalf("NewOIDCProvider real discovery against Dex at %s: %v", dex.IssuerURL, err)
	}
	srv.SetOIDCProvider(provider)

	// 1. Login: ShinyHub's own handler issues the 302 to the real Dex
	//    authorization endpoint, with a state param and a state binding cookie.
	loginRec := httptest.NewRecorder()
	srv.Router().ServeHTTP(loginRec, httptest.NewRequest(http.MethodGet, "/api/auth/oidc/login", nil))
	if loginRec.Code != http.StatusFound {
		t.Fatalf("login: expected 302, got %d (%s)", loginRec.Code, loginRec.Body.String())
	}
	authorizeURL, err := loginRec.Result().Location()
	if err != nil {
		t.Fatalf("login redirect location: %v", err)
	}
	ourState := authorizeURL.Query().Get("state")
	if ourState == "" {
		t.Fatalf("login redirect %q carries no state", authorizeURL.String())
	}
	var stateCookie *http.Cookie
	for _, c := range loginRec.Result().Cookies() {
		if c.Value != "" {
			stateCookie = c
		}
	}
	if stateCookie == nil {
		t.Fatal("login set no state binding cookie")
	}

	// 2. Drive the real authorization-code flow against Dex: follow every
	//    redirect Dex issues (through its mockCallback connector, which needs
	//    no form) until it tries to redirect back to our (non-listening)
	//    redirect_uri host, at which point we stop and read the code+state off
	//    the Location header ourselves - exactly what a browser would hand back
	//    to ShinyHub's callback endpoint.
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar: %v", err)
	}
	dexHost := mustHost(t, dex.IssuerURL)
	dexClient := &http.Client{
		Jar:     jar,
		Timeout: 10 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL.Host != dexHost {
				return http.ErrUseLastResponse
			}
			if len(via) > 10 {
				return fmt.Errorf("too many redirects following Dex's auth flow")
			}
			return nil
		},
	}
	resp, err := dexClient.Get(authorizeURL.String())
	if err != nil {
		t.Fatalf("drive authorization request against real Dex: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound && resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected Dex's final redirect back to our callback, got %d from %s", resp.StatusCode, resp.Request.URL)
	}
	callbackURL, err := resp.Location()
	if err != nil {
		t.Fatalf("Dex's final redirect carries no Location: %v", err)
	}
	if callbackURL.Host != "shinyhub.test" || callbackURL.Path != "/api/auth/oidc/callback" {
		t.Fatalf("Dex redirected somewhere unexpected: %s (expected our configured redirect_uri)", callbackURL)
	}
	code := callbackURL.Query().Get("code")
	if code == "" {
		t.Fatalf("Dex's redirect %q carries no authorization code", callbackURL)
	}
	echoedState := callbackURL.Query().Get("state")
	if echoedState != ourState {
		t.Fatalf("Dex echoed state %q, want our original state %q", echoedState, ourState)
	}

	// 3. Callback: the browser hands ShinyHub the real code+state it got from
	//    Dex. handleOIDCCallback exchanges the code with Dex's real token
	//    endpoint and verifies the ID token against Dex's real JWKS.
	cbReq := httptest.NewRequest(http.MethodGet,
		"/api/auth/oidc/callback?state="+url.QueryEscape(echoedState)+"&code="+url.QueryEscape(code), nil)
	cbReq.AddCookie(stateCookie)
	cbRec := httptest.NewRecorder()
	srv.Router().ServeHTTP(cbRec, cbReq)

	if cbRec.Code != http.StatusFound {
		t.Fatalf("callback: expected 302, got %d (%s)", cbRec.Code, cbRec.Body.String())
	}
	if cbLoc, _ := cbRec.Result().Location(); cbLoc == nil || cbLoc.Path != "/" {
		t.Fatalf("callback should redirect to /, got %v", cbLoc)
	}

	// Session cookie issued => the real Dex-signed ID token verified and a
	// ShinyHub session was established.
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

	// 4. The user was JIT-provisioned from the email local-part of Dex's fixed
	//    mock identity ("kilgore@kilgore.trout" -> "kilgore").
	user, err := store.GetUserByUsername("kilgore")
	if err != nil {
		t.Fatalf("expected JIT-provisioned user 'kilgore': %v", err)
	}
	// 5. Role reconciled from Dex's real "groups" claim (["authors"]), NOT the
	//    default 'viewer'.
	if user.Role != "admin" {
		t.Errorf("role = %q, want admin (reconciled from Dex's real groups claim via group_role_mappings)", user.Role)
	}
	// 6. Dex's real name/email claims were persisted.
	if user.DisplayName != "Kilgore Trout" {
		t.Errorf("display name = %q, want %q", user.DisplayName, "Kilgore Trout")
	}
	if user.Email != "kilgore@kilgore.trout" {
		t.Errorf("email = %q, want %q", user.Email, "kilgore@kilgore.trout")
	}
}

func mustHost(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse %q: %v", rawURL, err)
	}
	return u.Host
}
