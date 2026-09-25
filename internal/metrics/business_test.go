package metrics

import (
	"strings"
	"testing"
	"time"

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

func TestGenerationHandoffMetricsExposeOutcomesAndDrainState(t *testing.T) {
	reg := New("test")
	reg.RecordGenerationHandoff("success")
	reg.RecordGenerationHandoff("forced_retirement")
	reg.BeginGenerationDrain()
	reg.UpdateGenerationDrainSessions(3)
	if got := testutil.ToFloat64(reg.generationDraining); got != 1 {
		t.Fatalf("generation_draining = %v, want 1", got)
	}
	if got := testutil.ToFloat64(reg.generationSessions); got != 3 {
		t.Fatalf("generation_draining_sessions = %v, want 3", got)
	}
	reg.EndGenerationDrain(3, 2*time.Second)
	if got := testutil.ToFloat64(reg.generationDraining); got != 0 {
		t.Fatalf("generation_draining after end = %v, want 0", got)
	}
	scraped := scrape(t, reg)
	for _, want := range []string{
		`shinyhub_generation_handoffs_total{outcome="success"} 1`,
		`shinyhub_generation_handoffs_total{outcome="forced_retirement"} 1`,
		`shinyhub_generation_draining_sessions 0`,
		`shinyhub_generation_drain_duration_seconds_count 1`,
	} {
		if !strings.Contains(scraped, want) {
			t.Errorf("scrape missing %q:\n%s", want, scraped)
		}
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
	apps, replicas, crashed := 3.0, 7.0, 2.0
	reg.RegisterFleetGauges(
		func() float64 { return apps },
		func() float64 { return replicas },
		func() float64 { return crashed },
	)

	if v, ok := sampleValue(t, reg, "shinyhub_apps_running", nil); !ok || v != 3 {
		t.Fatalf("shinyhub_apps_running = %v (ok=%v), want 3", v, ok)
	}
	if v, ok := sampleValue(t, reg, "shinyhub_replicas_running", nil); !ok || v != 7 {
		t.Fatalf("shinyhub_replicas_running = %v (ok=%v), want 7", v, ok)
	}
	if v, ok := sampleValue(t, reg, "shinyhub_apps_crashed", nil); !ok || v != 2 {
		t.Fatalf("shinyhub_apps_crashed = %v (ok=%v), want 2", v, ok)
	}

	// Gauges are evaluated lazily at scrape time, so a later change is reflected.
	apps = 5
	if v, _ := sampleValue(t, reg, "shinyhub_apps_running", nil); v != 5 {
		t.Fatalf("shinyhub_apps_running after change = %v, want 5", v)
	}
}

// TestRecordAuditWriteError surfaces dropped audit events as a counter so a
// persistent audit-write failure (e.g. disk full) can be alerted on.
func TestRecordAuditWriteError(t *testing.T) {
	reg := New("test")
	reg.RecordAuditWriteError()
	reg.RecordAuditWriteError()
	if v, ok := sampleValue(t, reg, "shinyhub_audit_write_errors_total", nil); !ok || v != 2 {
		t.Fatalf("shinyhub_audit_write_errors_total = %v (ok=%v), want 2", v, ok)
	}
}

// TestForgetApp_RemovesAdmissionRejectsAndRunsSeries proves that deleting an
// app removes every series it produced, on both metrics keyed solely by slug
// (admission_rejects) and metrics with additional labels (schedule_runs), so a
// deleted app's slug does not keep accumulating unbounded label cardinality
// forever. A sibling app's series must survive untouched.
func TestForgetApp_RemovesAdmissionRejectsAndRunsSeries(t *testing.T) {
	reg := New("test")
	reg.RecordReject("gone", "rate_limited")
	reg.RecordReject("keeper", "rate_limited")
	reg.RecordScheduleRun("gone", "nightly", "succeeded")
	reg.RecordScheduleRun("keeper", "nightly", "succeeded")

	if _, ok := sampleValue(t, reg, "shinyhub_admission_rejects_total", map[string]string{"slug": "gone"}); !ok {
		t.Fatal("precondition: admission_rejects series for slug=gone must exist before Forget")
	}
	if _, ok := sampleValue(t, reg, "shinyhub_schedule_runs_total", map[string]string{"slug": "gone"}); !ok {
		t.Fatal("precondition: schedule_runs series for slug=gone must exist before Forget")
	}

	reg.ForgetApp("gone")

	if _, ok := sampleValue(t, reg, "shinyhub_admission_rejects_total", map[string]string{"slug": "gone"}); ok {
		t.Error("admission_rejects series for slug=gone must be gone after ForgetApp")
	}
	if _, ok := sampleValue(t, reg, "shinyhub_schedule_runs_total", map[string]string{"slug": "gone"}); ok {
		t.Error("schedule_runs series for slug=gone must be gone after ForgetApp")
	}
	if v, ok := sampleValue(t, reg, "shinyhub_admission_rejects_total", map[string]string{"slug": "keeper"}); !ok || v != 1 {
		t.Errorf("sibling slug=keeper admission_rejects must survive, got %v (ok=%v)", v, ok)
	}
	if v, ok := sampleValue(t, reg, "shinyhub_schedule_runs_total", map[string]string{"slug": "keeper"}); !ok || v != 1 {
		t.Errorf("sibling slug=keeper schedule_runs must survive, got %v (ok=%v)", v, ok)
	}

	// A redeploy under the same slug must be able to start a fresh series
	// rather than resuming a stale, deleted one.
	reg.RecordReject("gone", "rate_limited")
	if v, ok := sampleValue(t, reg, "shinyhub_admission_rejects_total", map[string]string{"slug": "gone"}); !ok || v != 1 {
		t.Errorf("re-recording after ForgetApp must start a fresh series, got %v (ok=%v)", v, ok)
	}
}

// TestForgetSchedule_RemovesOnlyThatSchedule proves deleting one schedule of
// an app removes only that schedule's series, leaving other schedules on the
// same app (and the same schedule name on a different app) untouched.
func TestForgetSchedule_RemovesOnlyThatSchedule(t *testing.T) {
	reg := New("test")
	reg.RecordScheduleRun("app1", "nightly", "succeeded")
	reg.RecordScheduleRun("app1", "weekly", "succeeded")
	reg.RecordScheduleRun("app2", "nightly", "succeeded")

	reg.ForgetSchedule("app1", "nightly")

	if _, ok := sampleValue(t, reg, "shinyhub_schedule_runs_total", map[string]string{"slug": "app1", "schedule": "nightly"}); ok {
		t.Error("app1/nightly series must be gone after ForgetSchedule")
	}
	if v, ok := sampleValue(t, reg, "shinyhub_schedule_runs_total", map[string]string{"slug": "app1", "schedule": "weekly"}); !ok || v != 1 {
		t.Errorf("app1/weekly must survive ForgetSchedule(app1, nightly), got %v (ok=%v)", v, ok)
	}
	if v, ok := sampleValue(t, reg, "shinyhub_schedule_runs_total", map[string]string{"slug": "app2", "schedule": "nightly"}); !ok || v != 1 {
		t.Errorf("app2/nightly must survive ForgetSchedule(app1, nightly), got %v (ok=%v)", v, ok)
	}
}
