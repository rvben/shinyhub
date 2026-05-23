package cli

import (
	"encoding/json"
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
