package main

import (
	"log/slog"

	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/metrics"
	"github.com/rvben/shinyhub/internal/proxy"
	"github.com/rvben/shinyhub/internal/upgrade"
	"github.com/rvben/shinyhub/internal/usage"
)

// serverMetricsDeps is what the server self-telemetry registry reads from.
// usageRecorder is nil when usage recording is disabled.
type serverMetricsDeps struct {
	version       string
	logger        *slog.Logger
	store         *db.Store
	srv           *api.Server
	prx           *proxy.Proxy
	usageRecorder *usage.Recorder
	relayLeases   *upgrade.RelayLeases
}

// newServerMetrics builds the server's Prometheus registry and wires every
// component that reports into it: API request instrumentation, proxy
// admission rejects, store health and audit/log persistence errors, plus the
// scrape-time collectors for the fleet, retired generations, schedules and
// per-app sessions. Serving the registry is the caller's job.
func newServerMetrics(d serverMetricsDeps) *metrics.Registry {
	reg := metrics.New(d.version)
	reg.RegisterDBStats(d.store.Stats)
	if d.usageRecorder != nil {
		d.usageRecorder.SetMetrics(reg)
	}
	d.srv.SetMetrics(reg)
	d.prx.SetRejectRecorder(reg)
	reg.RegisterFleetGauges(fleetCounts(d.store))
	reg.RegisterRetiredGauges(func() (int, int64, bool) {
		st := d.relayLeases.Status()
		return st.Count, st.Sessions, st.Unknown
	})
	// Schedule freshness is DB-backed, so it survives restarts.
	if err := reg.Register(metrics.NewScheduleFreshnessCollector(scheduleSamples(d.store))); err != nil {
		d.logger.Warn("register schedule freshness collector", "err", err)
	}
	// Session gauges are sampled live from the proxy at scrape time, so a
	// dashboard can show sessions against the ceiling without polling the API.
	if err := reg.Register(metrics.NewSessionGaugesCollector(sessionSamples(d.prx.PoolSessionSnapshot))); err != nil {
		d.logger.Warn("register session gauges collector", "err", err)
	}
	// Dropped audit events (e.g. disk full) become a counter, so silent loss
	// of the compliance trail is alertable.
	d.store.SetAuditErrorHook(reg.RecordAuditWriteError)
	// Shared-log writers capture this recorder when they are created, keeping
	// persistence health observable without db importing Prometheus.
	d.store.SetAppLogMetrics(reg)
	return reg
}

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

// scheduleSamples reads one freshness sample per schedule from the store.
func scheduleSamples(store *db.Store) func() ([]metrics.ScheduleSample, error) {
	return func() ([]metrics.ScheduleSample, error) {
		rows, err := store.ScheduleFreshness()
		if err != nil {
			return nil, err
		}
		out := make([]metrics.ScheduleSample, len(rows))
		for i, fr := range rows {
			out[i] = scheduleSample(fr)
		}
		return out, nil
	}
}

// scheduleSample converts one freshness row. An event that never happened
// stays zero, which the collector reads as absent; OK separately records
// whether the schedule has ever succeeded.
func scheduleSample(fr db.ScheduleFreshness) metrics.ScheduleSample {
	s := metrics.ScheduleSample{
		Slug: fr.Slug, Name: fr.Name, Enabled: fr.Enabled,
		ConsecutiveFailures: fr.ConsecutiveFailures,
		ActivationStatus:    fr.ActivationStatus,
		CapacityDeferrals:   fr.ActivationCapacityDeferrals,
	}
	if fr.LastFailureAt != nil {
		s.LastFailureUnix = fr.LastFailureAt.Unix()
	}
	if fr.LastInterruptedAt != nil {
		s.LastInterruptedUnix = fr.LastInterruptedAt.Unix()
	}
	if fr.LastSuccessAt != nil {
		s.LastSuccessUnix = fr.LastSuccessAt.Unix()
		s.OK = true
	}
	if fr.ActivationCreatedAt != nil {
		s.ActivationCreatedUnix = fr.ActivationCreatedAt.Unix()
	}
	if fr.ActivationCapacityDeferredAt != nil {
		s.CapacityDeferredUnix = fr.ActivationCapacityDeferredAt.Unix()
	}
	if fr.ActivationGeneration != nil {
		s.ActivationGeneration = *fr.ActivationGeneration
	}
	return s
}

// sessionSamples turns the proxy's per-pool session snapshot into gauge
// samples. The admission ceiling is the per-replica cap times the live
// replica count; an uncapped pool reports Cap 0 and the collector emits only
// its sessions gauge.
func sessionSamples(snapshot func() map[string]proxy.PoolSessionStat) func() []metrics.SessionSample {
	return func() []metrics.SessionSample {
		stats := snapshot()
		out := make([]metrics.SessionSample, 0, len(stats))
		for slug, st := range stats {
			out = append(out, metrics.SessionSample{
				Slug: slug, Sessions: st.Sessions, Cap: st.Cap, Replicas: st.Replicas,
			})
		}
		return out
	}
}
