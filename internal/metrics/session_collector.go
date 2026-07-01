package metrics

import "github.com/prometheus/client_golang/prometheus"

// SessionSample is the metrics-local view of one app's live session usage.
// main.go adapts proxy.PoolSessionStat into this so the metrics package never
// imports proxy. Cap is the per-replica session cap (0 = uncapped); Replicas is
// the count of replicas admitting new sessions (live, not draining). The
// collector derives the admission ceiling and decides emission.
type SessionSample struct {
	Slug     string
	Sessions int
	Cap      int
	Replicas int
}

var (
	appSessionsDesc = prometheus.NewDesc(
		"shinyhub_app_sessions",
		"Current active proxied sessions for an app, summed across live replicas.",
		[]string{"slug"}, nil,
	)
	appSessionsLimitDesc = prometheus.NewDesc(
		"shinyhub_app_sessions_limit",
		"Admission ceiling for an app (live replicas times per-replica session cap). Absent for uncapped apps.",
		[]string{"slug"}, nil,
	)
)

// sessionGaugesCollector emits per-app session and admission-ceiling gauges,
// sampling the proxy live at scrape time so the values never go stale. Mirrors
// scheduleFreshnessCollector's lazy-collect design.
type sessionGaugesCollector struct {
	sample func() []SessionSample
}

// NewSessionGaugesCollector builds the proxy-backed per-app session collector.
func NewSessionGaugesCollector(sample func() []SessionSample) prometheus.Collector {
	return &sessionGaugesCollector{sample: sample}
}

func (c *sessionGaugesCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- appSessionsDesc
	ch <- appSessionsLimitDesc
}

func (c *sessionGaugesCollector) Collect(ch chan<- prometheus.Metric) {
	for _, s := range c.sample() {
		ch <- prometheus.MustNewConstMetric(
			appSessionsDesc, prometheus.GaugeValue, float64(s.Sessions), s.Slug,
		)
		// The limit series is emitted iff the pool is capped. The value is the
		// current admission ceiling (cap times admitting replicas), which is 0
		// when every replica is draining - so a capped, zero-capacity pool still
		// exposes a saturation signal (sessions/limit = +Inf) rather than going
		// silent. Uncapped pools have no meaningful ceiling and emit no series.
		if s.Cap > 0 {
			ch <- prometheus.MustNewConstMetric(
				appSessionsLimitDesc, prometheus.GaugeValue, float64(s.Cap*s.Replicas), s.Slug,
			)
		}
	}
}
