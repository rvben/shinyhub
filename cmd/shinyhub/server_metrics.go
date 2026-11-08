package main

import (
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/metrics"
)

// fleetCounts reads the live fleet counts from the store at scrape time, so
// "apps/replicas running right now" is answerable from Prometheus alone. Any
// failed count fails the whole reading; a partial one would mix a real value
// with a missing one.
func fleetCounts(store *db.Store) func() (metrics.FleetCounts, error) {
	return func() (metrics.FleetCounts, error) {
		var c metrics.FleetCounts
		var err error
		if c.AppsRunning, err = store.CountRunningApps(); err != nil {
			return metrics.FleetCounts{}, err
		}
		if c.ReplicasRunning, err = store.CountRunningReplicas(); err != nil {
			return metrics.FleetCounts{}, err
		}
		if c.AppsCrashed, err = store.CountCrashedApps(); err != nil {
			return metrics.FleetCounts{}, err
		}
		return c, nil
	}
}
