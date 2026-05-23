package api

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/metrics"
)

// scrape returns the Prometheus exposition text for a registry.
func scrape(t *testing.T, reg *metrics.Registry) string {
	t.Helper()
	rec := httptest.NewRecorder()
	reg.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if rec.Code != 200 {
		t.Fatalf("metrics scrape returned %d", rec.Code)
	}
	return rec.Body.String()
}

// TestRouter_RecordsAPIMetricsWhenEnabled proves that once a metrics registry is
// wired in, requests through the API router are recorded against their route
// pattern. The public /api/auth/providers endpoint needs no auth, so this
// exercises the middleware end to end through the real router.
func TestRouter_RecordsAPIMetricsWhenEnabled(t *testing.T) {
	srv := New(&config.Config{Auth: config.AuthConfig{Secret: "test-secret"}}, nil, nil, nil)
	reg := metrics.New("test")
	srv.SetMetrics(reg)

	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, httptest.NewRequest("GET", "/api/auth/providers", nil))
	if rec.Code != 200 {
		t.Fatalf("GET /api/auth/providers returned %d: %s", rec.Code, rec.Body.String())
	}

	body := scrape(t, reg)
	if !strings.Contains(body, `shinyhub_http_requests_total{`) {
		t.Fatalf("scrape missing request counter:\n%s", body)
	}
	if !strings.Contains(body, `route="/api/auth/providers"`) {
		t.Fatalf("request was not recorded against its route pattern:\n%s", body)
	}
}

// TestRouter_NoMetricsWhenDisabled proves the router functions normally when no
// registry is wired in (the default), so metrics stay strictly opt-in.
func TestRouter_NoMetricsWhenDisabled(t *testing.T) {
	srv := New(&config.Config{Auth: config.AuthConfig{Secret: "test-secret"}}, nil, nil, nil)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, httptest.NewRequest("GET", "/api/auth/providers", nil))
	if rec.Code != 200 {
		t.Fatalf("GET /api/auth/providers returned %d with metrics disabled: %s", rec.Code, rec.Body.String())
	}
}
