package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestRecordWSSessionEnd(t *testing.T) {
	r := New("test")
	r.RecordWSSessionEnd("demo", "unknown", "upstream", true)
	r.RecordWSSessionEnd("demo", "unknown", "upstream", true)
	if got := testutil.ToFloat64(r.wsSessionEnds.WithLabelValues("demo", "unknown", "upstream", "true")); got != 2 {
		t.Fatalf("upstream abnormal closes = %v, want 2", got)
	}
}
