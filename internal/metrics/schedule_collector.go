package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// ScheduleSample is the metrics-local view of one schedule's run health.
// main.go adapts db.ScheduleFreshness into this so the metrics package never
// imports db. OK is false for a schedule that has never succeeded, in which
// case no last-success sample is emitted (absence = never succeeded); the
// enabled gauge is what lets an alert tell that apart from a deleted schedule.
type ScheduleSample struct {
	Slug                  string
	Name                  string
	Enabled               bool
	LastSuccessUnix       int64
	OK                    bool
	ConsecutiveFailures   int
	LastFailureUnix       int64 // 0 when the schedule has no failed or timed_out run
	LastInterruptedUnix   int64 // 0 when the schedule has no interrupted run
	ActivationStatus      string
	ActivationCreatedUnix int64
	ActivationGeneration  int64
	CapacityDeferredUnix  int64
	CapacityDeferrals     int
	FreshnessKnown        bool
	Stale                 bool
	StalenessSeconds      float64
	ExpectedSuccessUnix   int64 // absent (0) for disabled or unknown schedules
	NextRunUnix           int64 // absent (0) for disabled, unknown, or @every schedules
}

var scheduleFreshnessKnownDesc = prometheus.NewDesc(
	"shinyhub_schedule_freshness_known",
	"1 when schedule freshness is known, 0 when cron or timezone evaluation failed or the cron never fires.",
	[]string{"slug", "schedule"}, nil,
)

var scheduleStaleDesc = prometheus.NewDesc(
	"shinyhub_schedule_stale",
	"1 when enabled schedule data is overdue under the API freshness policy, 0 otherwise. Absent when freshness is unknown.",
	[]string{"slug", "schedule"}, nil,
)

var scheduleStalenessDesc = prometheus.NewDesc(
	"shinyhub_schedule_staleness_seconds",
	"Seconds past the first unmet expected success deadline, clamped to zero. Absent when freshness is unknown.",
	[]string{"slug", "schedule"}, nil,
)

var scheduleExpectedSuccessDesc = prometheus.NewDesc(
	"shinyhub_schedule_expected_success_timestamp_seconds",
	"Unix timestamp of the first cron fire after last success or creation, plus the 10-minute freshness margin. Absent when disabled or unknown.",
	[]string{"slug", "schedule"}, nil,
)

var scheduleNextRunDesc = prometheus.NewDesc(
	"shinyhub_schedule_next_run_timestamp_seconds",
	"Unix timestamp of the next nominal cron fire after the scrape time. Absent when disabled, unknown, or using an @every interval.",
	[]string{"slug", "schedule"}, nil,
)

var scheduleLastSuccessDesc = prometheus.NewDesc(
	"shinyhub_schedule_last_success_seconds",
	"Unix timestamp of the last successful run of a schedule.",
	[]string{"slug", "schedule"}, nil,
)

var scheduleEnabledDesc = prometheus.NewDesc(
	"shinyhub_schedule_enabled",
	"1 when a schedule is enabled, 0 when disabled or retired. Emitted for every schedule.",
	[]string{"slug", "schedule"}, nil,
)

var scheduleConsecutiveFailuresDesc = prometheus.NewDesc(
	"shinyhub_schedule_consecutive_failures",
	"Failed or timed_out runs since the schedule's latest succeeded run, within retained run history. Emitted for every schedule.",
	[]string{"slug", "schedule"}, nil,
)

var scheduleLastFailureDesc = prometheus.NewDesc(
	"shinyhub_schedule_last_failure_seconds",
	"Unix timestamp at which the schedule's newest failed or timed_out run finished. Absent when there is none.",
	[]string{"slug", "schedule"}, nil,
)

var scheduleLastInterruptedDesc = prometheus.NewDesc(
	"shinyhub_schedule_last_interrupted_seconds",
	"Unix timestamp at which the schedule's newest interrupted run was closed. Absent when there is none.",
	[]string{"slug", "schedule"}, nil,
)

var scheduleCollectorUpDesc = prometheus.NewDesc(
	"shinyhub_schedule_collector_up",
	"1 when the schedule metrics query succeeded on this scrape, 0 when it failed and every other schedule gauge is absent.",
	nil, nil,
)

var scheduleActivationStatusDesc = prometheus.NewDesc(
	"shinyhub_schedule_activation_status",
	"Current durable serving-data activation state (1 for the labeled state).",
	[]string{"slug", "schedule", "status"}, nil,
)

var scheduleActivationAgeDesc = prometheus.NewDesc(
	"shinyhub_schedule_activation_age_seconds",
	"Age in seconds of the current nonterminal serving-data activation.",
	[]string{"slug", "schedule"}, nil,
)

var scheduleActivationGenerationDesc = prometheus.NewDesc(
	"shinyhub_schedule_activation_target_generation",
	"Target serving-data generation of the latest activation.",
	[]string{"slug", "schedule"}, nil,
)

var scheduleCapacityDeferralsDesc = prometheus.NewDesc(
	"shinyhub_schedule_activation_capacity_deferrals",
	"Capacity deferral count of the latest activation, retained across coalesced producer successes.",
	[]string{"slug", "schedule"}, nil,
)
var scheduleCapacityDeferredAgeDesc = prometheus.NewDesc(
	"shinyhub_schedule_activation_capacity_deferred_age_seconds",
	"Seconds since the first capacity deferral of an outstanding activation, retained across coalesced producer successes.",
	[]string{"slug", "schedule"}, nil,
)

// scheduleFreshnessCollector emits per-schedule run-health gauges, querying the
// store live at scrape time so the values survive a process restart (DB-backed,
// like apps_crashed).
type scheduleFreshnessCollector struct {
	query func() ([]ScheduleSample, error)
}

