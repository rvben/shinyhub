package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGetProviders_NoneConfigured(t *testing.T) {
	srv, _ := newTestServer(t)
	req := httptest.NewRequest("GET", "/api/auth/providers", nil)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var resp struct {
		GitHub bool `json:"github"`
		Google bool `json:"google"`
		OIDC   struct {
			Enabled bool `json:"enabled"`
		} `json:"oidc"`
	}
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp.GitHub || resp.Google || resp.OIDC.Enabled {
		t.Errorf("expected all providers disabled in test server, got github=%v google=%v oidc=%v",
			resp.GitHub, resp.Google, resp.OIDC.Enabled)
	}
}

func TestOIDCLogin_NotConfigured(t *testing.T) {
	srv, _ := newTestServer(t)
	req := httptest.NewRequest("GET", "/api/auth/oidc/login", nil)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Errorf("expected 501 when OIDC not configured, got %d", rec.Code)
	}
}

func TestOIDCCallback_NotConfigured(t *testing.T) {
	srv, _ := newTestServer(t)
	req := httptest.NewRequest("GET", "/api/auth/oidc/callback?state=x&code=y", nil)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Errorf("expected 501 when OIDC not configured, got %d", rec.Code)
	}
}
