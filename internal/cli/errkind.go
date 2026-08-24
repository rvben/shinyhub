package cli

// Kind is the stable machine-readable failure category carried in structured
// error envelopes and listed in the schema document's errors array. Consumers
// branch on Kind, not on message text.
type Kind string

const (
	KindValidation           Kind = "validation"
	KindNotFound             Kind = "not_found"
	KindConfirmationRequired Kind = "confirmation_required"
	KindAuth                 Kind = "auth"
	KindNetwork              Kind = "network"
	KindTimeout              Kind = "timeout"
	KindRateLimit            Kind = "rate_limit"
	KindServerError          Kind = "server_error"
	KindPartialConvergence   Kind = "partial_convergence"
	KindConflict             Kind = "conflict"
	KindServerNotReady       Kind = "server_not_ready"
	KindJobFailed            Kind = "job_failed"
	KindSchemaIncompatible   Kind = "schema_incompatible"
	KindInternal             Kind = "internal"
)

// kindInfo describes one error kind for the schema document and the renderer.
// ExitCode 0 means passthrough: the process exit code is not fixed by the
// kind (schedule --follow propagates the remote job's own code) and the
// schema entry declares exit_code_passthrough instead of a fixed exit_code.
type kindInfo struct {
	Kind      Kind
	ExitCode  int
	Retryable bool
	Desc      string
}

// kindTable is the finite set of kinds this binary emits. The schema
// generator serializes it as the errors array; tests pin it to the spec.
var kindTable = []kindInfo{
	{KindValidation, 1, false, "Invalid flags, arguments, manifest, or request (HTTP 400)"},
	{KindNotFound, 1, false, "Resource does not exist (HTTP 404)"},
	{KindConfirmationRequired, 1, false, "Confirmation needed but stdin is not a TTY; hint names the bypass flag"},
	{KindInternal, 1, false, "Unexpected or unclassified error"},
	{KindAuth, 3, false, "Authentication or authorization failed (HTTP 401/403, expired session)"},
	{KindNetwork, 3, true, "Connection failed (refused, reset, DNS)"},
	{KindTimeout, 3, true, "Request or wait deadline exceeded"},
	{KindRateLimit, 3, true, "Too many requests (HTTP 429)"},
	{KindServerError, 3, true, "Server-side failure (HTTP 5xx)"},
	{KindPartialConvergence, 4, false, "fleet apply: one or more apps failed to converge"},
	{KindConflict, 5, false, "Resource exists with different configuration (HTTP 409)"},
	{KindServerNotReady, 6, true, "Host reachable but ShinyHub is not up"},
	{KindSchemaIncompatible, 7, false, "serve: the database was migrated by a newer build; permanent until the binary or database changes"},
	{KindJobFailed, 0, false, "schedule --follow: the remote job failed; exit code is the job's own"},
}
