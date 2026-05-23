package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
)

// sampleValue gathers the named metric from the registry and returns the value
// of the single sample whose label set is a superset of want. ok is false when
// no such sample exists.
func sampleValue(t *testing.T, reg *Registry, name string, want map[string]string) (float64, bool) {
	t.Helper()
	families, err := reg.reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, mf := range families {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			if !labelsMatch(m, want) {
				continue
			}
			switch mf.GetType() {
			case dto.MetricType_COUNTER:
				return m.GetCounter().GetValue(), true
			case dto.MetricType_GAUGE:
				return m.GetGauge().GetValue(), true
			case dto.MetricType_HISTOGRAM:
				return float64(m.GetHistogram().GetSampleCount()), true
			}
		}
	}
	return 0, false
}

func labelsMatch(m *dto.Metric, want map[string]string) bool {
	have := map[string]string{}
	for _, lp := range m.GetLabel() {
		have[lp.GetName()] = lp.GetValue()
	}
	for k, v := range want {
		if have[k] != v {
			return false
		}
	}
	return true
}

// TestMiddleware_CollapsesPathParamsToRoutePattern is the core cardinality
// guard: two requests to the same chi route with different path params must
// record against the route PATTERN (/api/apps/{slug}), never the raw path, so
// a high-cardinality slug space cannot explode the metric series count.
func TestMiddleware_CollapsesPathParamsToRoutePattern(t *testing.T) {
	reg := New("v1.2.3")
	r := chi.NewRouter()
	r.Use(reg.Middleware)
	r.Get("/api/apps/{slug}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	for _, slug := range []string{"alpha", "beta", "gamma"} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest("GET", "/api/apps/"+slug, nil))
	}

	got := testutil.ToFloat64(reg.httpRequests.WithLabelValues("GET", "/api/apps/{slug}", "200"))
	if got != 3 {
		t.Fatalf("requests_total{route=/api/apps/{slug},status=200} = %v, want 3 (every slug must collapse to the pattern)", got)
	}
	// The raw paths must NOT have produced their own series.
	if v := testutil.ToFloat64(reg.httpRequests.WithLabelValues("GET", "/api/apps/alpha", "200")); v != 0 {
		t.Fatalf("raw path /api/apps/alpha produced its own series (%v); cardinality is unbounded", v)
	}
}

// TestMiddleware_UnmatchedRouteCollapses proves a request that matches no route
// is recorded under a single constant label, not its raw (attacker-controlled)
// path - otherwise a 404 scan would explode the series count.
func TestMiddleware_UnmatchedRouteCollapses(t *testing.T) {
	reg := New("v1")
	r := chi.NewRouter()
	r.Use(reg.Middleware)
	r.Get("/known", func(w http.ResponseWriter, _ *http.Request) {})

	for _, p := range []string{"/nope/a", "/nope/b", "/whatever"} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
	}

	got := testutil.ToFloat64(reg.httpRequests.WithLabelValues("GET", "unmatched", "404"))
	if got != 3 {
		t.Fatalf("requests_total{route=unmatched,status=404} = %v, want 3 (all unmatched paths collapse)", got)
	}
}

// TestMiddleware_RecordsStatusAndDuration proves the status label reflects the
// real response code and a duration sample is observed for the route.
func TestMiddleware_RecordsStatusAndDuration(t *testing.T) {
	reg := New("v1")
	r := chi.NewRouter()
	r.Use(reg.Middleware)
	r.Get("/boom", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/boom", nil))

	if v := testutil.ToFloat64(reg.httpRequests.WithLabelValues("GET", "/boom", "500")); v != 1 {
		t.Fatalf("requests_total{route=/boom,status=500} = %v, want 1", v)
	}
	if v, ok := sampleValue(t, reg, "shinyhub_http_request_duration_seconds", map[string]string{"route": "/boom"}); !ok || v != 1 {
		t.Fatalf("duration histogram sample count for /boom = %v (ok=%v), want 1", v, ok)
	}
}

// TestMiddleware_DefaultsEmptyResponseToOK proves a handler that returns without
// writing a header or body (an implicit empty 200) is recorded as status 200,
// not the wrapper's uninitialized 0, so successful no-content responses land in
// the expected series rather than a phantom status="0".
func TestMiddleware_DefaultsEmptyResponseToOK(t *testing.T) {
	reg := New("v1")
	r := chi.NewRouter()
	r.Use(reg.Middleware)
	r.Get("/ping", func(http.ResponseWriter, *http.Request) {})
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/ping", nil))

	if v := testutil.ToFloat64(reg.httpRequests.WithLabelValues("GET", "/ping", "200")); v != 1 {
		t.Fatalf("requests_total{route=/ping,status=200} = %v, want 1", v)
	}
	if v := testutil.ToFloat64(reg.httpRequests.WithLabelValues("GET", "/ping", "0")); v != 0 {
		t.Fatalf("an empty response recorded a phantom status=0 series (%v)", v)
	}
}

// TestNew_ExposesBuildInfo proves the build/version is queryable at runtime as
// a labeled gauge set to 1 (the conventional build_info pattern).
func TestNew_ExposesBuildInfo(t *testing.T) {
	reg := New("v9.9.9")
	v, ok := sampleValue(t, reg, "shinyhub_build_info", map[string]string{"version": "v9.9.9"})
	if !ok || v != 1 {
		t.Fatalf("shinyhub_build_info{version=v9.9.9} = %v (ok=%v), want 1", v, ok)
	}
}

// TestHandler_ExposesRuntimeAndCustomMetrics proves the scrape endpoint serves
// the Go runtime collector plus ShinyHub's own build_info and uptime metrics.
func TestHandler_ExposesRuntimeAndCustomMetrics(t *testing.T) {
	reg := New("v1")
	rec := httptest.NewRecorder()
	reg.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("scrape returned %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"go_goroutines", "shinyhub_build_info", "shinyhub_uptime_seconds"} {
		if !strings.Contains(body, want) {
			t.Errorf("scrape output missing %q", want)
		}
	}
}
