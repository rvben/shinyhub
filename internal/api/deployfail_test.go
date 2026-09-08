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
		{
			// uv is present and working, but its managed-CPython download is
			// blocked. The message must point at the build.python* knobs, not
			// tell the operator to install a runtime that is already installed.
			name:     "blocked managed interpreter download names the config knobs",
			err:      errors.New(`uv sync: error: Failed to download https://github.com/astral-sh/python-build-standalone/releases/download/x.tar.gz (403 Forbidden)`),
			contains: []string{"could not obtain a Python interpreter", "build.python_preference", "build.python_install_mirror"},
		},
		{
			name:     "no matching interpreter names the config knobs",
			err:      errors.New(`uv sync: error: No interpreter found for Python >=3.12 in managed installations or search path`),
			contains: []string{"could not obtain a Python interpreter", "build.python_preference"},
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

// readinessTimeoutErr is the error a replica that stayed alive but never
// answered produces: waitHealthyContract returns the "did not become healthy
// within" form, and bootReplicas joins it under "all replicas failed health
// check". internal/deployfail's TestClassify pins that this text classifies as
// ReadinessTimeout, which is what these cases depend on.
func readinessTimeoutErr() error {
	return errors.New("all replicas failed health check: replica 0: health: " +
		"app at http://127.0.0.1:20211/ did not become healthy within 20s (last readiness status 0)")
}

// A readiness timeout is not a crash. Reproduced against a running server with
// an app.R ending in runApp(host=..., port=9922): the process bound its own
// port, stayed alive for the whole readiness window, and the deploy reported
// "it likely crashed on startup" with the app's own "Listening on
// http://127.0.0.1:9922" printed directly underneath it. The message sent the
// developer to look in the log for a crash that had not happened.
func TestDeployFailureMessage_ReadinessTimeoutIsNotReportedAsACrash(t *testing.T) {
	got := deployFailureMessage(readinessTimeoutErr())

	// Absence is the regression itself: the old message was confident and
	// wrong, and a fix that merely appended a hint would still leave it there.
	for _, forbidden := range []string{"crashed on startup", "likely crashed"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("readiness timeout must not be reported as a crash; got %q", got)
		}
	}
	// Second bound: saying what did not happen is worthless on its own. The
	// message has to name the cause the developer can act on, or it has only
	// traded a wrong answer for no answer.
	for _, want := range []string{"never answered", "host or port of its own", "startup_timeout_seconds"} {
		if !strings.Contains(got, want) {
			t.Errorf("readiness timeout message = %q; want it to contain %q", got, want)
		}
	}
}

// The crash message must keep saying a crash happened. Splitting the two apart
// is only an improvement if each half still lands: a fix that reported every
// health-check failure as a timeout would pass the test above and be just as
// misleading in the other direction.
func TestDeployFailureMessage_CrashIsStillReportedAsACrash(t *testing.T) {
	err := errors.New("all replicas failed health check: replica 0: health: " +
		"app at http://127.0.0.1:20211/ crashed on startup before becoming healthy")
	got := deployFailureMessage(err)

	if !strings.Contains(got, "exited during startup") {
		t.Errorf("crash message = %q; want it to say the app exited during startup", got)
	}
	if strings.Contains(got, "never answered") {
		t.Errorf("a real crash must not be reported as a readiness timeout; got %q", got)
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
