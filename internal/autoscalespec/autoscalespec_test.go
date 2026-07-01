package autoscalespec

import (
	"math"
	"strings"
	"testing"
)

func boolPtr(b bool) *bool { return &b }

func TestValidate_Accepts(t *testing.T) {
	for _, p := range []Params{
		{Enabled: boolPtr(true), MinReplicas: 1, MaxReplicas: 8, Target: 0.8},
		{Enabled: boolPtr(true), MinReplicas: 1, MaxReplicas: 1000, Target: 1},
		{Enabled: boolPtr(true), MinReplicas: 2, MaxReplicas: 2, Target: 0}, // target 0 = inherit
		{Enabled: boolPtr(false)}, // disabled, bounds default 0
	} {
		if err := Validate(p); err != nil {
			t.Errorf("Validate(%+v) = %v, want nil", p, err)
		}
	}
}

func TestValidate_Rejects(t *testing.T) {
	cases := []struct {
		name string
		p    Params
		want string
	}{
		{"enabled missing", Params{MinReplicas: 1, MaxReplicas: 2}, "autoscale.enabled is required"},
		{"nan target", Params{Enabled: boolPtr(true), MinReplicas: 1, MaxReplicas: 2, Target: math.NaN()}, "finite"},
		{"inf target", Params{Enabled: boolPtr(true), MinReplicas: 1, MaxReplicas: 2, Target: math.Inf(1)}, "finite"},
		{"target too high", Params{Enabled: boolPtr(true), MinReplicas: 1, MaxReplicas: 2, Target: 1.5}, "autoscale.target must be in [0,1]"},
		{"min out of range", Params{Enabled: boolPtr(false), MinReplicas: 1001}, "autoscale.min_replicas must be between 0 and 1000"},
		{"max out of range", Params{Enabled: boolPtr(false), MaxReplicas: -1}, "autoscale.max_replicas must be between 0 and 1000"},
		{"enabled min < 1", Params{Enabled: boolPtr(true), MinReplicas: 0, MaxReplicas: 2}, "autoscale.min_replicas must be >= 1 when enabled"},
		{"enabled max < min", Params{Enabled: boolPtr(true), MinReplicas: 5, MaxReplicas: 2}, "autoscale.max_replicas must be >= min_replicas"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(tc.p)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Validate(%+v) = %v, want error containing %q", tc.p, err, tc.want)
			}
		})
	}
}
