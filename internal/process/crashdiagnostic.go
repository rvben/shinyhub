package process

// CrashDiagnosticTailLines bounds how many trailing app-log lines are folded
// into a crash diagnostic; CrashDiagnosticMaxBytes caps the total so a
// runaway log line cannot bloat the apps row it is stored in.
const (
	CrashDiagnosticTailLines = 20
	CrashDiagnosticMaxBytes  = 8000
)

// BuildCrashDiagnostic builds a short diagnostic from a boot/exit error (when
// present) and the tail of a replica's log, where a Python/R traceback lands.
// The end of the text - the actual error - is preserved when the combined
// text is truncated. Shared by the runtime watchdog (marking an app
// "crashed") and the deploy handler (marking an app "failed" when its
// replicas never came up), so both record the same shape of diagnostic into
// apps.last_error.
func BuildCrashDiagnostic(bootErr error, logTail string) string {
	var reason string
	switch {
	case bootErr != nil && logTail != "":
		reason = bootErr.Error() + "\n\n" + logTail
	case bootErr != nil:
		reason = bootErr.Error()
	default:
		reason = logTail
	}
	if len(reason) > CrashDiagnosticMaxBytes {
		reason = "...\n" + reason[len(reason)-CrashDiagnosticMaxBytes:]
	}
	return reason
}
