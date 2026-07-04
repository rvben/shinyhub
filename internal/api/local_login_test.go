package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/dbtest"
)

// The login UI reads /api/auth/providers to decide what to show; it must report
// whether local (password) login is available so the form can be hidden when
// SSO-only.
func TestProviders_ReportsLocalLoginState(t *testing.T) {
	localOf := func(srv *api.Server) bool {
		req := httptest.NewRequest(http.MethodGet, "/api/auth/providers", nil)
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, req)
		var resp struct {
			Local bool `json:"local"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
			t.Fatalf("decode providers: %v", err)
		}
		return resp.Local
	}

	srv, _ := newTestServer(t) // default: local login enabled
	if !localOf(srv) {
		t.Error("providers.local should be true when local login is enabled (default)")
	}

	disabled := false
	cfg := &config.Config{
		Auth:    config.AuthConfig{Secret: "test-secret-000000000000000000000000", LocalLogin: &disabled},
		Storage: config.StorageConfig{AppsDir: t.TempDir(), AppDataDir: t.TempDir()},
	}
	srv2 := api.New(cfg, dbtest.New(t), nil, nil)
	if localOf(srv2) {
		t.Error("providers.local should be false when local login is disabled")
	}
}

// When local login is disabled the password endpoints must reject with 403,
// independent of any UI change - otherwise a client could bypass the IdP by
// POSTing credentials directly. This is the server-side half of the SSO-only
// switch.
func TestPasswordLogin_RejectedWhenLocalLoginDisabled(t *testing.T) {
	store := dbtest.New(t)
	disabled := false
	cfg := &config.Config{
		Auth:    config.AuthConfig{Secret: "test-secret-000000000000000000000000", LocalLogin: &disabled},
		Storage: config.StorageConfig{AppsDir: t.TempDir(), AppDataDir: t.TempDir()},
	}
	srv := api.New(cfg, store, nil, nil)

	for _, path := range []string{"/api/auth/login", "/api/auth/session"} {
		body := `{"username":"admin","password":"whatever"}`
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: expected 403 when local login disabled, got %d (%s)", path, rec.Code, rec.Body.String())
		}
	}
}

// The guard must NOT fire when local login is enabled (the default): a bad
// credential returns 401 (unauthorized), not 403 (disabled). This pins that the
// switch does not accidentally block the normal login path.
func TestPasswordLogin_NotBlockedWhenEnabled(t *testing.T) {
	srv, _ := newTestServer(t) // default cfg: LocalLogin nil => enabled
	for _, path := range []string{"/api/auth/login", "/api/auth/session"} {
		body := `{"username":"nobody","password":"wrong"}`
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, req)
		if rec.Code == http.StatusForbidden {
			t.Errorf("%s: local login is enabled; must not return 403", path)
		}
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: bad credentials should be 401, got %d", path, rec.Code)
		}
	}
}
