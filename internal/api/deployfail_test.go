package api

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/deployfail"
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

// TestWriteErrorWithKind_AlwaysCarriesValidKind pins the invariant the hook
// classifier depends on. Server-side, failure_kind is computed from the RAW
// deploy error, while the human "error" text is deployFailureMessage's wrapped
// form ("deploy failed: a post-deploy hook ... - hook[0] (...)"). The CLI trusts
// failure_kind first and only falls back to classifying the message text - and
// that wrapped text no longer starts with the anchored "hook[" marker, so the
// fallback would misclassify it, potentially back to runtime_missing when the
// hook itself hit a missing binary. That is the exact bug hook_failed removes.
// The fallback is unreachable only while every deploy-failure response carries a
// valid kind; this fails if that ever stops being true.
func TestWriteErrorWithKind_AlwaysCarriesValidKind(t *testing.T) {
	hookErr := errors.New(`hook[0] (Rscript setup.R): exec: "Rscript": executable file not found in $PATH`)

	rec := httptest.NewRecorder()
	writeErrorWithKind(rec, 500, deployFailureMessage(hookErr), deployfail.Classify(hookErr))

	var body struct {
		Error       string `json:"error"`
		FailureKind string `json:"failure_kind"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if k := deployfail.Kind(body.FailureKind); !k.Valid() {
		t.Fatalf("failure_kind %q is not valid; the CLI would fall back to text classification", body.FailureKind)
	}
	if body.FailureKind != string(deployfail.HookFailed) {
		t.Errorf("failure_kind = %q, want hook_failed", body.FailureKind)
	}
	// Demonstrates why the kind field is load-bearing: the human text alone
	// classifies wrongly, and specifically back to the pre-fix answer.
	if got := deployfail.ClassifyMessage(body.Error); got == deployfail.HookFailed {
		t.Fatal("test is vacuous: the wrapped message classifies correctly on its own")
	} else if got != deployfail.RuntimeMissing {
		t.Logf("wrapped message classifies as %q (not hook_failed), which is why failure_kind must be sent", got)
	}
}
