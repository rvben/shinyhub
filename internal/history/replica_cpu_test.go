package history

import (
	"testing"
	"time"
)

func TestReplicaCPUSaturationUsesCollectorSamples(t *testing.T) {
	s := NewStore(time.Hour, 15*time.Second)
	high, low := 95.0, 40.0
	for _, ts := range []int64{100, 115, 130} {
		s.RecordReplicaCPU("app", 0, "run-a", ts, &high)
	}
	if !s.ReplicaCPUSaturated("app", 0, "run-a", 130, 100) {
		t.Fatal("three high fixed-cadence samples should saturate")
	}
	if s.ReplicaCPUSaturated("app", 0, "run-b", 130, 100) {
		t.Fatal("a new run must not inherit the old run's streak")
	}
	if s.ReplicaCPUSaturated("app", 0, "run-a", 200, 100) {
		t.Fatal("stale samples must not claim current saturation")
	}
	gapped := NewStore(time.Hour, 15*time.Second)
	for _, ts := range []int64{100, 115, 145} {
		gapped.RecordReplicaCPU("app", 0, "run-a", ts, &high)
	}
	if gapped.ReplicaCPUSaturated("app", 0, "run-a", 145, 100) {
		t.Fatal("missing a collector tick must break the sustained window")
	}
	s.RecordReplicaCPU("app", 0, "run-a", 145, &low)
	if s.ReplicaCPUSaturated("app", 0, "run-a", 145, 100) {
		t.Fatal("a low sample must clear saturation")
	}
	s.RecordReplicaCPU("app", 0, "run-a", 160, nil)
	s.RecordReplicaCPU("app", 0, "run-a", 175, &high)
	if s.ReplicaCPUSaturated("app", 0, "run-a", 175, 100) {
		t.Fatal("a missing rate must break the streak")
	}
	s.GC(220)
	if len(s.replicaCPU) != 0 {
		t.Fatal("inactive replica windows must be reclaimed")
	}
}
