package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/metrics"
)

// silentRecover is a minimal stand-in for chi's middleware.Recoverer: it
// converts a downstream panic into a 500 without dumping a stack trace, so a
// panic-recovery test stays focused on the observation boundary (recoverer
// below, observation above) without polluting test output.
func silentRecover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rvr := recover(); rvr != nil {
				w.WriteHeader(http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

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

// TestObserve_RecordsAPIMetricsWhenEnabled proves that once a metrics registry
// is wired in, requests through the observed API handler are recorded against
// their route pattern. The public /api/auth/providers endpoint needs no auth,
// so this exercises the middleware end to end through the real router.
func TestObserve_RecordsAPIMetricsWhenEnabled(t *testing.T) {
	srv := New(&config.Config{Auth: config.AuthConfig{Secret: "test-secret"}}, nil, nil, nil)
	reg := metrics.New("test")
	srv.SetMetrics(reg)

	rec := httptest.NewRecorder()
	srv.Observe(srv.Router()).ServeHTTP(rec, httptest.NewRequest("GET", "/api/auth/providers", nil))
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

// TestObserve_NoMetricsWhenDisabled proves the observed handler functions
// normally when no registry is wired in (the default), so metrics stay strictly
// opt-in.
func TestObserve_NoMetricsWhenDisabled(t *testing.T) {
	srv := New(&config.Config{Auth: config.AuthConfig{Secret: "test-secret"}}, nil, nil, nil)
	rec := httptest.NewRecorder()
	srv.Observe(srv.Router()).ServeHTTP(rec, httptest.NewRequest("GET", "/api/auth/providers", nil))
	if rec.Code != 200 {
		t.Fatalf("GET /api/auth/providers returned %d with metrics disabled: %s", rec.Code, rec.Body.String())
	}
}

// TestObserve_RecordsRecoveredPanicAs500 proves a handler panic, recovered by
// the inner chi Recoverer, is still counted as a 500. Observation runs outside
// the recoverer, so the failures operators alert on are not silently dropped.
func TestObserve_RecordsRecoveredPanicAs500(t *testing.T) {
	srv := New(&config.Config{Auth: config.AuthConfig{Secret: "test-secret"}}, nil, nil, nil)
	reg := metrics.New("test")
	srv.SetMetrics(reg)

	// Mirror the production chain: a Recoverer is an inner chi middleware, so
	// the panic is converted to a 500 before observation reads the status.
	inner := chi.NewRouter()
	inner.Use(silentRecover)
	inner.Get("/api/boom", func(http.ResponseWriter, *http.Request) { panic("kaboom") })

	rec := httptest.NewRecorder()
	srv.Observe(inner).ServeHTTP(rec, httptest.NewRequest("GET", "/api/boom", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("client saw status %d, want 500", rec.Code)
	}

	body := scrape(t, reg)
	if !strings.Contains(body, `route="/api/boom",status="500"`) {
		t.Fatalf("recovered panic was not counted as a 500 against its route:\n%s", body)
	}
}

// TestObserve_RecordsTimeoutAs503 proves that when a request exceeds the timeout
// handler's deadline, the metric reflects the 503 the client actually received
// rather than the inner handler's eventual status. Observation wraps the timeout
// handler, so timed-out requests are labeled with the client-observed outcome.
func TestObserve_RecordsTimeoutAs503(t *testing.T) {
	srv := New(&config.Config{Auth: config.AuthConfig{Secret: "test-secret"}}, nil, nil, nil)
	reg := metrics.New("test")
	srv.SetMetrics(reg)

	inner := chi.NewRouter()
	inner.Get("/api/slow", func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // unblocks when the timeout handler cancels
	})
	timed := http.TimeoutHandler(inner, 5*time.Millisecond, `{"error":"timeout"}`)

	rec := httptest.NewRecorder()
	srv.Observe(timed).ServeHTTP(rec, httptest.NewRequest("GET", "/api/slow", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("client saw status %d, want 503", rec.Code)
	}

	body := scrape(t, reg)
	if !strings.Contains(body, `route="/api/slow",status="503"`) {
		t.Fatalf("timed-out request was not counted as a 503 against its route:\n%s", body)
	}
}
