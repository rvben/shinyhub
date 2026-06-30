package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestScheduleFreshnessCollector(t *testing.T) {
	const ts = int64(1751284800) // fixed unix timestamp
	query := func() ([]ScheduleSample, error) {
		return []ScheduleSample{
			{Slug: "jp-dash", Name: "refresh-data", LastSuccessUnix: ts, OK: true},
			{Slug: "ccro-kpi", Name: "refresh-data", OK: false}, // never succeeded -> no sample
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
	if labels["slug"] != "jp-dash" || labels["schedule"] != "refresh-data" {
		t.Fatalf("labels = %v, want slug=jp-dash schedule=refresh-data", labels)
	}
}
