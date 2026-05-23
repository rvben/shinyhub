package cli

import (
	"errors"
	"fmt"
)

// ExitCodeError wraps an error with a specific process exit code. main()
// inspects the returned error chain for this type via errors.As and exits
// with Code; without it, cobra's default non-nil-error exit (1) applies.
//
// Codes follow the fleet spec: 0 success/report, 1 usage/manifest, 2
// plan-detailed-exitcode changes-pending, 3 transport/auth, 4 partial
// convergence, 5 conflicts.
type ExitCodeError struct {
	Code int
	Err  error
	// Reported is set when the command already wrote a contextual, user-facing
	// message for this error (e.g. a "✗ ..." preflight box or a full apply
	// report). The fleet RunE wrapper then stays silent so the message is not
	// duplicated; cobra's generic "Error:" line is suppressed independently via
	// SilenceErrors on the fleet subcommand tree.
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
