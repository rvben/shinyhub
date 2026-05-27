package metrics

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// TestRecordDeploy_CountsByResult proves deploys are counted, split by outcome,
// so an operator can alert on a rising failure rate.
func TestRecordDeploy_CountsByResult(t *testing.T) {
	reg := New("test")
	reg.RecordDeploy("success")
	reg.RecordDeploy("success")
	reg.RecordDeploy("failure")

	if got := testutil.ToFloat64(reg.deploys.WithLabelValues("success")); got != 2 {
		t.Errorf("deploys_total{result=success} = %v, want 2", got)
	}
	if got := testutil.ToFloat64(reg.deploys.WithLabelValues("failure")); got != 1 {
		t.Errorf("deploys_total{result=failure} = %v, want 1", got)
	}
	if rr := scrape(t, reg); !strings.Contains(rr, `shinyhub_deploys_total{result="failure"}`) {
		t.Errorf("scrape missing deploys series:\n%s", rr)
	}
}

// TestRecordStateTransition_CountsByEvent proves app lifecycle transitions
// (hibernate/wake/restart) are counted by event type.
func TestRecordStateTransition_CountsByEvent(t *testing.T) {
	reg := New("test")
	reg.RecordStateTransition("hibernate")
	reg.RecordStateTransition("wake")
	reg.RecordStateTransition("wake")

	if got := testutil.ToFloat64(reg.stateTransitions.WithLabelValues("hibernate")); got != 1 {
		t.Errorf("state_transitions_total{event=hibernate} = %v, want 1", got)
	}
	if got := testutil.ToFloat64(reg.stateTransitions.WithLabelValues("wake")); got != 2 {
		t.Errorf("state_transitions_total{event=wake} = %v, want 2", got)
	}
}

// TestRecordReplicaRestart_Increments proves replica crash-restarts are counted,
// so a flapping app shows up as a rising restart rate.
func TestRecordReplicaRestart_Increments(t *testing.T) {
	reg := New("test")
	reg.RecordReplicaRestart()
	reg.RecordReplicaRestart()

	if got := testutil.ToFloat64(reg.replicaRestarts); got != 2 {
		t.Errorf("replica_restarts_total = %v, want 2", got)
	}
	if rr := scrape(t, reg); !strings.Contains(rr, "shinyhub_replica_restarts_total") {
		t.Errorf("scrape missing replica restarts series:\n%s", rr)
	}
}

// TestRegisterFleetGauges_ReflectsCallbacks proves the fleet gauges report
// whatever the wired callbacks return at scrape time, so "how many apps/replicas
// are running right now" is answerable from Prometheus alone.
func TestRegisterFleetGauges_ReflectsCallbacks(t *testing.T) {
	reg := New("test")
	apps, replicas := 3.0, 7.0
	reg.RegisterFleetGauges(
		func() float64 { return apps },
		func() float64 { return replicas },
	)

	if v, ok := sampleValue(t, reg, "shinyhub_apps_running", nil); !ok || v != 3 {
		t.Fatalf("shinyhub_apps_running = %v (ok=%v), want 3", v, ok)
	}
	if v, ok := sampleValue(t, reg, "shinyhub_replicas_running", nil); !ok || v != 7 {
		t.Fatalf("shinyhub_replicas_running = %v (ok=%v), want 7", v, ok)
	}

	// Gauges are evaluated lazily at scrape time, so a later change is reflected.
	apps = 5
	if v, _ := sampleValue(t, reg, "shinyhub_apps_running", nil); v != 5 {
		t.Fatalf("shinyhub_apps_running after change = %v, want 5", v)
	}
}
