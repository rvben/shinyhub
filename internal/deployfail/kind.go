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
	RuntimeMissing   Kind = "runtime_missing"   // uv/python3/Rscript not in PATH
	BuildFailed      Kind = "build_failed"      // uv sync / renv restore failed
	BundleInvalid    Kind = "bundle_invalid"    // server rejected bundle content
	ReadinessTimeout Kind = "readiness_timeout" // started, never became healthy in time
	Crashed          Kind = "crashed"           // process exited before healthy
	ServerError      Kind = "server_error"      // a 5xx the server could not classify

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
	case RuntimeMissing, BuildFailed, BundleInvalid, ReadinessTimeout,
		Crashed, ServerError, ZipError, TransportError, Unknown:
		return true
	}
	return false
}
