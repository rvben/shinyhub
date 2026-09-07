package metrics

import (
	"database/sql"

	"github.com/prometheus/client_golang/prometheus"
)

// RegisterDBStats observes the server's actual connection pool. These counters
// distinguish connection-pool queueing from query execution time; no DSN or
// query text is exported.
func (r *Registry) RegisterDBStats(stats func() sql.DBStats) {
	for _, item := range []struct {
		name, help string
		value      func(sql.DBStats) float64
	}{
		{"open_connections", "Open database connections.", func(s sql.DBStats) float64 { return float64(s.OpenConnections) }},
		{"in_use_connections", "Database connections currently in use.", func(s sql.DBStats) float64 { return float64(s.InUse) }},
		{"max_open_connections", "Configured database connection-pool limit.", func(s sql.DBStats) float64 { return float64(s.MaxOpenConnections) }},
	} {
		r.reg.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "shinyhub_db_" + item.name, Help: item.help}, func() float64 { return item.value(stats()) }))
	}
	r.reg.MustRegister(prometheus.NewCounterFunc(prometheus.CounterOpts{Name: "shinyhub_db_wait_count_total", Help: "Connection acquisitions that waited for the database pool."}, func() float64 { return float64(stats().WaitCount) }))
	r.reg.MustRegister(prometheus.NewCounterFunc(prometheus.CounterOpts{Name: "shinyhub_db_wait_duration_seconds_total", Help: "Total time spent waiting for a database connection, excluding query execution."}, func() float64 { return stats().WaitDuration.Seconds() }))
}
