package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// gaugeBySlug extracts a gauge family's values keyed by the "slug" label.
func gaugeBySlug(t *testing.T, mfs []*dto.MetricFamily, name string) map[string]float64 {
	t.Helper()
	out := map[string]float64{}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.Metric {
			slug := ""
			for _, lp := range m.Label {
				if lp.GetName() == "slug" {
					slug = lp.GetValue()
				}
			}
			out[slug] = m.GetGauge().GetValue()
		}
	}
	return out
}

// TestSessionGaugesCollector verifies every app emits shinyhub_app_sessions,
// and shinyhub_app_sessions_limit is emitted iff the pool is CAPPED (Cap>0) -
// including a value of 0 when every replica is draining, so a capped pool with
// no admission capacity still exposes a saturation signal. Uncapped pools emit
// no limit series (no meaningful ceiling).
func TestSessionGaugesCollector(t *testing.T) {
	sample := func() []SessionSample {
		return []SessionSample{
			{Slug: "busy", Sessions: 8, Cap: 10, Replicas: 1},     // limit = 10
			{Slug: "free", Sessions: 3, Cap: 0, Replicas: 1},      // uncapped -> no limit
			{Slug: "draining", Sessions: 5, Cap: 10, Replicas: 0}, // capped, fully draining -> limit = 0
		}
	}
	reg := prometheus.NewRegistry()
	reg.MustRegister(NewSessionGaugesCollector(sample))

	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}

	sessions := gaugeBySlug(t, mfs, "shinyhub_app_sessions")
	if sessions["busy"] != 8 || sessions["free"] != 3 || sessions["draining"] != 5 {
		t.Errorf("shinyhub_app_sessions = %v, want busy=8 free=3 draining=5", sessions)
	}

	limits := gaugeBySlug(t, mfs, "shinyhub_app_sessions_limit")
	if limits["busy"] != 10 {
		t.Errorf("busy limit = %v, want 10", limits["busy"])
	}
	if v, ok := limits["draining"]; !ok || v != 0 {
		t.Errorf("capped fully-draining 'draining' must emit limit=0, got %v (present=%v)", v, ok)
	}
	if _, ok := limits["free"]; ok {
		t.Errorf("uncapped 'free' must have no limit series, got %v", limits["free"])
	}
}
