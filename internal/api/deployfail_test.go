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
		{
			// Observed verbatim against a running server: the hook invoked
			// `python`, which macOS does not provide, while uv and the build
			// were perfectly healthy. The old message told the operator to
			// install a Python runtime that was already installed.
			name:     "hook calling a missing binary blames the hook, not the server",
			err:      errors.New(`hook[0] (python -c open('HOOK_RAN.txt','w').write('yes')): exec: "python": executable file not found in $PATH`),
			contains: []string{"post-deploy hook", "shinyhub.toml", "was not started"},
		},
		{
			name:     "hook exit status blames the hook",
			err:      errors.New(`hook[1] (make assets): exit status 2`),
			contains: []string{"post-deploy hook", "make assets"},
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

// TestDeployFailureMessage_HookNotMisreportedAsRuntimeMissing pins the exact
// regression: the hook message must not claim the server is missing a runtime.
// Asserting on absence matters here because the old behaviour produced a
// confident, plausible, and entirely wrong instruction.
func TestDeployFailureMessage_HookNotMisreportedAsRuntimeMissing(t *testing.T) {
	err := errors.New(`hook[0] (python -c pass): exec: "python": executable file not found in $PATH`)
	got := deployFailureMessage(err)
	for _, forbidden := range []string{"Python runtime not found", "Install it", "contact your administrator"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("hook failure message must not blame the server runtime; got %q", got)
		}
	}
}
