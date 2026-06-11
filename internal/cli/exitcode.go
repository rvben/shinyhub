package cli

import (
	"errors"
	"fmt"
)

// ExitCodeError wraps an error with a specific process exit code. main()
// inspects the returned error chain for this type via errors.As and exits
// with Code; without it, cobra's default non-nil-error exit (1) applies.
//
// Codes: 0 success/report, 1 usage/manifest/validation, 2
// plan-detailed-exitcode changes-pending, 3 transport/auth, 4 partial
// convergence, 5 conflicts, 6 server not ready. schedule --follow commands
// propagate the remote job's own exit code (exit_code_passthrough).
type ExitCodeError struct {
	Code int
	Err  error
	// Kind is the structured error kind for the envelope. Empty means
	// classify() derives it from Code (legacy fleet paths) or falls back.
	Kind Kind
	// Reported is set when the command already wrote a contextual,
	// user-facing message for this error. The renderer then skips the prose
	// duplication; the structured envelope is still emitted.
	Reported bool
}

func (e *ExitCodeError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("exit code %d", e.Code)
	}
	return e.Err.Error()
}

func (e *ExitCodeError) Unwrap() error { return e.Err }

// exitCode returns the process exit code for err: 0 for nil, the Code of the
// first ExitCodeError in the chain, else 1.
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ece *ExitCodeError
	if errors.As(err, &ece) {
		return ece.Code
	}
	return 1
}

// ExitCode is the exported entry point for main(): it returns the process
// exit code implied by err (see exitCode).
func ExitCode(err error) int { return exitCode(err) }
