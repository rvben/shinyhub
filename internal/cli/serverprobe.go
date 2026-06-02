package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// serverPollInterval is how often --wait-for-server re-probes /api/server-info
// while waiting for a recycled host to come back up.
const serverPollInterval = 2 * time.Second

// serverInfo is the parsed GET /api/server-info response.
type serverInfo struct {
	Version      string     `json:"version"`
	Capabilities serverCaps `json:"capabilities"`
}

// looksLikeShinyhub reports whether the parsed server-info carries a
// recognizable shinyhub signature: a version string, or at least one advertised
// capability. A current server always sets version; a pre-version server still
// advertises capabilities. This keeps arbitrary 200-OK JSON from a front proxy
// from being mistaken for a healthy shinyhub.
func (i serverInfo) looksLikeShinyhub() bool {
	return i.Version != "" || i.Capabilities != (serverCaps{})
}

// serverNotReadyError marks a host that answered but is not (yet) a shinyhub
// server: a half-provisioned box where a front proxy is up but the shinyhub
// binary is not, so /api/server-info does not return the expected JSON. It is
// deliberately distinct from a transport/auth failure so the CLI can avoid the
// misleading "run shinyhub login" hint when the real problem is "the server
// isn't up yet".
type serverNotReadyError struct {
	host   string
	detail string
	// status is the HTTP status code from /api/server-info, or 0 when the
	// failure was not an HTTP response (a transport or body-parse error).
	status int
}

func (e *serverNotReadyError) Error() string {
	return fmt.Sprintf("server at %s is not ready: %s", e.host, e.detail)
}

// probeServer issues the unauthenticated GET /api/server-info and classifies
// the result:
//   - success: the parsed serverInfo, nil error.
//   - a reachable host that does not return shinyhub server-info JSON
//     (non-200, or 200 with a body that is not a server-info envelope):
//     a *serverNotReadyError.
//   - a genuine transport failure (DNS, refused connection, TLS): the raw
//     error, unwrapped, so the caller keeps treating it as transport/auth.
func probeServer(cfg *cliConfig) (serverInfo, error) {
	var info serverInfo
	req, err := http.NewRequest("GET", cfg.Host+"/api/server-info", nil)
	if err != nil {
		return info, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return info, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return info, &serverNotReadyError{
			host:   cfg.Host,
			detail: fmt.Sprintf("/api/server-info returned %s", resp.Status),
			status: resp.StatusCode,
		}
	}
	if err := json.Unmarshal(body, &info); err != nil || !info.looksLikeShinyhub() {
		return serverInfo{}, &serverNotReadyError{
			host:   cfg.Host,
			detail: "/api/server-info did not return a shinyhub response",
		}
	}
	return info, nil
}

// serverReadinessProblem probes the server and returns a *serverNotReadyError
// when the host is reachable but is not (yet) a shinyhub server. It returns nil
// when the server is a healthy shinyhub OR is genuinely unreachable (a
// transport failure the caller already classifies as transport/auth). This lets
// a failed authenticated call be re-classified: a 401 from a front proxy on a
// half-provisioned box becomes "server not ready", not "bad credentials".
func serverReadinessProblem(cfg *cliConfig) *serverNotReadyError {
	_, err := probeServer(cfg)
	var nr *serverNotReadyError
	if !errors.As(err, &nr) {
		return nil
	}
	// A 404 most plausibly means an older shinyhub that predates the
	// /api/server-info endpoint, not a half-provisioned host. fetchServerCaps
	// already treats a missing endpoint as "supported (degraded)", so do not
	// override the genuine /api/apps error with misleading "server not ready"
	// advice; let it fall through to the transport/auth classification.
	if nr.status == http.StatusNotFound {
		return nil
	}
	return nr
}

// waitForServerReady polls GET /api/server-info until the server responds as a
// healthy shinyhub or timeout elapses, returning the parsed serverInfo on
// success. It retries through BOTH transport errors (a recycled host not yet
// accepting connections) and not-ready responses (a front proxy answering
// before the shinyhub binary is up) - exactly the EC2-churn window this exists
// for. now/sleep are injected so the loop is testable without real time.
func waitForServerReady(cfg *cliConfig, timeout, interval time.Duration, out io.Writer, now func() time.Time, sleep func(time.Duration)) (serverInfo, error) {
	deadline := now().Add(timeout)
	for attempt := 1; ; attempt++ {
		info, err := probeServer(cfg)
		if err == nil {
			return info, nil
		}
		if !now().Before(deadline) {
			return serverInfo{}, &serverNotReadyError{
				host:   cfg.Host,
				detail: fmt.Sprintf("still not ready after %s", timeout),
			}
		}
		fmt.Fprintf(out, "  waiting for %s to become ready (attempt %d): %v\n", cfg.Host, attempt, err)
		sleep(interval)
	}
}
