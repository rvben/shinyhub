package metrics

import (
	"errors"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestSeedSchedule_PublishesAlertableSeriesAtZero(t *testing.T) {
	reg := New("test")
	reg.SeedSchedule("sales", "nightly")

	for _, status := range []string{"succeeded", "failed", "timed_out", "interrupted"} {
		if v, ok := sampleValue(t, reg, "shinyhub_schedule_runs_total",
			map[string]string{"slug": "sales", "schedule": "nightly", "status": status}); !ok || v != 0 {
			t.Errorf("runs_total{status=%q} = %v (present=%v), want present at 0", status, v, ok)
		}
	}
	for _, reason := range []string{"app_deploying", "quarantined", "error"} {
		if v, ok := sampleValue(t, reg, "shinyhub_schedule_dispatch_failures_total",
			map[string]string{"slug": "sales", "schedule": "nightly", "reason": reason}); !ok || v != 0 {
			t.Errorf("dispatch_failures_total{reason=%q} = %v (present=%v), want present at 0", reason, v, ok)
		}
	}

	// Re-seeding on a reload must not reset a count already recorded.
	reg.RecordScheduleRun("sales", "nightly", "failed")
	reg.RecordScheduleDispatchFailure("sales", "nightly", "quarantined")
	reg.SeedSchedule("sales", "nightly")
	if v, _ := sampleValue(t, reg, "shinyhub_schedule_runs_total",
		map[string]string{"slug": "sales", "schedule": "nightly", "status": "failed"}); v != 1 {
		t.Errorf("failed count after reseed = %v, want 1", v)
	}
	if v, _ := sampleValue(t, reg, "shinyhub_schedule_dispatch_failures_total",
		map[string]string{"slug": "sales", "schedule": "nightly", "reason": "quarantined"}); v != 1 {
		t.Errorf("quarantined count after reseed = %v, want 1", v)
	}
}

func TestForgetSchedule_RemovesDispatchFailureSeries(t *testing.T) {
	reg := New("test")
	reg.SeedSchedule("sales", "old-name")
	reg.SeedSchedule("sales", "other")
	reg.RecordScheduleDispatchFailure("sales", "old-name", "error")

	reg.ForgetSchedule("sales", "old-name")

	for _, name := range []string{"shinyhub_schedule_runs_total", "shinyhub_schedule_dispatch_failures_total"} {
		if _, ok := sampleValue(t, reg, name, map[string]string{"slug": "sales", "schedule": "old-name"}); ok {
			t.Errorf("%s for the forgotten schedule must be gone", name)
		}
		if _, ok := sampleValue(t, reg, name, map[string]string{"slug": "sales", "schedule": "other"}); !ok {
			t.Errorf("%s for a sibling schedule must survive", name)
		}
	}
}

func TestForgetApp_RemovesDispatchFailureSeries(t *testing.T) {
	reg := New("test")
	reg.RecordScheduleDispatchFailure("gone", "nightly", "error")
	reg.RecordScheduleDispatchFailure("keeper", "nightly", "error")

	reg.ForgetApp("gone")

	if _, ok := sampleValue(t, reg, "shinyhub_schedule_dispatch_failures_total", map[string]string{"slug": "gone"}); ok {
		t.Error("dispatch_failures series for slug=gone must be gone after ForgetApp")
	}
	if v, ok := sampleValue(t, reg, "shinyhub_schedule_dispatch_failures_total", map[string]string{"slug": "keeper"}); !ok || v != 1 {
		t.Errorf("sibling slug=keeper must survive, got %v (ok=%v)", v, ok)
	}
}

func gatherCollector(t *testing.T, query func() ([]ScheduleSample, error)) map[string]*dto.MetricFamily {
	t.Helper()
	reg := prometheus.NewRegistry()
	reg.MustRegister(NewScheduleFreshnessCollector(query))
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	out := map[string]*dto.MetricFamily{}
	for _, mf := range mfs {
		out[mf.GetName()] = mf
	}
	return out
}

func gaugeFor(fam *dto.MetricFamily, schedule string) (float64, bool) {
	if fam == nil {
		return 0, false
	}
	for _, m := range fam.Metric {
		for _, lp := range m.Label {
			if lp.GetName() == "schedule" && lp.GetValue() == schedule {
				return m.GetGauge().GetValue(), true
			}
		}
	}
	return 0, false
}

func TestScheduleCollector_FailureStreakGauges(t *testing.T) {
	fams := gatherCollector(t, func() ([]ScheduleSample, error) {
		return []ScheduleSample{
			// Never succeeded, failing: the case a last-success alert cannot see.
			{Slug: "etl", Name: "broken", Enabled: true, ConsecutiveFailures: 3, LastFailureUnix: 1751284800, LastInterruptedUnix: 1751284500},
			// Disabled, healthy, never failed.
			{Slug: "etl", Name: "paused", Enabled: false, OK: true, LastSuccessUnix: 1751284000},
		}, nil
	})

	if up := fams["shinyhub_schedule_collector_up"]; up == nil || len(up.Metric) != 1 || up.Metric[0].GetGauge().GetValue() != 1 {
		t.Fatalf("collector_up = %+v, want 1", up)
	}
	if v, ok := gaugeFor(fams["shinyhub_schedule_consecutive_failures"], "broken"); !ok || v != 3 {
		t.Errorf("consecutive_failures{broken} = %v (present=%v), want 3", v, ok)
	}
	if v, ok := gaugeFor(fams["shinyhub_schedule_consecutive_failures"], "paused"); !ok || v != 0 {
		t.Errorf("consecutive_failures{paused} = %v (present=%v), want present at 0", v, ok)
	}
	if v, ok := gaugeFor(fams["shinyhub_schedule_enabled"], "broken"); !ok || v != 1 {
		t.Errorf("enabled{broken} = %v (present=%v), want 1", v, ok)
	}
	if v, ok := gaugeFor(fams["shinyhub_schedule_enabled"], "paused"); !ok || v != 0 {
		t.Errorf("enabled{paused} = %v (present=%v), want present at 0", v, ok)
	}
	if v, ok := gaugeFor(fams["shinyhub_schedule_last_failure_seconds"], "broken"); !ok || v != 1751284800 {
		t.Errorf("last_failure_seconds{broken} = %v (present=%v), want 1751284800", v, ok)
	}
	// No failure on record is absence, not a timestamp of 0 (1970).
	if v, ok := gaugeFor(fams["shinyhub_schedule_last_failure_seconds"], "paused"); ok {
		t.Errorf("last_failure_seconds{paused} = %v, want absent", v)
	}
	if v, ok := gaugeFor(fams["shinyhub_schedule_last_interrupted_seconds"], "broken"); !ok || v != 1751284500 {
		t.Errorf("last_interrupted_seconds{broken} = %v (present=%v), want 1751284500", v, ok)
	}
	if v, ok := gaugeFor(fams["shinyhub_schedule_last_interrupted_seconds"], "paused"); ok {
		t.Errorf("last_interrupted_seconds{paused} = %v, want absent", v)
	}
}

func TestScheduleCollector_QueryErrorIsVisible(t *testing.T) {
	fams := gatherCollector(t, func() ([]ScheduleSample, error) {
		return nil, errors.New("database is locked")
	})
	up := fams["shinyhub_schedule_collector_up"]
	if up == nil || len(up.Metric) != 1 || up.Metric[0].GetGauge().GetValue() != 0 {
		t.Fatalf("collector_up = %+v, want 0 on query error", up)
	}
	if len(fams) != 1 {
		t.Fatalf("families on query error = %d, want only collector_up", len(fams))
	}

	// An empty schedule list is a successful answer, not an outage.
	fams = gatherCollector(t, func() ([]ScheduleSample, error) { return nil, nil })
	if up := fams["shinyhub_schedule_collector_up"]; up == nil || up.Metric[0].GetGauge().GetValue() != 1 {
		t.Fatalf("collector_up with no schedules = %+v, want 1", up)
	}
}
