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
