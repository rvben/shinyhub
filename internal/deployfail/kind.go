// Package deployfail defines the stable vocabulary and classifier for why a
// deploy attempt failed. It is the single source of truth shared by the server
// (which emits the kind in the deploy 500 body) and the CLI (which reports it
// per attempt). It depends only on the standard library.
package deployfail

// Kind is the machine-readable reason a deploy attempt failed. The string
// values are a public contract surfaced in the fleet apply JSON output and the
// CLI schema; do not rename them.
type Kind string

const (
	// Server-emitted kinds (computed by Classify from the deploy error).
	RuntimeMissing         Kind = "runtime_missing"         // uv/python3/Rscript not in PATH
	BuildFailed            Kind = "build_failed"            // uv sync / renv restore failed
	InterpreterUnavailable Kind = "interpreter_unavailable" // uv could not obtain a Python matching requires-python
	HookFailed             Kind = "hook_failed"             // a manifest post-deploy hook failed
	BundleInvalid          Kind = "bundle_invalid"          // server rejected bundle content
	ReadinessTimeout       Kind = "readiness_timeout"       // started, never became healthy in time
	Crashed                Kind = "crashed"                 // process exited before healthy
	ServerError            Kind = "server_error"            // a 5xx the server could not classify

	// DowntimeRequired is the server's refusal to deploy without downtime when
	// parallel generation handoff does not support the app's current shape
	// (non-multiplex isolation, a draining generation, a config-bearing
	// manifest). The working version is preserved and nothing changed under the
	// caller, so this is a precondition the operator clears with
	// --allow-downtime, never a state race to re-plan.
	DowntimeRequired Kind = "downtime_required"

	// Client-emitted kinds (set by the CLI for its own error shapes).
	ZipError       Kind = "zip_error"       // CLI failed to package the local dir
	TransportError Kind = "transport_error" // the request never reached the server
	Unknown        Kind = "unknown"         // not classifiable
)

// Valid reports whether k is one of the known kinds. Used by tests and by the
// CLI to decide whether a server-supplied failure_kind is trustworthy; it is
// not a runtime gate (unrecognised kinds are treated as opaque text elsewhere).
func (k Kind) Valid() bool {
	switch k {
	case RuntimeMissing, BuildFailed, InterpreterUnavailable, HookFailed, BundleInvalid,
		ReadinessTimeout, Crashed, ServerError, DowntimeRequired, ZipError, TransportError, Unknown:
		return true
	}
	return false
}
