package admission

import (
	"testing"
	"time"
)

func TestWatermarkDisabledAlwaysAdmits(t *testing.T) {
	w := NewWatermark(0, func() (float64, error) { return 100, nil })
	if !w.Admit() {
		t.Fatal("disabled watermark (maxPercent 0) must always admit")
	}
}

func TestWatermarkFailsOpenWithNoReading(t *testing.T) {
	// Enabled, but nothing recorded yet: must admit rather than treat missing as 0.
	w := NewWatermark(90, func() (float64, error) { return 0, nil })
	if !w.Admit() {
		t.Fatal("no reading must fail open (admit), not be read as 0% busy")
	}
}

func TestWatermarkShedsAboveThreshold(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	w := NewWatermark(90, func() (float64, error) { return 0, nil })
	w.nowFn = clk.now
	w.record(95, clk.now())
	if w.Admit() {
		t.Fatal("95% busy above 90% threshold must shed")
	}
	// Below threshold admits.
	w.record(80, clk.now())
	if !w.Admit() {
		t.Fatal("80% busy below 90% threshold must admit")
	}
}

func TestWatermarkFailsOpenOnStaleReading(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	w := NewWatermark(90, func() (float64, error) { return 0, nil })
	w.nowFn = clk.now
	w.record(99, clk.now()) // would shed if fresh
	// Advance past the staleness horizon: a stale high reading must fail open,
	// so a dead sampler cannot freeze admissions at an old value.
	clk.add(6 * time.Second)
	if !w.Admit() {
		t.Fatal("reading older than 5s must fail open (admit), not shed on stale data")
	}
}
