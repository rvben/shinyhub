package admission

import (
	"math"
	"testing"
)

func TestSuggestSessionCap(t *testing.T) {
	// 2 cores, 0.75 headroom, 1.3s render: Rate = 2*0.75/1.3 = 1.153/s, *2s cadence
	// = 2.3, floored to 2.
	if cap, cadence := SuggestSessionCap(2, 0.75, 1.3); cap != 2 || cadence != 2.0 {
		t.Fatalf("SuggestSessionCap(2,0.75,1.3) = (%d,%v), want (2,2)", cap, cadence)
	}
	// A tiny render cost still floors at >= 1 only when the product < 1; here it is
	// large, so it is many. A huge render cost drives the product below 1: floor to 1.
	if cap, _ := SuggestSessionCap(2, 0.75, 100); cap != 1 {
		t.Fatalf("SuggestSessionCap(2,0.75,100) cap = %d, want 1 (floored)", cap)
	}
	// Pacing off yields no suggestion.
	if cap, _ := SuggestSessionCap(2, 0.75, 0); cap != 0 {
		t.Fatalf("SuggestSessionCap with renderSeconds 0 cap = %d, want 0", cap)
	}
	// The cadence is a stable, reported constant.
	if _, cadence := SuggestSessionCap(8, 0.75, 2); math.Abs(cadence-2.0) > 1e-9 {
		t.Fatalf("cadence = %v, want 2.0", cadence)
	}
}
