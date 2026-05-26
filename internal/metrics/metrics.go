// Package metrics exposes Prometheus instrumentation for the ShinyHub server
// process itself - HTTP request counts/latency for the control-plane API, the
// Go runtime and process collectors, and build/version + uptime gauges. This is
// distinct from the per-managed-app CPU/RAM sampling in internal/process, which
// observes the Shiny app subprocesses rather than ShinyHub.
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Registry bundles a private Prometheus registry with the server's HTTP
// instruments. Construct with New; the zero value is not usable.
type Registry struct {
	reg              *prometheus.Registry
	httpRequests     *prometheus.CounterVec
	httpDuration     *prometheus.HistogramVec
	admissionRejects *prometheus.CounterVec
}

// New builds a Registry seeded with the Go runtime collector, the process
// collector (server RSS/CPU/FDs on Linux), a build_info gauge carrying version,
// and an uptime gauge. version is the build version string.
func New(version string) *Registry {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	buildInfo := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "shinyhub_build_info",
		Help: "ShinyHub build information; the value is always 1, the version is a label.",
	}, []string{"version"})
	buildInfo.WithLabelValues(version).Set(1)
	reg.MustRegister(buildInfo)

	start := time.Now()
	reg.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "shinyhub_uptime_seconds",
		Help: "Seconds since the ShinyHub server process started serving.",
	}, func() float64 { return time.Since(start).Seconds() }))

	httpRequests := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "shinyhub_http_requests_total",
		Help: "Total ShinyHub HTTP requests by method, matched route pattern, and status code.",
	}, []string{"method", "route", "status"})
	reg.MustRegister(httpRequests)

	httpDuration := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "shinyhub_http_request_duration_seconds",
		Help:    "ShinyHub HTTP request latency by method, matched route pattern, and status code.",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "route", "status"})
	reg.MustRegister(httpDuration)

	admissionRejects := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "shinyhub_admission_rejects_total",
		Help: "Total data-plane admission rejections by slug and reason. The slug label is __unknown__ for slugs that are not registered apps.",
	}, []string{"slug", "reason"})
	reg.MustRegister(admissionRejects)

	return &Registry{reg: reg, httpRequests: httpRequests, httpDuration: httpDuration, admissionRejects: admissionRejects}
}

// Handler returns the Prometheus scrape handler for this registry.
func (r *Registry) Handler() http.Handler {
	return promhttp.HandlerFor(r.reg, promhttp.HandlerOpts{})
}

// RecordReject increments the admission-rejects counter for the given slug and
// reason. Satisfies proxy.RejectRecorder so the proxy can record without
// importing Prometheus. slug is the caller's cardinality-guarded key.
func (r *Registry) RecordReject(slug, reason string) {
	r.admissionRejects.WithLabelValues(slug, reason).Inc()
}

// Middleware records a request count and latency observation for every request,
// labeled by the matched chi route PATTERN (not the raw path) so high-cardinality
// path parameters and unmatched 404 scans cannot explode the series count.
func (r *Registry) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ww := chimw.NewWrapResponseWriter(w, req.ProtoMajor)
		start := time.Now()
		next.ServeHTTP(ww, req)

		route := chi.RouteContext(req.Context()).RoutePattern()
		if route == "" {
			route = "unmatched"
		}
		status := ww.Status()
		if status == 0 {
			status = http.StatusOK
		}
		statusStr := strconv.Itoa(status)

		r.httpRequests.WithLabelValues(req.Method, route, statusStr).Inc()
		r.httpDuration.WithLabelValues(req.Method, route, statusStr).Observe(time.Since(start).Seconds())
	})
}
