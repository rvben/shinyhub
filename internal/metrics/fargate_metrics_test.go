package metrics

import (
	"testing"
)

func TestFargateMetrics_RunTaskCounter(t *testing.T) {
	reg := New("test")
	reg.RecordRunTask("ok")
	reg.RecordRunTask("ok")
	reg.RecordRunTask("error")

	okVal, ok := sampleValue(t, reg, "shinyhub_fargate_run_task_total", map[string]string{"result": "ok"})
	if !ok || okVal != 2 {
		t.Errorf("fargate_run_task_total{result=ok} = %v (ok=%v), want 2", okVal, ok)
	}
	errVal, ok := sampleValue(t, reg, "shinyhub_fargate_run_task_total", map[string]string{"result": "error"})
	if !ok || errVal != 1 {
		t.Errorf("fargate_run_task_total{result=error} = %v (ok=%v), want 1", errVal, ok)
	}
}

func TestFargateMetrics_WaitIPTimeoutCounter(t *testing.T) {
	reg := New("test")
	reg.RecordWaitIPTimeout()
	reg.RecordWaitIPTimeout()
	val, ok := sampleValue(t, reg, "shinyhub_fargate_wait_ip_timeout_total", nil)
	if !ok || val != 2 {
		t.Errorf("fargate_wait_ip_timeout_total = %v (ok=%v), want 2", val, ok)
	}
}

func TestFargateMetrics_StopTaskCounter(t *testing.T) {
	reg := New("test")
	reg.RecordStopTask("ok")
	reg.RecordStopTask("error")
	okVal, ok := sampleValue(t, reg, "shinyhub_fargate_stop_task_total", map[string]string{"result": "ok"})
	if !ok || okVal != 1 {
		t.Errorf("fargate_stop_task_total{result=ok} = %v (ok=%v), want 1", okVal, ok)
	}
	errVal, ok := sampleValue(t, reg, "shinyhub_fargate_stop_task_total", map[string]string{"result": "error"})
	if !ok || errVal != 1 {
		t.Errorf("fargate_stop_task_total{result=error} = %v (ok=%v), want 1", errVal, ok)
	}
}

func TestFargateMetrics_InventoryErrorCounter(t *testing.T) {
	reg := New("test")
	reg.RecordInventoryError()
	val, ok := sampleValue(t, reg, "shinyhub_fargate_inventory_errors_total", nil)
	if !ok || val != 1 {
		t.Errorf("fargate_inventory_errors_total = %v (ok=%v), want 1", val, ok)
	}
}

func TestFargateMetrics_RunTaskLatencyHistogram(t *testing.T) {
	reg := New("test")
	reg.ObserveRunTaskLatency(0.5)
	reg.ObserveRunTaskLatency(1.0)
	reg.ObserveRunTaskLatency(2.5)
	// sampleValue returns GetSampleCount for histograms; expect 3 observations.
	count, ok := sampleValue(t, reg, "shinyhub_fargate_run_task_duration_seconds", nil)
	if !ok || count != 3 {
		t.Errorf("fargate_run_task_duration_seconds sample count = %v (ok=%v), want 3", count, ok)
	}
}
