package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strings"
)

// statusKind maps an HTTP status code to its stable error kind and process
// exit code. Shared by the *httpStatusError and *deployHTTPError arms of
// classify so both typed-HTTP error types use identical mapping logic.
func statusKind(status int) (Kind, int) {
	switch {
	case status == 400:
		return KindValidation, 1
	case status == 401 || status == 403:
		return KindAuth, 3
	case status == 404:
		return KindNotFound, 1
	case status == 409:
		return KindConflict, 5
	case status == 429:
		return KindRateLimit, 3
	case status >= 500:
		return KindServerError, 3
	default:
		return KindInternal, 1
	}
}

// classify maps any error returned by a command to its stable kind and
// process exit code. Order matters: explicit kinds win, then typed HTTP
// status, then network shapes, then legacy exit codes, then the internal
// fallback. An empty kind (exit 2, detailed-exitcode) means "report
// outcome, no envelope".
func classify(err error) (Kind, int) {
	var ece *ExitCodeError
	hasECE := errors.As(err, &ece)
	if hasECE && ece.Kind != "" {
		return ece.Kind, exitCode(err)
	}
	var ce *conflictError
	if errors.As(err, &ce) {
		return KindConflict, 5
	}
	var hse *httpStatusError
	if errors.As(err, &hse) {
		return statusKind(hse.Status)
	}
	var dhe *deployHTTPError
	if errors.As(err, &dhe) {
		return statusKind(dhe.statusCode)
	}
	var pe *protocolError
	if errors.As(err, &pe) {
		// An undecodable response body (usually CLI/server version skew) is
		// neither an auth nor a transport failure: retrying or re-logging-in
		// cannot fix it, so it must not ride the retryable exit-3 kinds.
		return KindInternal, 1
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return KindTimeout, 3
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		return KindNetwork, 3
	}
	if hasECE {
		switch ece.Code {
		case 2:
			return "", 2
		case 3:
			// Typed transport errors (httpStatusError, url.Error, net.Error) are
			// classified earlier in the chain, so an untyped error carrying legacy
			// code 3 is a credential or auth-state failure (missing login, bad
			// config) rather than a network error.
			return KindAuth, 3
		case 4:
			return KindPartialConvergence, 4
		case 5:
			return KindConflict, 5
		case 6:
			return KindServerNotReady, 6
		}
	}
	// cobra generates plain errors (no typed wrapper) for argument/flag
	// validation failures. Classify them by message prefix rather than type
	// so callers receive kind=validation instead of the internal fallback.
	msg := err.Error()
	for _, prefix := range cobraErrorPrefixes {
		if strings.HasPrefix(msg, prefix) {
			return KindValidation, 1
		}
	}
	return KindInternal, 1
}

// cobraErrorPrefixes lists the fixed message prefixes cobra uses for
// argument and flag validation failures. These errors carry no typed wrapper,
// so classification falls back to prefix matching.
var cobraErrorPrefixes = []string{
	"required flag(s)",
	"unknown command",
	"unknown flag:",
	"unknown shorthand flag:",
	"invalid argument",
	// The trailing space is load-bearing: cobra's arg-count errors read
	// "accepts N arg(s)", and the space avoids matching unrelated words.
	"accepts ",
}

// errEnvelope is the structured failure record. Per clispec v0.2 it is
// written as a single line of JSON, as the last line of stderr, on every
// failure, in every output mode.
type errEnvelope struct {
	Error struct {
		Kind     string `json:"kind"`
		Message  string `json:"message"`
		Hint     string `json:"hint,omitempty"`
		ExitCode int    `json:"exit_code,omitempty"`
	} `json:"error"`
}

// hintedError is satisfied by errors that carry an actionable remedy string.
// Errors that carry no hint render without one.
type hintedError interface{ Hint() string }

// hintedMsgError pairs a message with an actionable hint; the renderer
// surfaces Hint() in the envelope's hint field.
type hintedMsgError struct {
	msg  string
	hint string
}

func (e *hintedMsgError) Error() string { return e.msg }
func (e *hintedMsgError) Hint() string  { return e.hint }

// confirmationRequiredError returns a KindConfirmationRequired error for
// interactive-only operations run without a TTY. bypassFlag names the flag
// that skips the prompt (e.g. "--yes"), surfaced in the envelope hint field.
func confirmationRequiredError(msg, bypassFlag string) error {
	return &ExitCodeError{Code: 1, Kind: KindConfirmationRequired,
		Err: &hintedMsgError{msg: msg, hint: "pass " + bypassFlag + " to proceed without a prompt"}}
}

// loginMissingCredsError returns a KindValidation error when a non-TTY login
// attempt is missing username or password. The hint names the flags that
// supply credentials non-interactively.
func loginMissingCredsError() error {
	return &ExitCodeError{Code: 1, Kind: KindValidation,
		Err: &hintedMsgError{
			msg:  "username and password required",
			hint: "pass --username and --password, or --token, for non-interactive login"}}
}

// reportTo renders err to w and returns the process exit code. Pure function
// of its inputs for testability; Report wires the real stderr/TTY/format.
func reportTo(w io.Writer, stderrIsTTY bool, format outputFormat, err error) int {
	if err == nil {
		return 0
	}
	kind, code := classify(err)
	if kind == "" {
		return code // report outcome (fleet plan --detailed-exitcode): no envelope
	}
	var ece *ExitCodeError
	reported := errors.As(err, &ece) && ece.Reported
	var he hintedError
	errors.As(err, &he)
	if stderrIsTTY && format == formatTable && !reported {
		if he != nil && he.Hint() != "" {
			fmt.Fprintf(w, "Error: %s (%s)\n", err.Error(), he.Hint())
		} else {
			fmt.Fprintf(w, "Error: %s\n", err.Error())
		}
	}
	var env errEnvelope
	env.Error.Kind = string(kind)
	env.Error.Message = err.Error()
	if he != nil {
		env.Error.Hint = he.Hint()
	}
	if kind != KindJobFailed {
		env.Error.ExitCode = code
	}
	line, marshalErr := json.Marshal(env)
	if marshalErr != nil {
		fmt.Fprintf(w, `{"error":{"kind":"internal","message":"failed to encode error envelope"}}`+"\n")
		return code
	}
	fmt.Fprintln(w, string(line))
	return code
}

// Report is the root error boundary: main() calls os.Exit(cli.Report(err)).
func Report(err error) int {
	return reportTo(os.Stderr, isTTY(os.Stderr), currentFormat(), err)
}
