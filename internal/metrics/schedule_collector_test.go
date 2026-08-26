package metrics

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

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
