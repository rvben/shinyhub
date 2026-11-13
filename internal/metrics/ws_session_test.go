package metrics

import (
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestRecordWSSessionEnd(t *testing.T) {
	r := New("test")
	r.RecordWSSessionEnd("demo", "unknown", "upstream", true, 6*time.Hour)
	r.RecordWSSessionEnd("demo", "unknown", "upstream", true, 6*time.Hour)
	if got := testutil.ToFloat64(r.wsSessionEnds.WithLabelValues("demo", "unknown", "upstream", "true")); got != 2 {
		t.Fatalf("upstream abnormal closes = %v, want 2", got)
	}
	if err := testutil.CollectAndCompare(r.wsSessionDuration, strings.NewReader(`
# HELP shinyhub_ws_session_duration_seconds Completed WebSocket connection lifetime by app; not request latency or active work time.
# TYPE shinyhub_ws_session_duration_seconds histogram
shinyhub_ws_session_duration_seconds_bucket{slug="demo",le="1"} 0
shinyhub_ws_session_duration_seconds_bucket{slug="demo",le="10"} 0
shinyhub_ws_session_duration_seconds_bucket{slug="demo",le="60"} 0
shinyhub_ws_session_duration_seconds_bucket{slug="demo",le="300"} 0
shinyhub_ws_session_duration_seconds_bucket{slug="demo",le="900"} 0
shinyhub_ws_session_duration_seconds_bucket{slug="demo",le="3600"} 0
shinyhub_ws_session_duration_seconds_bucket{slug="demo",le="7200"} 0
shinyhub_ws_session_duration_seconds_bucket{slug="demo",le="14400"} 0
shinyhub_ws_session_duration_seconds_bucket{slug="demo",le="28800"} 2
shinyhub_ws_session_duration_seconds_bucket{slug="demo",le="86400"} 2
shinyhub_ws_session_duration_seconds_bucket{slug="demo",le="+Inf"} 2
shinyhub_ws_session_duration_seconds_sum{slug="demo"} 43200
shinyhub_ws_session_duration_seconds_count{slug="demo"} 2
`)); err != nil {
		t.Fatal(err)
	}
}

func TestRecordWSAbnormalBurst(t *testing.T) {
	r := New("test")
	r.RecordWSAbnormalBurst("demo")
	if got := testutil.ToFloat64(r.wsAbnormalBursts.WithLabelValues("demo")); got != 1 {
		t.Fatalf("abnormal bursts = %v, want 1", got)
	}
}
