package api

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvben/shinyhub/internal/config"
)

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

	ts := httptest.NewServer(srv.Router())
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
