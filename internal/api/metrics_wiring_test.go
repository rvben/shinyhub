package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
//
// The inner handler wraps the server's real router (so /api/auth/providers is
// resolved via s.router.Match) with a middleware that panics before delegating
// to the actual route handler. silentRecover converts the panic to a 500, and
// observation records that status against the matched route.
func TestObserve_RecordsRecoveredPanicAs500(t *testing.T) {
	srv := New(&config.Config{Auth: config.AuthConfig{Secret: "test-secret"}}, nil, nil, nil)
	reg := metrics.New("test")
	srv.SetMetrics(reg)

	// Mirror the production chain: a Recoverer sits inside the chi middleware
	// stack, so the panic is converted to a 500 before observation reads the
	// status. We inject a panicking middleware around the real server router so
	// /api/auth/providers is a route s.router.Match can resolve.
	router := srv.Router()
	inner := silentRecover(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = router // never reached; the panic fires first
		panic("kaboom")
	}))

	rec := httptest.NewRecorder()
	srv.Observe(inner).ServeHTTP(rec, httptest.NewRequest("GET", "/api/auth/providers", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("client saw status %d, want 500", rec.Code)
	}

	body := scrape(t, reg)
	if !strings.Contains(body, `route="/api/auth/providers",status="500"`) {
		t.Fatalf("recovered panic was not counted as a 500 against its route:\n%s", body)
	}
}

// TestObserve_RecordsTimeoutAs503 proves that when a request exceeds the timeout
// handler's deadline, the metric reflects the 503 the client actually received
// rather than the inner handler's eventual status. Observation wraps the timeout
// handler, so timed-out requests are labeled with the client-observed outcome.
//
// The inner handler wraps the server's real router (so /api/auth/providers is a
// registered route that s.router.Match resolves) with a blocking middleware that
// simulates a slow handler. This is the production-accurate shape: Observe
// resolves the route pattern via an independent s.router.Match before the inner
// chain runs, so a correct pattern label is available even when the timeout
// fires before the inner handler writes a status.
func TestObserve_RecordsTimeoutAs503(t *testing.T) {
	srv := New(&config.Config{Auth: config.AuthConfig{Secret: "test-secret"}}, nil, nil, nil)
	reg := metrics.New("test")
	srv.SetMetrics(reg)

	// Wrap the real server router with a middleware that blocks until the
	// request context is cancelled, simulating a handler that takes longer
	// than the timeout. /api/auth/providers is registered in the server's own
	// router so s.router.Match resolves it to its pattern.
	slow := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			<-r.Context().Done() // unblocks when the timeout handler cancels
		})
	}
	router := srv.Router()
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slow(router).ServeHTTP(w, r)
	})
	timed := http.TimeoutHandler(inner, 5*time.Millisecond, `{"error":"timeout"}`)

	rec := httptest.NewRecorder()
	srv.Observe(timed).ServeHTTP(rec, httptest.NewRequest("GET", "/api/auth/providers", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("client saw status %d, want 503", rec.Code)
	}

	body := scrape(t, reg)
	if !strings.Contains(body, `route="/api/auth/providers",status="503"`) {
		t.Fatalf("timed-out request was not counted as a 503 against its route:\n%s", body)
	}
}
