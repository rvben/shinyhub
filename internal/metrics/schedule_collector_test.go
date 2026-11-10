package metrics

import (
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestScheduleFreshnessMetricsDistinguishUnknownAndDisabled(t *testing.T) {
	reg := prometheus.NewPedanticRegistry()
	reg.MustRegister(NewScheduleFreshnessCollector(func() ([]ScheduleSample, error) {
		return []ScheduleSample{
			{Slug: "app", Name: "overdue", Enabled: true, FreshnessKnown: true, Stale: true, StalenessSeconds: 1800.5, ExpectedSuccessUnix: 1700000000, NextRunUnix: 1700086400},
			{Slug: "app", Name: "fresh", Enabled: true, FreshnessKnown: true, ExpectedSuccessUnix: 1700086400, NextRunUnix: 1700085800},
			{Slug: "app", Name: "disabled", FreshnessKnown: true},
			{Slug: "app", Name: "unknown", Enabled: true},
		}, nil
	}))
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]map[string]float64{}
	for _, f := range families {
		values[f.GetName()] = map[string]float64{}
		for _, m := range f.Metric {
			name := ""
			for _, label := range m.Label {
				if label.GetName() == "schedule" {
					name = label.GetValue()
				}
			}
			values[f.GetName()][name] = m.GetGauge().GetValue()
		}
	}
	for metric, want := range map[string]map[string]float64{
		"shinyhub_schedule_freshness_known":                    {"overdue": 1, "fresh": 1, "disabled": 1, "unknown": 0},
		"shinyhub_schedule_stale":                              {"overdue": 1, "fresh": 0, "disabled": 0},
		"shinyhub_schedule_staleness_seconds":                  {"overdue": 1800.5, "fresh": 0, "disabled": 0},
		"shinyhub_schedule_expected_success_timestamp_seconds": {"overdue": 1700000000, "fresh": 1700086400},
		"shinyhub_schedule_next_run_timestamp_seconds":         {"overdue": 1700086400, "fresh": 1700085800},
	} {
		got := values[metric]
		if len(got) != len(want) {
			t.Errorf("%s series=%v want=%v", metric, got, want)
		}
		for name, v := range want {
			if actual, ok := got[name]; !ok || actual != v {
				t.Errorf("%s[%s]=%v present=%t want=%v", metric, name, actual, ok, v)
			}
		}
	}
}

func TestScheduleFreshnessQueryFailureDoesNotReportFresh(t *testing.T) {
	reg := prometheus.NewPedanticRegistry()
	reg.MustRegister(NewScheduleFreshnessCollector(func() ([]ScheduleSample, error) { return nil, errors.New("database unavailable") }))
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	if len(families) != 1 || families[0].GetName() != "shinyhub_schedule_collector_up" || families[0].Metric[0].GetGauge().GetValue() != 0 {
		t.Fatalf("failed scrape=%v", families)
	}
}

func TestScheduleFreshnessCollector(t *testing.T) {
	const ts = int64(1751284800) // fixed unix timestamp
	query := func() ([]ScheduleSample, error) {
		return []ScheduleSample{
			{Slug: "alpha-dash", Name: "refresh-data", LastSuccessUnix: ts, OK: true,
				ActivationStatus: "repairing", ActivationCreatedUnix: time.Now().Add(-2 * time.Minute).Unix(), ActivationGeneration: 7},
			{Slug: "beta-kpi", Name: "refresh-data", OK: false}, // never succeeded -> no sample
		}, nil
	}
	c := NewScheduleFreshnessCollector(query)
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)

	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	var fam *dto.MetricFamily
	for _, mf := range mfs {
		if mf.GetName() == "shinyhub_schedule_last_success_seconds" {
			fam = mf
		}
	}
	if fam == nil {
		t.Fatal("shinyhub_schedule_last_success_seconds not emitted")
	}
	if len(fam.Metric) != 1 {
		t.Fatalf("got %d samples, want 1 (only the succeeded schedule)", len(fam.Metric))
	}
	m := fam.Metric[0]
	if got := m.GetGauge().GetValue(); got != float64(ts) {
		t.Fatalf("value = %v, want %v", got, float64(ts))
	}
	labels := map[string]string{}
	for _, lp := range m.Label {
		labels[lp.GetName()] = lp.GetValue()
	}
	if labels["slug"] != "alpha-dash" || labels["schedule"] != "refresh-data" {
		t.Fatalf("labels = %v, want slug=alpha-dash schedule=refresh-data", labels)
	}

	families := map[string]*dto.MetricFamily{}
	for _, mf := range mfs {
		families[mf.GetName()] = mf
	}
	status := families["shinyhub_schedule_activation_status"]
	if status == nil || len(status.Metric) != 1 || status.Metric[0].GetGauge().GetValue() != 1 {
		t.Fatalf("activation status family = %+v, want one value-1 sample", status)
	}
	statusLabels := map[string]string{}
	for _, lp := range status.Metric[0].Label {
		statusLabels[lp.GetName()] = lp.GetValue()
	}
	if statusLabels["status"] != "repairing" {
		t.Fatalf("activation status labels=%v, want repairing", statusLabels)
	}
	generation := families["shinyhub_schedule_activation_target_generation"]
	if generation == nil || generation.Metric[0].GetGauge().GetValue() != 7 {
		t.Fatalf("activation generation family=%+v, want 7", generation)
	}
	age := families["shinyhub_schedule_activation_age_seconds"]
	if age == nil || age.Metric[0].GetGauge().GetValue() < 119 {
		t.Fatalf("activation age family=%+v, want approximately 120 seconds", age)
	}
}

func TestCapacityDeferralMetricsSurviveCoalescedSuccess(t *testing.T) {
	now := time.Now().Unix()
	status := "pending" // newer producer success replaced a capacity-deferred row
	reg := prometheus.NewRegistry()
	reg.MustRegister(NewScheduleFreshnessCollector(func() ([]ScheduleSample, error) {
		return []ScheduleSample{{Slug: "app", Name: "refresh", ActivationStatus: status,
			ActivationCreatedUnix: now, CapacityDeferredUnix: now - 3600, CapacityDeferrals: 4}}, nil
	}))
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]float64{}
	for _, f := range families {
		values[f.GetName()] = f.Metric[0].GetGauge().GetValue()
	}
	if values["shinyhub_schedule_activation_capacity_deferrals"] != 4 || values["shinyhub_schedule_activation_capacity_deferred_age_seconds"] < 3600 || values["shinyhub_schedule_activation_age_seconds"] > 5 {
		t.Fatalf("coalescing reset capacity age: %v", values)
	}
	status = "succeeded"
	families, err = reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if f.GetName() == "shinyhub_schedule_activation_capacity_deferred_age_seconds" {
			t.Fatal("completed work still reports a growing capacity age")
		}
	}
}
