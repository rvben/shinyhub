package api

import (
	"strings"

	"github.com/rvben/shinyhub/internal/deployfail"
)

// deployFailureMessage turns a raw deploy error into an actionable, developer-
// facing message for the HTTP 500 body. The server otherwise collapses a rich
// cause (e.g. `exec: "Rscript": executable file not found in $PATH`) into a bare
// "deploy failed", leaving a developer unable to tell a missing runtime from a
// broken bundle. The error chain is matched by substring because the health-
// check aggregation joins replica errors as text, so wrap fidelity (errors.As)
// is not guaranteed; the underlying exec message is always present.
//
// The structured sibling deployfail.Classify(err) computes the machine-readable
// failure_kind from the same error; both share deployfail.MentionsMissingExecutable
// so the substring knowledge lives in one place.
func deployFailureMessage(err error) string {
	if err == nil {
		return "deploy failed"
	}
	msg := err.Error()
	switch {
	case deployfail.Classify(err) == deployfail.HookFailed:
		// Checked first, and by kind rather than substring: a hook echoes the
		// app's own command into the message, so a hook calling a binary the
		// host lacks is indistinguishable from a missing server runtime by text
		// alone. Blaming the server would send the operator to install a runtime
		// that is present and working.
		return "deploy failed: a post-deploy hook from shinyhub.toml failed - " + msg +
			". Fix the hook command or the bundle it runs from; the app was not started."
	case deployfail.MentionsMissingExecutable(msg, "Rscript"):
		return "deploy failed: R runtime not found on the server (Rscript is not in PATH). " +
			"Install R, switch the app to a container runtime, or contact your administrator."
	case deployfail.MentionsMissingExecutable(msg, "uv"),
		deployfail.MentionsMissingExecutable(msg, "python3"),
		deployfail.MentionsMissingExecutable(msg, "python"):
		return "deploy failed: Python runtime not found on the server (uv/python3 is not in PATH). " +
			"Install it, switch the app to a container runtime, or contact your administrator."
	case deployfail.Classify(err) == deployfail.InterpreterUnavailable:
		// uv is installed but could not obtain a Python interpreter matching the
		// app's requires-python: the managed-CPython download is blocked (no
		// egress to GitHub's python-build-standalone releases) or no system
		// interpreter matches. This is a host-provisioning fix, not a bundle fix,
		// so the message names the server-level build.python* knobs rather than
		// the raw uv output.
		return "deploy failed: could not obtain a Python interpreter for this app. " +
			"If this host cannot reach GitHub's python-build-standalone releases, set build.python_preference: only-system " +
			"in the server config to use a preinstalled interpreter, or build.python_install_mirror to an internal mirror."
	case deployfail.Classify(err) == deployfail.Crashed:
		return "deploy failed: the app exited during startup, before it accepted a connection. " +
			"Check the app logs for the error it printed on the way out."
	case deployfail.Classify(err) == deployfail.ReadinessTimeout:
		// Not a crash. The process was still running when the readiness window
		// closed, so telling the operator it crashed sends them to look for an
		// error the log does not contain, and the log they do find says the app
		// is listening. The overwhelmingly common cause is an entrypoint that
		// picks its own address, because the launcher's own host and port are
		// then simply ignored.
		return "deploy failed: the app kept running but never answered on the address ShinyHub assigned it, " +
			"so it did not crash and the app log will not show an error. " +
			"Check whether the entrypoint starts the app itself with a host or port of its own " +
			"(a runApp() call left in app.R, or app.run() in app.py): the log line reporting where it is " +
			"listening names the address it chose instead. End the entrypoint with the app object and let " +
			"ShinyHub start it. If the address is already correct, the app needs longer to start than its " +
			"readiness window allows; raise `[app] startup_timeout_seconds` in shinyhub.toml."
	case strings.Contains(msg, "health check"):
		// A health-check failure that is neither of the two above (a replica
		// that could not be started at all, say). Report what is known instead
		// of guessing at a cause.
		return "deploy failed: the app did not pass its health check. " + msg +
			". Check the app logs for the cause."
	default:
		return "deploy failed: " + msg
	}
}
