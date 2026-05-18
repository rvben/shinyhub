package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/rvben/shinyhub/internal/fleet"
)

// sendFleetMutation performs one precondition-gated, run-correlated mutation.
// It applies the audit headers (run id + User-Agent) and the If-Match-style
// preconditions, then maps the response: 2xx -> nil; 409 -> *conflictError
// (apply records it and never blind-retries it); any other >= 300 -> a
// descriptive error carrying the server's error envelope.
func sendFleetMutation(cfg *cliConfig, req *http.Request, slug string, ifDigest, ifManagedBy *string, runID string) error {
	req.Header.Set("Authorization", authHeader(cfg.Token))
	decorateFleetRequest(req, runID)
	setPrecondition(req, ifDigest, ifManagedBy)
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", req.Method, slug, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if isConflict(resp) {
		return &conflictError{slug: slug, msg: serverErrorMessage(body, resp.Status)}
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s failed: HTTP %d: %s",
			req.Method, slug, resp.StatusCode, serverErrorMessage(body, resp.Status))
	}
	return nil
}

// serverErrorMessage extracts the {"error": "..."} envelope when present,
// else the trimmed raw body, else the HTTP status text.
func serverErrorMessage(body []byte, status string) string {
	var env struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &env) == nil && env.Error != "" {
		return env.Error
	}
	if s := string(bytes.TrimSpace(body)); s != "" {
		return s
	}
	return status
}

// patchApp issues PATCH /api/apps/{slug} with the given JSON keys. A JSON
// null managed_by / hibernate_timeout_minutes is the server's "clear it"
// signal. An empty field set is a no-op success (callers can build a body
// unconditionally and let this skip the call).
func patchApp(cfg *cliConfig, slug string, fields map[string]any, ifDigest, ifManagedBy *string, runID string) error {
	if len(fields) == 0 {
		return nil
	}
	b, err := json.Marshal(fields)
	if err != nil {
		return fmt.Errorf("encode patch body for %s: %w", slug, err)
	}
	req, err := http.NewRequest("PATCH", cfg.Host+"/api/apps/"+slug, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return sendFleetMutation(cfg, req, slug, ifDigest, ifManagedBy, runID)
}

// patchManagedBy stamps (value = &"fleet:<id>") or clears (value = nil) the
// ownership marker. A nil value marshals to JSON null, which the server reads
// as "unmanaged".
func patchManagedBy(cfg *cliConfig, slug string, value *string, ifDigest, ifManagedBy *string, runID string) error {
	return patchApp(cfg, slug, map[string]any{"managed_by": value}, ifDigest, ifManagedBy, runID)
}

// fleetConfigBody builds the PATCH body for the reconcilable numeric keys
// the manifest declares. A nil pointer = key not declared = not reconciled.
func fleetConfigBody(c fleet.Config) map[string]any {
	body := map[string]any{}
	if c.HibernateTimeoutMinutes != nil {
		body["hibernate_timeout_minutes"] = *c.HibernateTimeoutMinutes
	}
	if c.Replicas != nil {
		body["replicas"] = *c.Replicas
	}
	if c.MaxSessionsPerReplica != nil {
		body["max_sessions_per_replica"] = *c.MaxSessionsPerReplica
	}
	return body
}

// patchAppAccess issues PATCH /api/apps/{slug}/access {"access": ...}.
func patchAppAccess(cfg *cliConfig, slug, access string, ifDigest, ifManagedBy *string, runID string) error {
	b, _ := json.Marshal(map[string]string{"access": access})
	req, err := http.NewRequest("PATCH", cfg.Host+"/api/apps/"+slug+"/access", bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return sendFleetMutation(cfg, req, slug, ifDigest, ifManagedBy, runID)
}

// deleteFleetApp issues DELETE /api/apps/{slug}. The server removes the code
// dir AND the persistent data dir; prune is gated on this precondition.
func deleteFleetApp(cfg *cliConfig, slug string, ifDigest, ifManagedBy *string, runID string) error {
	req, err := http.NewRequest("DELETE", cfg.Host+"/api/apps/"+slug, nil)
	if err != nil {
		return err
	}
	return sendFleetMutation(cfg, req, slug, ifDigest, ifManagedBy, runID)
}
