package cli

import (
	"encoding/json"

	"github.com/rvben/shinyhub/internal/deployfail"
)

// withDeployFailureHint attaches a hint to a request that failed on the
// server (status >= 500) with a failure kind the server stated and a retry
// may not clear: a deploy, a draft preview, a rollback or a restart. Such a
// failure still classifies as server_error with exit code 3, the code a script
// may read as "worth retrying", so the hint names the kind and whether a retry
// can clear it. The error keeps its kind, exit code and message: the hinted
// wrapper unwraps to err, which classify still finds.
func withDeployFailureHint(err error, status int, kind deployfail.Kind) error {
	if err == nil || status < 500 {
		return err
	}
	hint := deployFailureHint(kind)
	if hint == "" {
		return err
	}
	return &hintedMsgError{msg: err.Error(), hint: hint, cause: err}
}

// deployFailureHint returns the hint for a deploy failure kind, or "" for a
// kind that needs none. A kind is coarser than its cause, so a hint states
// only what holds for every cause the server files under it, and names no
// remedy that some cause would not need. Every other kind gets no hint: a
// retry can clear it (crashed, readiness_timeout, a hook or managed-Python
// download that timed out), or its causes range from a fault in the bundle to
// a transient server read error (bundle_invalid).
func deployFailureHint(kind deployfail.Kind) string {
	switch kind {
	case deployfail.BuildFailed:
		// A build timeout and an unreachable package index are build failures
		// too. What clears a conflict varies (the requirements, the indexes,
		// the Python version the build targets), so the hint names none.
		return "build_failed: the server could not install the bundle's dependencies, for the reason the message gives. A timeout or an unreachable package index can clear on a retry; a dependency conflict usually does not"
	case deployfail.RuntimeMissing:
		// The server files any "executable file not found" for the runtime
		// under this kind, including one a build tool reports for its own
		// subprocess, and its message does not always name the executable,
		// so the hint names neither.
		return "runtime_missing: an executable the app needs was not found where it runs; a retry fails the same way until it is available there"
	}
	return ""
}

// statedDeployFailureKind returns the failure_kind a non-streamed deploy
// failure body states, or "" when it states none the CLI knows. It does not
// guess a kind from the message text, which would hint at a cause the server
// did not report.
func statedDeployFailureKind(body []byte) deployfail.Kind {
	var env struct {
		FailureKind string `json:"failure_kind"`
	}
	if json.Unmarshal(body, &env) != nil {
		return ""
	}
	if kind := deployfail.Kind(env.FailureKind); kind.Valid() {
		return kind
	}
	return ""
}
