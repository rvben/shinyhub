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
	case deployfail.MentionsMissingExecutable(msg, "Rscript"):
		return "deploy failed: R runtime not found on the server (Rscript is not in PATH). " +
			"Install R, switch the app to a container runtime, or contact your administrator."
	case deployfail.MentionsMissingExecutable(msg, "uv"),
		deployfail.MentionsMissingExecutable(msg, "python3"),
		deployfail.MentionsMissingExecutable(msg, "python"):
		return "deploy failed: Python runtime not found on the server (uv/python3 is not in PATH). " +
			"Install it, switch the app to a container runtime, or contact your administrator."
	case strings.Contains(msg, "health check"):
		return "deploy failed: the app did not pass its health check - it likely crashed on startup. " +
			"Check the app logs for the cause."
	default:
		return "deploy failed: " + msg
	}
}
