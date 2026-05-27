package agent

import (
	"testing"
	"time"
)

func TestShouldRenew_PastHalfLife(t *testing.T) {
	notBefore := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	notAfter := notBefore.Add(time.Hour) // half-life at +30m

	cases := []struct {
		name string
		now  time.Time
		want bool
	}{
		{"fresh", notBefore.Add(time.Minute), false},
		{"just before half-life", notBefore.Add(29 * time.Minute), false},
		{"at half-life", notBefore.Add(30 * time.Minute), true},
		{"past half-life", notBefore.Add(45 * time.Minute), true},
		{"expired", notAfter.Add(time.Minute), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldRenew(notBefore, notAfter, tc.now); got != tc.want {
				t.Errorf("shouldRenew(now=%s) = %v, want %v", tc.now.Sub(notBefore), got, tc.want)
			}
		})
	}
}
