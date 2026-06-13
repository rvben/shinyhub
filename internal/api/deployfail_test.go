package api

import (
	"errors"
	"strings"
	"testing"
)

func TestDeployFailureMessage(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		contains []string
	}{
		{
			name:     "r runtime missing",
			err:      errors.New(`all replicas failed health check: replica 0: start: start process: start process: exec: "Rscript": executable file not found in $PATH`),
			contains: []string{"R runtime", "Rscript"},
		},
		{
			name:     "python runtime missing",
			err:      errors.New(`all replicas failed health check: replica 0: start: start process: exec: "uv": executable file not found in $PATH`),
			contains: []string{"Python runtime", "uv"},
		},
		{
			name:     "health check failure without runtime hint",
			err:      errors.New("all replicas failed health check: replica 0: timed out after 30s"),
			contains: []string{"health check"},
		},
		{
			name:     "unknown error surfaces the cause",
			err:      errors.New("bundle missing app entrypoint"),
			contains: []string{"deploy failed", "bundle missing app entrypoint"},
		},
		{
			name:     "nil error is defensive",
			err:      nil,
			contains: []string{"deploy failed"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := deployFailureMessage(tc.err)
			for _, want := range tc.contains {
				if !strings.Contains(got, want) {
					t.Errorf("deployFailureMessage(%v) = %q; want it to contain %q", tc.err, got, want)
				}
			}
		})
	}
}
