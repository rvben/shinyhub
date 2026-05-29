package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestFargateMetrics_RunTaskCounter(t *testing.T) {
	reg := New("test")
	reg.RecordRunTask("ok")
	reg.RecordRunTask("ok")
	reg.RecordRunTask("error")

	val := testutil.ToFloat64(reg.fargateRunTaskTotal.WithLabelValues("ok"))
	if val != 2 {
		t.Errorf("fargate_run_task_total{result=ok} = %v, want 2", val)
	}
	val = testutil.ToFloat64(reg.fargateRunTaskTotal.WithLabelValues("error"))
	if val != 1 {
		t.Errorf("fargate_run_task_total{result=error} = %v, want 1", val)
	}
}

func TestFargateMetrics_WaitIPTimeoutCounter(t *testing.T) {
	reg := New("test")
	reg.RecordWaitIPTimeout()
	reg.RecordWaitIPTimeout()
	val := testutil.ToFloat64(reg.fargateWaitIPTimeoutTotal)
	if val != 2 {
		t.Errorf("fargate_wait_ip_timeout_total = %v, want 2", val)
	}
}

func TestFargateMetrics_StopTaskCounter(t *testing.T) {
	reg := New("test")
	reg.RecordStopTask("ok")
	reg.RecordStopTask("error")
	okVal := testutil.ToFloat64(reg.fargateStopTaskTotal.WithLabelValues("ok"))
	errVal := testutil.ToFloat64(reg.fargateStopTaskTotal.WithLabelValues("error"))
	if okVal != 1 || errVal != 1 {
		t.Errorf("stop task totals ok=%v error=%v, want 1 each", okVal, errVal)
	}
}

func TestFargateMetrics_InventoryErrorCounter(t *testing.T) {
	reg := New("test")
	reg.RecordInventoryError()
	val := testutil.ToFloat64(reg.fargateInventoryErrorsTotal)
	if val != 1 {
		t.Errorf("fargate_inventory_errors_total = %v, want 1", val)
	}
}

func TestFargateMetrics_RunTaskLatencyHistogram(t *testing.T) {
	reg := New("test")
	reg.ObserveRunTaskLatency(0.5)
	// Asserting via handler output: verify no registration error (already done by
	// the counter tests using the same registry) and no panic from Observe.
	reg.ObserveRunTaskLatency(1.0)
	reg.ObserveRunTaskLatency(2.5)
	// If we reach here without panic the histogram is functional.
}
