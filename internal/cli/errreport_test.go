package cli

import (
	"errors"
	"fmt"
	"net/url"
	"testing"
)

type fakeTimeoutErr struct{}

func (fakeTimeoutErr) Error() string   { return "deadline exceeded" }
func (fakeTimeoutErr) Timeout() bool   { return true }
func (fakeTimeoutErr) Temporary() bool { return true }

func TestClassify(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantKind Kind
		wantCode int
	}{
		{"explicit kind wins", &ExitCodeError{Code: 6, Kind: KindServerNotReady, Err: errors.New("x")}, KindServerNotReady, 6},
		{"http 400", &httpStatusError{Status: 400, msg: "bad"}, KindValidation, 1},
		{"http 401", &httpStatusError{Status: 401, msg: "no"}, KindAuth, 3},
		{"http 403", &httpStatusError{Status: 403, msg: "no"}, KindAuth, 3},
		{"http 404", &httpStatusError{Status: 404, msg: "gone"}, KindNotFound, 1},
		{"http 409", &httpStatusError{Status: 409, msg: "dup"}, KindConflict, 5},
		{"http 429", &httpStatusError{Status: 429, msg: "slow down"}, KindRateLimit, 3},
		{"http 500", &httpStatusError{Status: 500, msg: "boom"}, KindServerError, 3},
		{"wrapped http", fmt.Errorf("deploy: %w", &httpStatusError{Status: 503, msg: "u"}), KindServerError, 3},
		{"net timeout", fakeTimeoutErr{}, KindTimeout, 3},
		{"url error", &url.Error{Op: "Get", URL: "http://x", Err: errors.New("connection refused")}, KindNetwork, 3},
		{"legacy exit 4", &ExitCodeError{Code: 4, Err: errors.New("partial")}, KindPartialConvergence, 4},
		{"legacy exit 5", &ExitCodeError{Code: 5, Err: errors.New("conflict")}, KindConflict, 5},
		{"legacy exit 6", &ExitCodeError{Code: 6, Err: errors.New("not ready")}, KindServerNotReady, 6},
		{"plain error", errors.New("something odd"), KindInternal, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, code := classify(tc.err)
			if kind != tc.wantKind || code != tc.wantCode {
				t.Errorf("classify() = (%q, %d), want (%q, %d)", kind, code, tc.wantKind, tc.wantCode)
			}
		})
	}
}

// TestClassify_DetailedExitCodeIsNotAnError pins that fleet plan's exit 2
// (changes pending) is a report outcome: no kind, no envelope.
func TestClassify_DetailedExitCodeIsNotAnError(t *testing.T) {
	kind, code := classify(&ExitCodeError{Code: 2, Err: errors.New("changes pending")})
	if kind != "" || code != 2 {
		t.Errorf("classify(exit 2) = (%q, %d), want (\"\", 2)", kind, code)
	}
}
