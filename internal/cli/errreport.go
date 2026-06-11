package cli

import (
	"errors"
	"net"
	"net/url"
)

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
	var hse *httpStatusError
	if errors.As(err, &hse) {
		switch {
		case hse.Status == 400:
			return KindValidation, 1
		case hse.Status == 401 || hse.Status == 403:
			return KindAuth, 3
		case hse.Status == 404:
			return KindNotFound, 1
		case hse.Status == 409:
			return KindConflict, 5
		case hse.Status == 429:
			return KindRateLimit, 3
		case hse.Status >= 500:
			return KindServerError, 3
		default:
			return KindInternal, 1
		}
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
	return KindInternal, 1
}
