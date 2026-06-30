package metrics

import "github.com/prometheus/client_golang/prometheus"

// ScheduleSample is the metrics-local view of one schedule's last-success
// state. main.go adapts db.ScheduleFreshness into this so the metrics package
// never imports db. OK is false for a schedule that has never succeeded, in
// which case the collector emits no sample (absence = never succeeded).
type ScheduleSample struct {
	Slug            string
	Name            string
	LastSuccessUnix int64
	OK              bool
}

var scheduleLastSuccessDesc = prometheus.NewDesc(
	"shinyhub_schedule_last_success_seconds",
	"Unix timestamp of the last successful run of a schedule.",
	[]string{"slug", "schedule"}, nil,
)

// scheduleFreshnessCollector emits one gauge per schedule that has ever
// succeeded, querying the store live at scrape time so the value survives a
// process restart (DB-backed, like apps_crashed).
type scheduleFreshnessCollector struct {
	query func() ([]ScheduleSample, error)
}

// NewScheduleFreshnessCollector builds the DB-backed last-success collector.
func NewScheduleFreshnessCollector(query func() ([]ScheduleSample, error)) prometheus.Collector {
	return &scheduleFreshnessCollector{query: query}
}

func (c *scheduleFreshnessCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- scheduleLastSuccessDesc
}

func (c *scheduleFreshnessCollector) Collect(ch chan<- prometheus.Metric) {
	samples, err := c.query()
	if err != nil {
		return // a scrape-time DB error yields no samples rather than a broken scrape
	}
	for _, s := range samples {
		if !s.OK {
			continue
		}
		ch <- prometheus.MustNewConstMetric(
			scheduleLastSuccessDesc, prometheus.GaugeValue,
			float64(s.LastSuccessUnix), s.Slug, s.Name,
		)
	}
}
