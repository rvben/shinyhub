package cli

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
)

// newRunID returns a random 32-hex-char id correlating every per-app call in
// one fleet run. The server records it via the existing per-app audit rows
// (no new audit type); a run is reconstructed by grouping on this id.
func newRunID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// fleetUserAgent identifies fleet-originated requests in server logs/audit.
var fleetUserAgent = "shinyhub-fleet/" + version

// decorateFleetRequest stamps the run correlation id and a descriptive
// User-Agent on every fleet-originated request.
func decorateFleetRequest(req *http.Request, runID string) {
	req.Header.Set("X-Shinyhub-Run-Id", runID)
	req.Header.Set("User-Agent", fleetUserAgent)
}

// setPrecondition applies the If-Match-style headers the server enforces.
// ifDigest != nil sets X-Shinyhub-If-Content-Digest (server treats an empty
// value as "no assertion"). ifManagedBy != nil sets X-Shinyhub-If-Managed-By
// even when the pointed-to string is empty: header presence activates the
// server check and an empty value asserts the app is currently unmanaged.
func setPrecondition(req *http.Request, ifDigest *string, ifManagedBy *string) {
	if ifDigest != nil {
		req.Header.Set("X-Shinyhub-If-Content-Digest", *ifDigest)
	}
	if ifManagedBy != nil {
		req.Header.Set("X-Shinyhub-If-Managed-By", *ifManagedBy)
	}
}

// conflictError marks an app action aborted by a server precondition 409.
// apply records it, continues other apps, and never blind-retries it.
type conflictError struct {
	slug string
	msg  string
}

func (e *conflictError) Error() string {
	return fmt.Sprintf("conflict (state changed under us; re-run plan): %s", e.msg)
}

func isConflictError(err error) bool {
	var c *conflictError
	return errors.As(err, &c)
}

// isConflict reports whether an HTTP response is a precondition conflict.
func isConflict(resp *http.Response) bool { return resp.StatusCode == http.StatusConflict }
