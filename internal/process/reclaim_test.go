package process

import "testing"

func TestReclaimFreed(t *testing.T) {
	cases := []struct {
		name string
		pre  uint64
		post uint64
		frac float64
		want bool
	}{
		{"full reclaim", 1000, 50, 0.8, true},
		{"exactly at threshold", 1000, 200, 0.8, true},  // freed = 0.80
		{"just below threshold", 1000, 201, 0.8, false}, // freed = 0.799
		{"partial 75pct below 80", 4_000_000_000, 1_000_000_000, 0.8, false},
		{"zero pre-RSS", 0, 0, 0.8, false},
		{"grew during freeze", 1000, 1200, 0.8, false},
		{"no change", 1000, 1000, 0.8, false},
		{"lenient threshold", 1000, 400, 0.5, true}, // freed = 0.60
	}
	for _, c := range cases {
		if got := reclaimFreed(c.pre, c.post, c.frac); got != c.want {
			t.Errorf("%s: reclaimFreed(%d,%d,%.2f) = %v, want %v", c.name, c.pre, c.post, c.frac, got, c.want)
		}
	}
}
