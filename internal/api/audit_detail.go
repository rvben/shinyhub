package api

import "encoding/json"

// auditDetailJSON renders an audit event's detail blob.
//
// An audit trail exists so a reader months later can answer what changed, and
// an event carrying only an action and a slug cannot answer that: "deploy
// reports" says nothing about which bundle went out, and "stop reports" does
// not record whether the app was running or already down when the operator hit
// it. Every mutating handler should record the small set of facts that make its
// own event self-explanatory, and this renders them uniformly so the audit
// page and downstream tooling see one shape.
//
// Facts only, never secrets: the detail column is readable by every admin and
// is exported by GET /api/audit.
func auditDetailJSON(fields map[string]any) string {
	b, err := json.Marshal(fields)
	if err != nil {
		return ""
	}
	return string(b)
}
