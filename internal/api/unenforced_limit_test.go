package api

import (
	"strings"
	"testing"
)

// TestUnenforcedLimitWarning covers the decision the warning encodes: an
// operator is told a limit is inert only when they actually asked for one and
// the runtime cannot apply it. The silent rows are the ones that matter - each
// is a case where an unconditional warning would cry wolf at a limit that is
// either enforced or not a limit at all.
func TestUnenforcedLimitWarning(t *testing.T) {
	tests := []struct {
		name        string
		setMem      bool
		effMemMB    int
		memEnforced bool
		setCPU      bool
		effCPUPct   int
		cpuEnforced bool
		wantSubject string // "" means: no warning at all
	}{
		{
			name:   "no resource key in the request",
			setMem: false, effMemMB: 512, memEnforced: false,
			setCPU: false, effCPUPct: 100, cpuEnforced: false,
			wantSubject: "",
		},
		{
			name:   "memory limit set and enforced",
			setMem: true, effMemMB: 512, memEnforced: true,
			wantSubject: "",
		},
		{
			name:   "memory limit set but not enforced",
			setMem: true, effMemMB: 512, memEnforced: false,
			wantSubject: "memory limit",
		},
		{
			name:   "memory set to unlimited on an unenforcing host",
			setMem: true, effMemMB: 0, memEnforced: false,
			wantSubject: "",
		},
		{
			name:   "memory cleared to a global default that is itself unlimited",
			setMem: true, effMemMB: 0, memEnforced: false,
			wantSubject: "",
		},
		{
			name:   "memory cleared to a global default that is a real limit",
			setMem: true, effMemMB: 256, memEnforced: false,
			wantSubject: "memory limit",
		},
		{
			name:   "cpu quota set but not enforced",
			setCPU: true, effCPUPct: 50, cpuEnforced: false,
			wantSubject: "CPU limit",
		},
		{
			name:   "cpu quota set to unlimited on an unenforcing host",
			setCPU: true, effCPUPct: 0, cpuEnforced: false,
			wantSubject: "",
		},
		{
			name:   "cpu quota set and enforced",
			setCPU: true, effCPUPct: 50, cpuEnforced: true,
			wantSubject: "",
		},
		{
			name:   "both set, both unenforced",
			setMem: true, effMemMB: 512, memEnforced: false,
			setCPU: true, effCPUPct: 50, cpuEnforced: false,
			wantSubject: "memory and CPU limits",
		},
		{
			// The mixed case is what separates a per-controller check from a
			// single "native mode" flag: cgroup v2 delegates memory and cpu
			// independently, so memory can bind while cpu does not.
			name:   "both set, only cpu unenforced",
			setMem: true, effMemMB: 512, memEnforced: true,
			setCPU: true, effCPUPct: 50, cpuEnforced: false,
			wantSubject: "CPU limit",
		},
		{
			name:   "both set, only memory unenforced",
			setMem: true, effMemMB: 512, memEnforced: false,
			setCPU: true, effCPUPct: 50, cpuEnforced: true,
			wantSubject: "memory limit",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := unenforcedLimitWarning(tc.setMem, tc.effMemMB, tc.memEnforced, tc.setCPU, tc.effCPUPct, tc.cpuEnforced)
			if tc.wantSubject == "" {
				if got != "" {
					t.Fatalf("expected no warning, got %q", got)
				}
				return
			}
			if got == "" {
				t.Fatalf("expected a warning naming %q, got none", tc.wantSubject)
			}
			// The verb has to agree with the subject: this string is read by
			// an operator in a terminal, and "the memory and CPU limits is
			// recorded" reads as a bug in the thing reporting the problem.
			verb := "is"
			if strings.HasSuffix(tc.wantSubject, "limits") {
				verb = "are"
			}
			if !strings.Contains(got, "the "+tc.wantSubject+" "+verb+" recorded but not enforced") {
				t.Errorf("warning should name %q as the unenforced subject, got %q", tc.wantSubject, got)
			}
			// The warning is only useful if it says what to do about it.
			if !strings.Contains(got, "Delegate") || !strings.Contains(got, "Docker runtime") {
				t.Errorf("warning should name both remedies (delegation, Docker runtime), got %q", got)
			}
		})
	}
}
