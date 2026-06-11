package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// unwrapServerError extracts a human-readable message from a failed HTTP
// response body. The server wraps errors in a {"error":"..."} envelope; this
// returns that message so the CLI prints it instead of raw JSON. If the body is
// not the standard envelope it returns the trimmed body, and if the body is
// empty it returns the fallback.
func unwrapServerError(body []byte, fallback string) string {
	var env struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err == nil && env.Error != "" {
		return env.Error
	}
	if trimmed := strings.TrimSpace(string(body)); trimmed != "" {
		return trimmed
	}
	return fallback
}

// httpStatusError is the required error type for every non-2xx HTTP response
// in the CLI. It carries the status code so the root classifier maps it to a
// stable error kind without parsing message strings. msg is the fully
// formatted human message.
type httpStatusError struct {
	Status int
	msg    string
}

func (e *httpStatusError) Error() string { return e.msg }

// loginFailedError types the login-failure path; 401 here means bad
// credentials, not an expired session.
func loginFailedError(resp *http.Response) error {
	return &httpStatusError{Status: resp.StatusCode, msg: fmt.Sprintf("login failed: %s", resp.Status)}
}

// httpError builds the error returned for a failed (>= 400) HTTP response.
// op names the action being attempted (e.g. "list apps", "rollback") so the
// non-session message keeps its context.
//
// A JWT minted by `shinyhub login` lives for one hour; once it lapses every
// command 401s with the server's bare "unauthorized", which gives no hint that
// the credential simply expired. When the stored credential is a JWT and the
// status is 401, this returns a message that points the developer at
// `shinyhub login` to re-authenticate. API keys and opaque deploy tokens do
// not expire, so their 401s are reported verbatim (a revoked or wrong key is a
// different problem than a lapsed session). All other failures are reported as
// the operation, the server's status, and its error envelope.
func httpError(token, op string, resp *http.Response, body []byte) error {
	if resp.StatusCode == http.StatusUnauthorized && looksLikeJWT(token) {
		return &httpStatusError{
			Status: resp.StatusCode,
			msg:    "session expired - run `shinyhub login` to sign in again",
		}
	}
	return &httpStatusError{
		Status: resp.StatusCode,
		msg:    fmt.Sprintf("%s (%s): %s", op, resp.Status, unwrapServerError(body, "no error body")),
	}
}
