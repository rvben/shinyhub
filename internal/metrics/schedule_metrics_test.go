package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestRecordScheduleRun_IncrementsCounter(t *testing.T) {
	r := New("test")
	r.RecordScheduleRun("jp-dash", "refresh-data", "succeeded")
	r.RecordScheduleRun("jp-dash", "refresh-data", "succeeded")
	r.RecordScheduleRun("jp-dash", "refresh-data", "failed")

	if got := testutil.ToFloat64(r.runs.WithLabelValues("jp-dash", "refresh-data", "succeeded")); got != 2 {
		t.Fatalf("succeeded count = %v, want 2", got)
	}
	if got := testutil.ToFloat64(r.runs.WithLabelValues("jp-dash", "refresh-data", "failed")); got != 1 {
		t.Fatalf("failed count = %v, want 1", got)
	}
}