// NewScheduleFreshnessCollector builds the DB-backed run-health and cron
// freshness collector. The adapter computes all timing at one scrape instant.
func NewScheduleFreshnessCollector(query func() ([]ScheduleSample, error)) prometheus.Collector {
	return &scheduleFreshnessCollector{query: query}
}

func (c *scheduleFreshnessCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- scheduleLastSuccessDesc
	ch <- scheduleEnabledDesc
	ch <- scheduleConsecutiveFailuresDesc
	ch <- scheduleLastFailureDesc
	ch <- scheduleLastInterruptedDesc
	ch <- scheduleCollectorUpDesc
	ch <- scheduleActivationStatusDesc
	ch <- scheduleActivationAgeDesc
	ch <- scheduleActivationGenerationDesc
	ch <- scheduleCapacityDeferralsDesc
	ch <- scheduleCapacityDeferredAgeDesc
	ch <- scheduleFreshnessKnownDesc
	ch <- scheduleStaleDesc
	ch <- scheduleStalenessDesc
	ch <- scheduleExpectedSuccessDesc
	ch <- scheduleNextRunDesc
}

func (c *scheduleFreshnessCollector) Collect(ch chan<- prometheus.Metric) {
	samples, err := c.query()
	if err != nil {
		// A scrape-time DB error yields no schedule samples rather than a broken
		// scrape. The up gauge makes that gap visible: without it, every
		// absent-series alert would read the outage as "no schedules".
		ch <- prometheus.MustNewConstMetric(scheduleCollectorUpDesc, prometheus.GaugeValue, 0)
		return
	}
	ch <- prometheus.MustNewConstMetric(scheduleCollectorUpDesc, prometheus.GaugeValue, 1)
	now := time.Now().Unix()
	for _, s := range samples {
		known := 0.0
		if s.FreshnessKnown {
			known = 1
			stale := 0.0
			if s.Stale {
				stale = 1
			}
			ch <- prometheus.MustNewConstMetric(scheduleStaleDesc, prometheus.GaugeValue, stale, s.Slug, s.Name)
			ch <- prometheus.MustNewConstMetric(scheduleStalenessDesc, prometheus.GaugeValue, s.StalenessSeconds, s.Slug, s.Name)
			if s.ExpectedSuccessUnix > 0 {
				ch <- prometheus.MustNewConstMetric(scheduleExpectedSuccessDesc, prometheus.GaugeValue, float64(s.ExpectedSuccessUnix), s.Slug, s.Name)
			}
			if s.NextRunUnix > 0 {
				ch <- prometheus.MustNewConstMetric(scheduleNextRunDesc, prometheus.GaugeValue, float64(s.NextRunUnix), s.Slug, s.Name)
			}
		}
		ch <- prometheus.MustNewConstMetric(scheduleFreshnessKnownDesc, prometheus.GaugeValue, known, s.Slug, s.Name)
		enabled := 0.0
		if s.Enabled {
			enabled = 1
		}
		ch <- prometheus.MustNewConstMetric(scheduleEnabledDesc, prometheus.GaugeValue,
			enabled, s.Slug, s.Name)
		ch <- prometheus.MustNewConstMetric(scheduleConsecutiveFailuresDesc, prometheus.GaugeValue,
			float64(s.ConsecutiveFailures), s.Slug, s.Name)
		if s.LastFailureUnix > 0 {
			ch <- prometheus.MustNewConstMetric(scheduleLastFailureDesc, prometheus.GaugeValue,
				float64(s.LastFailureUnix), s.Slug, s.Name)
		}
		if s.LastInterruptedUnix > 0 {
			ch <- prometheus.MustNewConstMetric(scheduleLastInterruptedDesc, prometheus.GaugeValue,
				float64(s.LastInterruptedUnix), s.Slug, s.Name)
		}
		if s.OK {
			ch <- prometheus.MustNewConstMetric(
				scheduleLastSuccessDesc, prometheus.GaugeValue,
				float64(s.LastSuccessUnix), s.Slug, s.Name,
			)
		}
		if s.ActivationStatus == "" {
			continue
		}
		ch <- prometheus.MustNewConstMetric(scheduleActivationStatusDesc, prometheus.GaugeValue,
			1, s.Slug, s.Name, s.ActivationStatus)
		ch <- prometheus.MustNewConstMetric(scheduleCapacityDeferralsDesc, prometheus.GaugeValue,
			float64(s.CapacityDeferrals), s.Slug, s.Name)
		if s.CapacityDeferredUnix > 0 && nonterminalActivationMetricStatus(s.ActivationStatus) {
			age := max(int64(0), now-s.CapacityDeferredUnix)
			ch <- prometheus.MustNewConstMetric(scheduleCapacityDeferredAgeDesc, prometheus.GaugeValue,
				float64(age), s.Slug, s.Name)
		}
		if s.ActivationGeneration > 0 {
			ch <- prometheus.MustNewConstMetric(scheduleActivationGenerationDesc, prometheus.GaugeValue,
				float64(s.ActivationGeneration), s.Slug, s.Name)
		}
		if s.ActivationCreatedUnix > 0 && nonterminalActivationMetricStatus(s.ActivationStatus) {
			age := now - s.ActivationCreatedUnix
			if age < 0 {
				age = 0
			}
			ch <- prometheus.MustNewConstMetric(scheduleActivationAgeDesc, prometheus.GaugeValue,
				float64(age), s.Slug, s.Name)
		}
	}
}

func nonterminalActivationMetricStatus(status string) bool {
	switch status {
	case "pending", "deferred_interval", "deferred_capacity", "repairing", "running":
		return true
	default:
		return false
	}
}
