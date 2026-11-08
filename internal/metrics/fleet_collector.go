package metrics

import "github.com/prometheus/client_golang/prometheus"

// FleetCounts is one scrape-time reading of the fleet. main.go fills it from
// the store so the metrics package never imports db.
type FleetCounts struct {
	AppsRunning     int64
	ReplicasRunning int64
	AppsCrashed     int64
}

var (
	fleetAppsRunningDesc = prometheus.NewDesc(
		"shinyhub_apps_running",
		"Number of apps currently in the running state.",
		nil, nil,
	)
	fleetReplicasRunningDesc = prometheus.NewDesc(
		"shinyhub_replicas_running",
		"Number of app replicas currently running.",
		nil, nil,
	)
	fleetAppsCrashedDesc = prometheus.NewDesc(
		"shinyhub_apps_crashed",
		"Number of apps currently in the crashed state (exhausted restart budget, serving nothing).",
		nil, nil,
	)
	fleetCollectorUpDesc = prometheus.NewDesc(
		"shinyhub_fleet_collector_up",
		"1 when the fleet count query succeeded on this scrape, 0 when it failed and every other fleet gauge is absent.",
		nil, nil,
	)
)

type fleetCollector struct {
	query func() (FleetCounts, error)
}

func (c *fleetCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- fleetAppsRunningDesc
	ch <- fleetReplicasRunningDesc
	ch <- fleetAppsCrashedDesc
	ch <- fleetCollectorUpDesc
}

func (c *fleetCollector) Collect(ch chan<- prometheus.Metric) {
	counts, err := c.query()
	if err != nil {
		// A count that could not be taken is absent, not zero: a zero
		// shinyhub_apps_crashed would clear the crashed-app alert for exactly
		// as long as the database is unhealthy. The up gauge makes the gap
		// visible.
		ch <- prometheus.MustNewConstMetric(fleetCollectorUpDesc, prometheus.GaugeValue, 0)
		return
	}
	ch <- prometheus.MustNewConstMetric(fleetCollectorUpDesc, prometheus.GaugeValue, 1)
	ch <- prometheus.MustNewConstMetric(fleetAppsRunningDesc, prometheus.GaugeValue, float64(counts.AppsRunning))
	ch <- prometheus.MustNewConstMetric(fleetReplicasRunningDesc, prometheus.GaugeValue, float64(counts.ReplicasRunning))
	ch <- prometheus.MustNewConstMetric(fleetAppsCrashedDesc, prometheus.GaugeValue, float64(counts.AppsCrashed))
}
