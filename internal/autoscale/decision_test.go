package autoscale

import "testing"

func TestDesiredReplicas(t *testing.T) {
	// Base case shape: cap 10, target 0.8 => 8 target sessions per replica.
	base := scaleInput{cap: 10, target: 0.8, min: 1, max: 8, runtimeMax: 32}

	cases := []struct {
		name      string
		in        scaleInput
		active    int64
		current   int
		saturated bool
		want      int
	}{
		{"steady stays put", base, 17, 3, false, 3},    // ceil(17/8)=3
		{"high load scales up", base, 30, 3, false, 4}, // ceil(30/8)=4
		{"low load scales down", base, 9, 3, false, 2}, // ceil(9/8)=2
		{"idle floors to min", base, 0, 3, false, 1},   // ceil(0)=0 -> min 1
		{"clamp to per-app max", base, 200, 4, false, 8},
		{"saturation forces +1 over math", base, 17, 3, true, 4}, // math says 3, saturated -> 4
		{"saturation no-op when already scaling up", base, 30, 3, true, 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := tc.in
			in.activeSessions = tc.active
			in.current = tc.current
			in.saturated = tc.saturated
			if got, _ := desiredReplicas(in); got != tc.want {
				t.Fatalf("desiredReplicas(active=%d current=%d sat=%v) = %d, want %d",
					tc.active, tc.current, tc.saturated, got, tc.want)
			}
		})
	}
}

func TestDesiredReplicas_RespectsRuntimeCeiling(t *testing.T) {
	in := scaleInput{
		activeSessions: 1000, current: 4, cap: 10, target: 0.8,
		min: 1, max: 50, runtimeMax: 6,
	}
	if got, _ := desiredReplicas(in); got != 6 {
		t.Fatalf("desiredReplicas with runtimeMax=6 = %d, want 6", got)
	}
}

func TestDesiredReplicas_MinFloorWins(t *testing.T) {
	in := scaleInput{
		activeSessions: 5, current: 10, cap: 10, target: 0.8,
		min: 3, max: 8, runtimeMax: 32,
	}
	// ceil(5/8)=1, but per-app min is 3.
	if got, _ := desiredReplicas(in); got != 3 {
		t.Fatalf("desiredReplicas with min=3 = %d, want 3", got)
	}
}

func TestDesiredReplicas_NoDecisionWithoutCapOrTarget(t *testing.T) {
	// Without an effective cap or target the saturation signal is undefined, so
	// the controller must hold the current count rather than scale blindly.
	noCap := scaleInput{activeSessions: 100, current: 4, cap: 0, target: 0.8, min: 1, max: 8, runtimeMax: 32}
	if got, _ := desiredReplicas(noCap); got != 4 {
		t.Fatalf("desiredReplicas with cap=0 = %d, want current 4", got)
	}
	noTarget := scaleInput{activeSessions: 100, current: 4, cap: 10, target: 0, min: 1, max: 8, runtimeMax: 32}
	if got, _ := desiredReplicas(noTarget); got != 4 {
		t.Fatalf("desiredReplicas with target=0 = %d, want current 4", got)
	}
}

func TestDesiredReplicas_ReasonPoolSaturated(t *testing.T) {
	// saturated && desired <= current triggers the pool_saturated branch.
	in := scaleInput{
		activeSessions: 16, current: 2, cap: 10, target: 0.8,
		min: 1, max: 8, runtimeMax: 32,
		saturated: true,
	}
	// ceil(16/8)=2 == current; saturation forces +1 -> desired=3.
	desired, reason := desiredReplicas(in)
	if desired != 3 {
		t.Fatalf("desired = %d, want 3", desired)
	}
	if reason != "pool_saturated" {
		t.Fatalf("reason = %q, want pool_saturated", reason)
	}
}

func TestDesiredReplicas_ReasonSessionLoad(t *testing.T) {
	// plain scale-up from load, no saturation.
	in := scaleInput{
		activeSessions: 30, current: 2, cap: 10, target: 0.8,
		min: 1, max: 8, runtimeMax: 32,
		saturated: false,
	}
	// ceil(30/8)=4 > 2; no saturation branch.
	desired, reason := desiredReplicas(in)
	if desired != 4 {
		t.Fatalf("desired = %d, want 4", desired)
	}
	if reason != "session_load" {
		t.Fatalf("reason = %q, want session_load", reason)
	}
}
