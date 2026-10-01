package api

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
)

func TestForwardAuthIntegration_DefaultViewerCannotCreateApps(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", strings.Repeat("a", 32))
	t.Setenv("SHINYHUB_FORWARD_AUTH_ENABLED", "true")
	t.Setenv("SHINYHUB_FORWARD_AUTH_SHARED_SECRET", strings.Repeat("p", 32))
	t.Setenv("SHINYHUB_FORWARD_AUTH_DEFAULT_ROLE", "")
	t.Setenv("SHINYHUB_FORWARD_AUTH_USER_HEADER", "X-Forwarded-User")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Storage.AppsDir = t.TempDir()
	cfg.Storage.AppDataDir = t.TempDir()
	store := newTestStore(t)
	srv := New(cfg, store, nil, nil)
	handler := auth.ForwardAuthMiddleware(store, auth.ForwardAuthConfig{
		Enabled: true, UserHeader: cfg.Auth.ForwardAuth.UserHeader,
		SharedSecret: cfg.Auth.ForwardAuth.SharedSecret, SecretHeader: cfg.Auth.ForwardAuth.SecretHeader,
		DefaultRole: cfg.Auth.ForwardAuth.DefaultRole,
	}, cfg.TrustedProxyNets)(srv.Router())

	request := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "http://example.com"+path, strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:12345"
		req.Header.Set(cfg.Auth.ForwardAuth.UserHeader, "new-viewer")
		req.Header.Set(cfg.Auth.ForwardAuth.SecretHeader, cfg.Auth.ForwardAuth.SharedSecret)
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}
	if response := request("GET", "/api/auth/me", ""); response.Code != http.StatusOK {
		t.Fatalf("sign in: status=%d body=%s", response.Code, response.Body.String())
	}
	user, err := store.GetUserByUsername("new-viewer")
	if err != nil || user.Role != "viewer" {
		t.Fatalf("new forward-auth account must be a viewer: user=%v err=%v", user, err)
	}
	if response := request("POST", "/api/apps", `{"slug":"forbidden-app","name":"Forbidden app"}`); response.Code != http.StatusForbidden {
		t.Fatalf("viewer app creation: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestForwardAuthIntegration_TrustedPeerAuthenticates(t *testing.T) {
	_, loopback, err := net.ParseCIDR("127.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Auth: config.AuthConfig{
			Secret: "test-secret-xxxxxxxxxxxxxxxxxxxxxxxx",
			ForwardAuth: config.ForwardAuthConfig{
				Enabled:     true,
				UserHeader:  "X-Forwarded-User",
				DefaultRole: "developer",
			},
		},
		Storage:          config.StorageConfig{AppsDir: t.TempDir(), AppDataDir: t.TempDir()},
		TrustedProxyNets: []*net.IPNet{loopback},
	}

	store := newTestStore(t) // defined in workers_test.go (package api)
	srv := New(cfg, store, nil, nil)

	// Mirror the main.go wiring: ForwardAuthMiddleware wraps the chi router at
	// the top-level mux so it covers all paths (/api and /app/*). The chi
	// router itself no longer carries forward-auth middleware.
	faCfg := auth.ForwardAuthConfig{
		Enabled:     true,
		UserHeader:  cfg.Auth.ForwardAuth.UserHeader,
		DefaultRole: cfg.Auth.ForwardAuth.DefaultRole,
	}
	handler := auth.ForwardAuthMiddleware(store, faCfg, cfg.TrustedProxyNets)(srv.Router())

	ts := httptest.NewServer(handler)
	defer ts.Close()

	// GET /api/auth/me requires authentication. With forward-auth enabled, a
	// trusted (loopback) peer, and the username header, the middleware
	// auto-provisions the user and BearerMiddleware passes through.
	req, _ := http.NewRequest("GET", ts.URL+"/api/auth/me", nil)
	req.Header.Set("X-Forwarded-User", "alice")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, b)
	}

	// The user should now exist in the store.
	if _, err := store.GetUserByUsername("alice"); err != nil {
		t.Fatalf("user not provisioned: %v", err)
	}
}
