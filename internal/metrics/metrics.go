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
	deploys          *prometheus.CounterVec
	stateTransitions *prometheus.CounterVec
	replicaRestarts  prometheus.Counter

	// Fargate AWS operation metrics.
	fargateRunTaskTotal         *prometheus.CounterVec
	fargateWaitIPTimeoutTotal   prometheus.Counter
	fargateStopTaskTotal        *prometheus.CounterVec
	fargateInventoryErrorsTotal prometheus.Counter
	fargateRunTaskDuration      prometheus.Histogram
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

	deploys := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "shinyhub_deploys_total",
		Help: "Total app deployments by result (success or failure).",
	}, []string{"result"})
	reg.MustRegister(deploys)

	stateTransitions := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "shinyhub_app_state_transitions_total",
		Help: "Total app lifecycle state transitions by event (hibernate, wake, restart).",
	}, []string{"event"})
	reg.MustRegister(stateTransitions)

	replicaRestarts := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "shinyhub_replica_restarts_total",
		Help: "Total replica crash-restarts performed by the watchdog.",
	})
	reg.MustRegister(replicaRestarts)

	fargateRunTaskTotal := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "shinyhub_fargate_run_task_total",
		Help: "Total ECS RunTask calls by result (ok or error).",
	}, []string{"result"})
	reg.MustRegister(fargateRunTaskTotal)

	fargateWaitIPTimeoutTotal := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "shinyhub_fargate_wait_ip_timeout_total",
		Help: "Total Fargate tasks that did not acquire an IP within the start timeout.",
	})
	reg.MustRegister(fargateWaitIPTimeoutTotal)

	fargateStopTaskTotal := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "shinyhub_fargate_stop_task_total",
		Help: "Total ECS StopTask calls by result (ok or error).",
	}, []string{"result"})
	reg.MustRegister(fargateStopTaskTotal)

	fargateInventoryErrorsTotal := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "shinyhub_fargate_inventory_errors_total",
		Help: "Total errors returned by the Fargate Inventory call (ListTasks or DescribeTasks failures).",
	})
	reg.MustRegister(fargateInventoryErrorsTotal)

	fargateRunTaskDuration := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "shinyhub_fargate_run_task_duration_seconds",
		Help:    "Latency of ECS RunTask calls from issue to response (not including IP-wait).",
		Buckets: []float64{0.1, 0.25, 0.5, 1.0, 2.5, 5.0, 10.0},
	})
	reg.MustRegister(fargateRunTaskDuration)

	return &Registry{
		reg:              reg,
		httpRequests:     httpRequests,
		httpDuration:     httpDuration,
		admissionRejects: admissionRejects,
		deploys:          deploys,
		stateTransitions: stateTransitions,
		replicaRestarts:  replicaRestarts,

		fargateRunTaskTotal:         fargateRunTaskTotal,
		fargateWaitIPTimeoutTotal:   fargateWaitIPTimeoutTotal,
		fargateStopTaskTotal:        fargateStopTaskTotal,
		fargateInventoryErrorsTotal: fargateInventoryErrorsTotal,
		fargateRunTaskDuration:      fargateRunTaskDuration,
	}
}

// RecordDeploy increments the deployment counter for the given result, which
// should be "success" or "failure".
func (r *Registry) RecordDeploy(result string) {
	r.deploys.WithLabelValues(result).Inc()
}

// RecordStateTransition increments the app lifecycle transition counter for the
// given event (e.g. "hibernate", "wake", "restart").
func (r *Registry) RecordStateTransition(event string) {
	r.stateTransitions.WithLabelValues(event).Inc()
}

// RecordReplicaRestart increments the replica crash-restart counter.
func (r *Registry) RecordReplicaRestart() {
	r.replicaRestarts.Inc()
}

// RegisterFleetGauges registers two GaugeFuncs reporting the live count of
// running apps and running replicas. The callbacks are evaluated lazily at
// scrape time, so the reported values always reflect current fleet state.
func (r *Registry) RegisterFleetGauges(appsRunning, replicasRunning func() float64) {
	r.reg.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "shinyhub_apps_running",
		Help: "Number of apps currently in the running state.",
	}, appsRunning))
	r.reg.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "shinyhub_replicas_running",
		Help: "Number of app replicas currently running.",
	}, replicasRunning))
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

// RecordRunTask satisfies fargate.FargateMetrics. result is "ok" or "error".
func (r *Registry) RecordRunTask(result string) {
	r.fargateRunTaskTotal.WithLabelValues(result).Inc()
}

// RecordWaitIPTimeout satisfies fargate.FargateMetrics.
func (r *Registry) RecordWaitIPTimeout() {
	r.fargateWaitIPTimeoutTotal.Inc()
}

// RecordStopTask satisfies fargate.FargateMetrics. result is "ok" or "error".
func (r *Registry) RecordStopTask(result string) {
	r.fargateStopTaskTotal.WithLabelValues(result).Inc()
}

// RecordInventoryError satisfies fargate.FargateMetrics.
func (r *Registry) RecordInventoryError() {
	r.fargateInventoryErrorsTotal.Inc()
}

// ObserveRunTaskLatency satisfies fargate.FargateMetrics. seconds is the
// duration from RunTask issue to response (not including IP-wait).
func (r *Registry) ObserveRunTaskLatency(seconds float64) {
	r.fargateRunTaskDuration.Observe(seconds)
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
