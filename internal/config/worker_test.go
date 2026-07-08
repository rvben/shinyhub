package config

import (
	"strings"
	"testing"
)

func containsFold(s, sub string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(sub))
}

func TestValidateWorkerSettings(t *testing.T) {
	tests := []struct {
		name      string
		w         WorkerSettings
		clustered bool
		memMB     int
		budgetMB  int
		wantErr   string // substring; "" = no error
	}{
		{"multiplex default ok", WorkerSettings{Isolation: IsolationMultiplex}, false, 0, 0, ""},
		{"multiplex clustered ok", WorkerSettings{Isolation: IsolationMultiplex}, true, 0, 0, ""},
		{"grouped needs size", WorkerSettings{Isolation: IsolationGrouped, MaxWorkers: 5}, false, 0, 0, "grouped_size"},
		{"negative lifetime rejected", WorkerSettings{Isolation: IsolationPerSession, MaxWorkers: 1, MaxSessionLifetime: -1}, false, 0, 0, "max_session_lifetime"},
		{"grouped ok", WorkerSettings{Isolation: IsolationGrouped, GroupedSize: 8, MaxWorkers: 5}, false, 0, 0, ""},
		{"per_session needs max_workers", WorkerSettings{Isolation: IsolationPerSession}, false, 0, 0, "max_workers"},
		{"per_session ok", WorkerSettings{Isolation: IsolationPerSession, MaxWorkers: 20}, false, 0, 0, ""},
		{"unknown mode", WorkerSettings{Isolation: "bogus"}, false, 0, 0, "isolation"},
		{"non-multiplex clustered rejected", WorkerSettings{Isolation: IsolationPerSession, MaxWorkers: 5}, true, 0, 0, "single-node"},
		{"host budget exceeded", WorkerSettings{Isolation: IsolationPerSession, MaxWorkers: 100}, false, 512, 8192, "host"},
		{"host budget ok", WorkerSettings{Isolation: IsolationPerSession, MaxWorkers: 4}, false, 512, 8192, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateWorkerSettings(tc.w, tc.clustered, tc.memMB, tc.budgetMB)
			if tc.wantErr == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantErr != "" && (err == nil || !containsFold(err.Error(), tc.wantErr)) {
				t.Fatalf("error = %v, want substring %q", err, tc.wantErr)
			}
		})
	}
}

func TestWorkerBudgetWarning(t *testing.T) {
	grouped := WorkerSettings{Isolation: IsolationGrouped, GroupedSize: 4, MaxWorkers: 10}
	perSession := WorkerSettings{Isolation: IsolationPerSession, MaxWorkers: 40}

	cases := []struct {
		name           string
		w              WorkerSettings
		effMemMB       int
		hostBudgetMB   int
		minAvailableMB int
		wantWarning    bool
	}{
		{"multiplex never warns", WorkerSettings{Isolation: IsolationMultiplex}, 0, 0, 0, false},
		{"empty isolation never warns", WorkerSettings{}, 0, 0, 0, false},
		{"grouped with no guard warns", grouped, 0, 0, 0, true},
		{"per_session with no guard warns", perSession, 0, 0, 0, true},
		{"static guard armed silences", grouped, 512, 8192, 0, false},
		{"runtime floor silences", grouped, 0, 0, 1024, false},
		{"budget without memory limit is inert and warns", grouped, 0, 8192, 0, true},
		{"memory limit without budget is inert and warns", grouped, 512, 0, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := WorkerBudgetWarning(tc.w, tc.effMemMB, tc.hostBudgetMB, tc.minAvailableMB)
			if tc.wantWarning && got == "" {
				t.Fatal("expected a warning, got none")
			}
			if !tc.wantWarning && got != "" {
				t.Fatalf("expected no warning, got %q", got)
			}
			if tc.wantWarning && !strings.Contains(got, "memory guard") {
				t.Errorf("warning should name the missing memory guard, got %q", got)
			}
		})
	}
}
