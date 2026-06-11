package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
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
		// url.Error wrapping a timeout implements net.Error; the timeout branch
		// must fire before the url.Error-network branch for real http.Client shapes.
		{"url error wrapping timeout", &url.Error{Op: "Get", URL: "http://x", Err: fakeTimeoutErr{}}, KindTimeout, 3},
		{"legacy exit 3", &ExitCodeError{Code: 3, Err: errors.New("not logged in")}, KindAuth, 3},
		{"legacy exit 4", &ExitCodeError{Code: 4, Err: errors.New("partial")}, KindPartialConvergence, 4},
		{"legacy exit 5", &ExitCodeError{Code: 5, Err: errors.New("conflict")}, KindConflict, 5},
		{"legacy exit 6", &ExitCodeError{Code: 6, Err: errors.New("not ready")}, KindServerNotReady, 6},
		// The typed HTTP status is more specific than the aggregate legacy code;
		// the typed-status branch fires first.
		{"wrapped http status outranks legacy code", &ExitCodeError{Code: 4, Err: &httpStatusError{Status: 401, msg: "x"}}, KindAuth, 3},
		{"conflict error type", &conflictError{slug: "app", msg: "re-run plan"}, KindConflict, 5},
		{"deploy http 401", &deployHTTPError{statusCode: 401, body: "unauthorized"}, KindAuth, 3},
		{"deploy http 503 wrapped", fmt.Errorf("checking demo: %w", &deployHTTPError{statusCode: 503, body: "service unavailable"}), KindServerError, 3},
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

func TestReport_EnvelopeIsLastStderrLine(t *testing.T) {
	var stderr bytes.Buffer
	code := reportTo(&stderr, false /* stderrIsTTY */, formatJSON,
		&httpStatusError{Status: 401, msg: "session expired - run `shinyhub login` to sign in again"})
	if code != 3 {
		t.Fatalf("exit code = %d, want 3", code)
	}
	lines := strings.Split(strings.TrimRight(stderr.String(), "\n"), "\n")
	last := lines[len(lines)-1]
	var env struct {
		Error struct {
			Kind     string `json:"kind"`
			Message  string `json:"message"`
			Hint     string `json:"hint"`
			ExitCode int    `json:"exit_code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(last), &env); err != nil {
		t.Fatalf("last stderr line is not a JSON envelope: %q: %v", last, err)
	}
	if env.Error.Kind != "auth" || env.Error.ExitCode != 3 {
		t.Errorf("envelope = %+v", env.Error)
	}
	// JSON mode: the envelope is the ONLY error output.
	if len(lines) != 1 {
		t.Errorf("json mode should emit exactly one stderr line, got %d: %q", len(lines), stderr.String())
	}
}

func TestReport_TableTTYProseThenEnvelope(t *testing.T) {
	var stderr bytes.Buffer
	code := reportTo(&stderr, true /* stderrIsTTY */, formatTable, errors.New("plain failure"))
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	lines := strings.Split(strings.TrimRight(stderr.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("want prose line + envelope line, got %d lines: %q", len(lines), stderr.String())
	}
	if !strings.Contains(lines[0], "plain failure") {
		t.Errorf("prose line = %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], `{"error":`) {
		t.Errorf("last line is not the envelope: %q", lines[1])
	}
}

func TestReport_ReportedSkipsProseKeepsEnvelope(t *testing.T) {
	var stderr bytes.Buffer
	reportTo(&stderr, true, formatTable, &ExitCodeError{Code: 4, Err: errors.New("2 apps failed"), Reported: true})
	lines := strings.Split(strings.TrimRight(stderr.String(), "\n"), "\n")
	if len(lines) != 1 || !strings.HasPrefix(lines[0], `{"error":`) {
		t.Errorf("Reported error should emit envelope only, got %q", stderr.String())
	}
}

func TestReport_DetailedExitCodeEmitsNothing(t *testing.T) {
	var stderr bytes.Buffer
	code := reportTo(&stderr, false, formatJSON, &ExitCodeError{Code: 2, Err: errors.New("changes pending"), Reported: false})
	if code != 2 || stderr.Len() != 0 {
		t.Errorf("exit 2 should be silent: code=%d stderr=%q", code, stderr.String())
	}
}

func TestReport_NilIsZero(t *testing.T) {
	var stderr bytes.Buffer
	if code := reportTo(&stderr, false, formatJSON, nil); code != 0 || stderr.Len() != 0 {
		t.Errorf("nil error: code=%d stderr=%q", code, stderr.String())
	}
}

// TestReport_ValidationErrHintInEnvelopeAndProse verifies that validationErr
// errors carry the hint in the envelope's dedicated hint field (not baked into
// the message) and that the TTY prose line shows both message and hint.
func TestReport_ValidationErrHintInEnvelopeAndProse(t *testing.T) {
	err := validationErr("unknown output format \"yaml\"", "valid formats: table, json, ndjson")

	// JSON mode: hint appears in the envelope field, not in the message.
	var jsonStderr bytes.Buffer
	code := reportTo(&jsonStderr, false, formatJSON, err)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	var env struct {
		Error struct {
			Kind    string `json:"kind"`
			Message string `json:"message"`
			Hint    string `json:"hint"`
		} `json:"error"`
	}
	if jerr := json.Unmarshal([]byte(strings.TrimRight(jsonStderr.String(), "\n")), &env); jerr != nil {
		t.Fatalf("envelope not valid JSON: %v\nraw: %q", jerr, jsonStderr.String())
	}
	if env.Error.Kind != "validation" {
		t.Errorf("kind = %q, want validation", env.Error.Kind)
	}
	if env.Error.Message == "" || strings.Contains(env.Error.Message, "valid formats") {
		t.Errorf("message must not contain hint text: %q", env.Error.Message)
	}
	if env.Error.Hint != "valid formats: table, json, ndjson" {
		t.Errorf("hint = %q, want the hint string", env.Error.Hint)
	}

	// TTY table mode: prose line shows message AND hint for humans.
	var ttyStderr bytes.Buffer
	reportTo(&ttyStderr, true, formatTable, err)
	lines := strings.Split(strings.TrimRight(ttyStderr.String(), "\n"), "\n")
	if len(lines) < 1 || !strings.Contains(lines[0], "valid formats") {
		t.Errorf("TTY prose line must contain hint, got %q", ttyStderr.String())
	}
}
